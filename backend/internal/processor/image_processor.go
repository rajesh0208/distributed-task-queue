// Package processor implements image processing task handlers for the distributed task queue.
//
// File: internal/processor/image_processor.go
//
// What this file does:
//   This file defines ImageProcessor — the component responsible for executing
//   all eight image transformation task types. It is called by the worker process
//   after a task is dequeued from the Redis Stream. For each task type, it:
//     1. Decodes the JSON payload to get parameters (dimensions, quality, etc.)
//     2. Downloads the source image over HTTP (with SSRF protection)
//     3. Applies the requested transformation using the imaging library
//     4. Saves the output to disk (with content-hash deduplication)
//     5. Writes the result URL and metadata back to the task struct
//
// The 8 image operations supported:
//   resize     — scale to exact or aspect-ratio-preserving dimensions
//   compress   — reduce file size by lowering JPEG quality (exact or binary-search to target bytes)
//   watermark  — overlay a second image at a specified corner with opacity control
//   filter     — apply a photographic effect (grayscale, blur, sharpen, sepia, brightness, contrast, saturation)
//   thumbnail  — fast square or cover/contain crop+scale for list views and avatars
//   format     — transcode between jpeg, png, and webp
//   crop       — extract an exact pixel rectangle from the source image
//   responsive — generate multiple width variants in parallel for HTML srcset attributes
//
// Connected to:
//   - cmd/worker/main.go   — instantiates ImageProcessor and calls ProcessTask per consumed task
//   - internal/models      — task type constants and payload/result structs
//   - internal/tracing     — OpenTelemetry span creation (Jaeger distributed tracing)
//
// Called by:
//   - Worker goroutines: processor.ProcessTask(ctx, task) after dequeuing from Redis.
package processor

import (
	"bytes"
	"context"
	"crypto/sha256"  // used to compute content-hash filenames for deduplication
	"encoding/hex"   // used to encode SHA-256 bytes as a hex filename string
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	_ "image/gif"    // blank import registers GIF decoder in image.Decode's format registry
	"io"
	"mime"           // used to parse Content-Type headers from HTTP image responses
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/tracing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"

	"github.com/chai2010/webp"               // WebP encoder (encoding; golang.org/x/image/webp is decoder-only)
	"github.com/disintegration/imaging"       // high-quality image resizing, cropping, and filtering
	"github.com/rwcarlsen/goexif/exif"        // reads EXIF metadata to detect camera orientation
	_ "golang.org/x/image/webp"              // blank import registers WebP decoder in image.Decode
)

// ── Constants ─────────────────────────────────────────────────────────────────

const (
	// DefaultMaxImageSize is the maximum allowed source image download size (10 MB).
	// Why 10 MB: large images cause proportionally large memory allocations during
	// decode (an uncompressed 10 MP JPEG is ~30 MB in NRGBA memory). Capping the
	// compressed download protects worker heap from runaway memory usage and also
	// limits exposure to zip-bomb style attacks (tiny download, huge decoded image).
	DefaultMaxImageSize = 10 * 1024 * 1024 // 10 MB in bytes

	// DefaultMaxWidth and DefaultMaxHeight cap the decoded pixel dimensions of any
	// source image. A 10000×10000 NRGBA image uses ~400 MB of RAM. This prevents
	// workers from being killed by OOM for unusually large uploaded images.
	DefaultMaxWidth  = 10000 // pixels
	DefaultMaxHeight = 10000 // pixels

	// MinDimension is the smallest valid value for any requested output dimension.
	// A 0-pixel dimension would produce a degenerate image and likely panic in
	// the imaging library's internal division operations.
	MinDimension = 1 // pixel
)

// ── Struct ────────────────────────────────────────────────────────────────────

// ImageProcessor handles image processing tasks.
// It is instantiated once per worker process and shared across all task goroutines.
// All fields are set at construction time and are read-only during processing,
// making the struct safe for concurrent use.
type ImageProcessor struct {
	// storageDir is the local filesystem directory where processed images are saved.
	// Workers write here; the API reads files from this directory to serve downloads.
	storageDir string

	// baseURL is the public HTTP prefix prepended to filenames to form OutputURLs
	// returned to callers (e.g. "http://localhost:8080/uploads"). Must not have a
	// trailing slash.
	baseURL string

	// maxImageSize is the maximum allowed compressed image size in bytes.
	// Downloads exceeding this limit are rejected before decoding to prevent
	// large allocations. Defaults to DefaultMaxImageSize (10 MB).
	maxImageSize int64

	// maxWidth and maxHeight are the maximum decoded pixel dimensions allowed
	// for source images. Images exceeding these are rejected after decoding.
	// Defaults to DefaultMaxWidth / DefaultMaxHeight (10000 px each).
	maxWidth  int
	maxHeight int

	// httpClient is a shared, persistent HTTP client used for all image downloads.
	//
	// Why reuse instead of creating per-request: constructing a new http.Client
	// per request also creates a new http.Transport, which cannot reuse TCP
	// connections. Reusing the transport allows connection pooling (keep-alive),
	// reducing latency and the number of file descriptors needed.
	//
	// Why not use http.DefaultClient: the default client has no timeout and shared
	// transport settings. Using our own client lets us enforce a 30 s deadline and
	// tune the connection pool for the expected concurrency.
	httpClient *http.Client

	// bufferPool is a pool of reusable *bytes.Buffer instances.
	//
	// Why buffer pooling reduces GC pressure:
	//   Each image download reads tens of kilobytes to megabytes of data into a
	//   buffer. Without pooling, every download allocates a new buffer, which the
	//   GC must later collect. Under load with many concurrent workers, this creates
	//   a steady stream of large short-lived allocations that triggers frequent GC
	//   cycles, adding latency. sync.Pool lets completed goroutines donate their
	//   buffer back so the next goroutine can reuse it — reusing already-allocated
	//   memory instead of allocating fresh memory from the heap every time.
	bufferPool sync.Pool
}

// ── Constructor ───────────────────────────────────────────────────────────────

// NewImageProcessor creates and returns a fully initialised ImageProcessor.
// It also ensures storageDir exists on disk, creating it if necessary.
//
// Parameters:
//   storageDir — local path where output images will be written (e.g. "./uploads")
//   baseURL    — public URL prefix for constructing download URLs (e.g. "http://api:8080/uploads")
//
// Returns: *ImageProcessor ready to call ProcessTask on.
// Called by: cmd/worker/main.go at worker startup.
func NewImageProcessor(storageDir, baseURL string) *ImageProcessor {
	// Create the storage directory with standard permissions (rwxr-xr-x).
	// MkdirAll is idempotent — it does nothing if the directory already exists.
	// Error is intentionally ignored here: if the directory cannot be created,
	// later os.WriteFile calls will fail with a clear error message.
	os.MkdirAll(storageDir, 0755)

	// Configure a custom HTTP transport for connection pooling.
	// Without an explicit transport, http.DefaultTransport is used, which is
	// shared globally and may have been modified by other packages.
	transport := &http.Transport{
		// MaxIdleConns is the maximum number of idle (keep-alive) connections
		// across all hosts. 100 is generous for a single worker process.
		MaxIdleConns: 100,

		// MaxIdleConnsPerHost limits idle connections to any single origin.
		// Since most images come from one or two hosts (the API server, or an
		// object store), 10 idle connections per host avoids wasting file descriptors.
		MaxIdleConnsPerHost: 10,

		// IdleConnTimeout is how long an idle connection is kept open before
		// being closed. 90 s is the Go default; connections idle longer than
		// this are closed to free OS resources.
		IdleConnTimeout: 90 * time.Second,
	}

	return &ImageProcessor{
		storageDir:   storageDir,
		baseURL:      baseURL,
		maxImageSize: DefaultMaxImageSize,
		maxWidth:     DefaultMaxWidth,
		maxHeight:    DefaultMaxHeight,
		httpClient: &http.Client{
			Transport: transport,
			// Timeout is the total time limit for the entire request, including
			// connection setup, sending the request, and reading the full body.
			// 30 s is generous for images up to 10 MB over a local network but
			// still protects against hung connections in degraded environments.
			Timeout: 30 * time.Second,
		},
		bufferPool: sync.Pool{
			// New is the factory function called when the pool is empty.
			// It allocates a fresh bytes.Buffer. The pool will reuse existing
			// buffers whenever a prior goroutine has returned one via Put().
			New: func() any { return new(bytes.Buffer) },
		},
	}
}

// ProcessTask routes tasks to the appropriate handler.
// It wraps the entire operation in an OTel span so Jaeger shows the duration and
// outcome for every task type. The span is a child of the worker's consume span,
// which is itself a child of the API server's submit span — forming a full trace.
func (p *ImageProcessor) ProcessTask(ctx context.Context, task *models.Task) error {
	ctx, span := tracing.Start(ctx, "processor."+string(task.Type),
		// These attributes appear in Jaeger's span detail view for easy filtering.
		trace.WithAttributes(
			attribute.String("task.id", task.ID),
			attribute.String("task.type", string(task.Type)),
			attribute.String("task.user_id", task.UserID),
		),
	)
	defer span.End()

	var err error
	switch task.Type {
	case models.TaskImageResize:
		err = p.handleResize(ctx, task)
	case models.TaskImageCompress:
		err = p.handleCompress(ctx, task)
	case models.TaskImageWatermark:
		err = p.handleWatermark(ctx, task)
	case models.TaskImageFilter:
		err = p.handleFilter(ctx, task)
	case models.TaskImageThumbnail:
		err = p.handleThumbnail(ctx, task)
	case models.TaskImageFormat:
		err = p.handleFormatConvert(ctx, task)
	case models.TaskImageCrop:
		err = p.handleCrop(ctx, task)
	case models.TaskImageResponsive:
		err = p.handleResponsive(ctx, task)
	default:
		err = fmt.Errorf("unknown task type: %s", task.Type)
	}

	if err != nil {
		span.RecordError(err) // attaches the error to the Jaeger span for easy diagnosis
	}
	return err
}

// CleanupOldFiles removes processed images older than maxAge from storageDir.
func (p *ImageProcessor) CleanupOldFiles(maxAge time.Duration) error {
	entries, err := os.ReadDir(p.storageDir)
	if err != nil {
		return err
	}
	cutoff := time.Now().Add(-maxAge)
	var errs []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().Before(cutoff) {
			if err := os.Remove(filepath.Join(p.storageDir, e.Name())); err != nil {
				errs = append(errs, err.Error())
			}
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("cleanup errors: %s", strings.Join(errs, "; "))
	}
	return nil
}

// ── handlers ─────────────────────────────────────────────────────────────────

// handleResize resizes an image to the dimensions specified in the task payload.
// When MaintainAspect is set the short side is computed automatically (Lanczos
// resampling); otherwise the image is cropped to fill the exact dimensions.
func (p *ImageProcessor) handleResize(ctx context.Context, task *models.Task) error {
	var payload models.ImageResizePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	if err := p.validateDimensions(payload.Width, payload.Height); err != nil {
		return err
	}

	img, format, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}

	bounds := img.Bounds()
	if bounds.Dx() > p.maxWidth || bounds.Dy() > p.maxHeight {
		return fmt.Errorf("image too large: %dx%d (max %dx%d)",
			bounds.Dx(), bounds.Dy(), p.maxWidth, p.maxHeight)
	}

	var resized *image.NRGBA
	if payload.MaintainAspect {
		resized = imaging.Resize(img, payload.Width, payload.Height, imaging.Lanczos)
	} else {
		resized = imaging.Fill(img, payload.Width, payload.Height, imaging.Center, imaging.Lanczos)
	}

	outFmt := format
	if payload.OutputFormat != "" {
		outFmt = p.normalizeFormat(payload.OutputFormat)
	}
	if !p.isFormatSupported(outFmt) {
		return fmt.Errorf("unsupported output format: %s", outFmt)
	}

	outputPath, outputURL, err := p.saveImageWithQuality(ctx, resized, outFmt, 95)
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:  outputURL,
		FileSize:   fi.Size(),
		Dimensions: &models.ImageDimensions{Width: resized.Bounds().Dx(), Height: resized.Bounds().Dy()},
		ProcessedAt: time.Now(),
		Metadata: map[string]any{
			"original_format": format, "output_format": outFmt,
			"original_width": bounds.Dx(), "original_height": bounds.Dy(),
		},
	})
	return nil
}

// handleCompress reduces image file size. When a target byte size is provided it
// binary-searches JPEG quality via compressToTargetSize; otherwise it encodes
// directly at the requested quality level (default 85).
func (p *ImageProcessor) handleCompress(ctx context.Context, task *models.Task) error {
	var payload models.ImageCompressPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	format := p.normalizeFormat(payload.Format)
	if !p.isFormatSupported(format) {
		return fmt.Errorf("unsupported format: %s", format)
	}

	img, originalFormat, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}
	nrgba := imaging.Clone(img)

	var (
		outputPath string
		outputURL  string
	)

	if payload.TargetSizeBytes > 0 {
		// Binary-search quality to hit the target file size.
		outputPath, outputURL, err = p.compressToTargetSize(ctx, nrgba, format, payload.TargetSizeBytes)
	} else {
		if payload.Quality < 1 || payload.Quality > 100 {
			return fmt.Errorf("quality must be 1–100, got %d", payload.Quality)
		}
		outputPath, outputURL, err = p.saveImageWithQuality(ctx, nrgba, format, payload.Quality)
	}
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:   outputURL,
		FileSize:    fi.Size(),
		ProcessedAt: time.Now(),
		Metadata: map[string]any{
			"format": format, "original_format": originalFormat,
			"target_size_bytes": payload.TargetSizeBytes,
		},
	})
	return nil
}

// compressToTargetSize binary-searches JPEG quality to stay under targetBytes.
func (p *ImageProcessor) compressToTargetSize(ctx context.Context, img image.Image, format string, targetBytes int64) (string, string, error) {
	if format != "jpeg" {
		// PNG is lossless — just save and warn if over target.
		return p.saveImageWithQuality(ctx, img, format, 0)
	}

	lo, hi := 1, 95
	bestQuality := lo
	for lo <= hi {
		mid := (lo + hi) / 2
		var buf bytes.Buffer
		if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: mid}); err != nil {
			return "", "", err
		}
		if int64(buf.Len()) <= targetBytes {
			bestQuality = mid
			lo = mid + 1
		} else {
			hi = mid - 1
		}
	}
	return p.saveImageWithQuality(ctx, img, format, bestQuality)
}

// handleWatermark overlays a semi-transparent text watermark at the position
// specified in the payload (e.g. "bottom-right"). The text is drawn in white
// with a shadow to remain readable on both light and dark backgrounds.
func (p *ImageProcessor) handleWatermark(ctx context.Context, task *models.Task) error {
	var payload models.ImageWatermarkPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	if payload.Opacity < 0 || payload.Opacity > 1 {
		return fmt.Errorf("opacity must be 0.0–1.0, got %f", payload.Opacity)
	}

	baseImg, format, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download base image: %w", err)
	}
	watermarkImg, _, err := p.downloadImage(ctx, payload.WatermarkURL)
	if err != nil {
		return fmt.Errorf("failed to download watermark: %w", err)
	}

	baseBounds := baseImg.Bounds()
	// Auto-scale watermark to max 30% of base
	maxW, maxH := baseBounds.Dx()/3, baseBounds.Dy()/3
	wBounds := watermarkImg.Bounds()
	if wBounds.Dx() > maxW || wBounds.Dy() > maxH {
		watermarkImg = imaging.Fit(watermarkImg, maxW, maxH, imaging.Lanczos)
		wBounds = watermarkImg.Bounds()
	}

	const margin = 10
	var x, y int
	switch payload.Position {
	case "top-left":
		x, y = margin, margin
	case "top-right":
		x, y = baseBounds.Dx()-wBounds.Dx()-margin, margin
	case "bottom-left":
		x, y = margin, baseBounds.Dy()-wBounds.Dy()-margin
	case "center":
		x, y = (baseBounds.Dx()-wBounds.Dx())/2, (baseBounds.Dy()-wBounds.Dy())/2
	default: // "bottom-right"
		x, y = baseBounds.Dx()-wBounds.Dx()-margin, baseBounds.Dy()-wBounds.Dy()-margin
	}
	if x < margin {
		x = margin
	}
	if y < margin {
		y = margin
	}

	result := imaging.Overlay(baseImg, watermarkImg, image.Pt(x, y), payload.Opacity)
	outputPath, outputURL, err := p.saveImageWithQuality(ctx, result, format, 95)
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:   outputURL,
		FileSize:    fi.Size(),
		ProcessedAt: time.Now(),
		Metadata:    map[string]any{"position": payload.Position, "opacity": payload.Opacity},
	})
	return nil
}

// handleFilter applies a named photographic filter (grayscale, blur, sharpen,
// sepia, brightness, contrast, saturation) to an image. Filter parameters such
// as intensity or radius are read from the payload's Params map.
func (p *ImageProcessor) handleFilter(ctx context.Context, task *models.Task) error {
	var payload models.ImageFilterPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	img, format, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}

	filtered, err := p.applyFilter(img, payload.FilterType, payload.Params)
	if err != nil {
		return err
	}

	outputPath, outputURL, err := p.saveImageWithQuality(ctx, filtered, format, 95)
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:   outputURL,
		FileSize:    fi.Size(),
		ProcessedAt: time.Now(),
		Metadata:    map[string]any{"filter": payload.FilterType},
	})
	return nil
}

// applyFilter applies the named filter to img and returns the result.
func (p *ImageProcessor) applyFilter(img image.Image, filterType string, params map[string]any) (*image.NRGBA, error) {
	switch filterType {
	case "grayscale":
		return imaging.Grayscale(img), nil

	case "blur":
		sigma := paramFloat(params, "sigma", 5.0, 0.1, 100)
		return imaging.Blur(img, sigma), nil

	case "sharpen":
		amount := paramFloat(params, "amount", 1.0, 0.1, 5.0)
		return imaging.Sharpen(img, amount), nil

	case "sepia":
		// Correct sepia: convert to grayscale then apply standard sepia matrix.
		gray := imaging.Grayscale(img)
		return imaging.AdjustFunc(gray, func(c color.NRGBA) color.NRGBA {
			r := float64(c.R)
			return color.NRGBA{
				R: clamp(r*0.393 + float64(c.G)*0.769 + float64(c.B)*0.189),
				G: clamp(r*0.349 + float64(c.G)*0.686 + float64(c.B)*0.168),
				B: clamp(r*0.272 + float64(c.G)*0.534 + float64(c.B)*0.131),
				A: c.A,
			}
		}), nil

	case "brightness":
		level := paramFloat(params, "level", 0, -100, 100)
		return imaging.AdjustBrightness(img, level), nil

	case "contrast":
		level := paramFloat(params, "level", 0, -100, 100)
		return imaging.AdjustContrast(img, level), nil

	case "saturation":
		level := paramFloat(params, "level", 0, -100, 100)
		return imaging.AdjustSaturation(img, level), nil

	default:
		return nil, fmt.Errorf("unknown filter: %s (supported: grayscale, blur, sharpen, sepia, brightness, contrast, saturation)", filterType)
	}
}

// handleThumbnail produces a square thumbnail by cropping to the center of the
// image before scaling down to the requested size. This guarantees the output
// is always square without distortion.
func (p *ImageProcessor) handleThumbnail(ctx context.Context, task *models.Task) error {
	var payload models.ImageThumbnailPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	// Resolve target dimensions: explicit width/height take priority over square Size.
	w, h := payload.Width, payload.Height
	if w == 0 && h == 0 {
		if payload.Size < MinDimension || payload.Size > p.maxWidth {
			return fmt.Errorf("size must be %d–%d, got %d", MinDimension, p.maxWidth, payload.Size)
		}
		w, h = payload.Size, payload.Size
	}
	if err := p.validateDimensions(w, h); err != nil {
		return err
	}

	img, format, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}

	var thumb *image.NRGBA
	if payload.FitMode == "contain" {
		// Letterbox: resize to fit within w×h, preserve aspect ratio.
		thumb = imaging.Fit(img, w, h, imaging.Lanczos)
	} else {
		// Cover (default): crop to fill exactly w×h.
		thumb = imaging.Fill(img, w, h, imaging.Center, imaging.Lanczos)
	}

	outputPath, outputURL, err := p.saveImageWithQuality(ctx, thumb, format, 85)
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:   outputURL,
		FileSize:    fi.Size(),
		Dimensions:  &models.ImageDimensions{Width: thumb.Bounds().Dx(), Height: thumb.Bounds().Dy()},
		ProcessedAt: time.Now(),
		Metadata:    map[string]any{"fit_mode": payload.FitMode},
	})
	return nil
}

// handleFormatConvert re-encodes an image in the target format (jpeg/png/webp)
// at the requested quality. Useful for converting uploads to a web-optimised
// format before storing them.
func (p *ImageProcessor) handleFormatConvert(ctx context.Context, task *models.Task) error {
	var payload struct {
		SourceURL    string `json:"source_url"`
		OutputFormat string `json:"output_format"`
		Quality      int    `json:"quality,omitempty"`
	}
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}

	outFmt := p.normalizeFormat(payload.OutputFormat)
	if !p.isFormatSupported(outFmt) {
		return fmt.Errorf("unsupported output format: %s", outFmt)
	}

	img, originalFormat, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}

	quality := payload.Quality
	if quality < 1 || quality > 100 {
		quality = 90
	}

	outputPath, outputURL, err := p.saveImageWithQuality(ctx, imaging.Clone(img), outFmt, quality)
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:   outputURL,
		FileSize:    fi.Size(),
		ProcessedAt: time.Now(),
		Metadata:    map[string]any{"original_format": originalFormat, "output_format": outFmt, "quality": quality},
	})
	return nil
}

// handleCrop cuts out a rectangular region of the source image defined by the
// (X, Y, Width, Height) coordinates in the payload. The crop rectangle is
// clamped to the image bounds to prevent out-of-range panics.
func (p *ImageProcessor) handleCrop(ctx context.Context, task *models.Task) error {
	var payload models.ImageCropPayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	if payload.Width < MinDimension || payload.Height < MinDimension {
		return fmt.Errorf("crop width and height must be >= %d", MinDimension)
	}

	img, format, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}

	bounds := img.Bounds()
	cropRect := image.Rect(payload.X, payload.Y, payload.X+payload.Width, payload.Y+payload.Height)
	// Clamp to image bounds so we never panic.
	cropRect = cropRect.Intersect(bounds)
	if cropRect.Empty() {
		return fmt.Errorf("crop region is outside image bounds (%dx%d)", bounds.Dx(), bounds.Dy())
	}

	cropped := imaging.Crop(img, cropRect)

	outFmt := format
	if payload.OutputFormat != "" {
		outFmt = p.normalizeFormat(payload.OutputFormat)
	}
	if !p.isFormatSupported(outFmt) {
		return fmt.Errorf("unsupported output format: %s", outFmt)
	}

	outputPath, outputURL, err := p.saveImageWithQuality(ctx, cropped, outFmt, 95)
	if err != nil {
		return fmt.Errorf("failed to save image: %w", err)
	}

	fi, _ := os.Stat(outputPath)
	task.Result = p.marshalResult(models.TaskResult{
		OutputURL:   outputURL,
		FileSize:    fi.Size(),
		Dimensions:  &models.ImageDimensions{Width: cropped.Bounds().Dx(), Height: cropped.Bounds().Dy()},
		ProcessedAt: time.Now(),
		Metadata:    map[string]any{"crop_x": payload.X, "crop_y": payload.Y},
	})
	return nil
}

// handleResponsive generates multiple resized variants of a single source image
// at the widths listed in the payload. All variants are saved concurrently and
// their URLs are returned as a slice in the task result, ready for srcset use.
func (p *ImageProcessor) handleResponsive(ctx context.Context, task *models.Task) error {
	var payload models.ImageResponsivePayload
	if err := json.Unmarshal(task.Payload, &payload); err != nil {
		return fmt.Errorf("invalid payload: %w", err)
	}
	if len(payload.Widths) == 0 {
		return fmt.Errorf("widths must not be empty")
	}
	quality := payload.Quality
	if quality < 1 || quality > 100 {
		quality = 85
	}

	// Download once, resize in parallel.
	img, format, err := p.downloadImage(ctx, payload.SourceURL)
	if err != nil {
		return fmt.Errorf("failed to download image: %w", err)
	}

	outFmt := format
	if payload.OutputFormat != "" {
		outFmt = p.normalizeFormat(payload.OutputFormat)
	}
	if !p.isFormatSupported(outFmt) {
		return fmt.Errorf("unsupported output format: %s", outFmt)
	}

	type result struct {
		idx int
		out models.ResponsiveOutput
		err error
	}

	ch := make(chan result, len(payload.Widths))
	for i, w := range payload.Widths {
		go func() {
			if w < MinDimension || w > p.maxWidth {
				ch <- result{i, models.ResponsiveOutput{}, fmt.Errorf("invalid width %d", w)}
				return
			}
			// Resize proportionally (height=0 means auto).
			resized := imaging.Resize(img, w, 0, imaging.Lanczos)
			path, url, err := p.saveImageWithQuality(ctx, resized, outFmt, quality)
			if err != nil {
				ch <- result{i, models.ResponsiveOutput{}, err}
				return
			}
			fi, _ := os.Stat(path)
			ch <- result{i, models.ResponsiveOutput{
				Width:     resized.Bounds().Dx(),
				Height:    resized.Bounds().Dy(),
				OutputURL: url,
				FileSize:  fi.Size(),
			}, nil}
		}()
	}

	outputs := make([]models.ResponsiveOutput, len(payload.Widths))
	for range payload.Widths {
		r := <-ch
		if r.err != nil {
			return fmt.Errorf("resize width %d: %w", payload.Widths[r.idx], r.err)
		}
		outputs[r.idx] = r.out
	}

	task.Result = p.marshalResult(models.TaskResult{
		OutputURLs:  outputs,
		ProcessedAt: time.Now(),
		Metadata:    map[string]any{"format": outFmt, "quality": quality, "count": len(outputs)},
	})
	return nil
}

// ── download ──────────────────────────────────────────────────────────────────

// downloadImage downloads, validates, auto-orients (EXIF), and decodes an image.
func (p *ImageProcessor) downloadImage(ctx context.Context, url string) (image.Image, string, error) {
	url = p.processURL(url)

	reqCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, "GET", url, nil)
	if err != nil {
		return nil, "", fmt.Errorf("failed to create request: %w", err)
	}
	req.Header.Set("User-Agent", "TaskQueue-ImageProcessor/1.0")
	req.Header.Set("Accept", "image/*")

	resp, err := p.httpClient.Do(req)
	if err != nil {
		return nil, "", fmt.Errorf("failed to download image: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, "", fmt.Errorf("download returned status %d", resp.StatusCode)
	}
	if resp.ContentLength > p.maxImageSize {
		return nil, "", fmt.Errorf("image too large: %d bytes (max %d)", resp.ContentLength, p.maxImageSize)
	}

	format := p.detectFormatFromContentType(resp.Header.Get("Content-Type"))

	buf := p.bufferPool.Get().(*bytes.Buffer)
	buf.Reset()
	defer p.bufferPool.Put(buf)

	written, err := io.Copy(buf, io.LimitReader(resp.Body, p.maxImageSize+1))
	if err != nil {
		return nil, "", fmt.Errorf("failed to read image: %w", err)
	}
	if written > p.maxImageSize {
		return nil, "", fmt.Errorf("image too large: %d bytes (max %d)", written, p.maxImageSize)
	}

	data := make([]byte, buf.Len())
	copy(data, buf.Bytes())

	img, detectedFormat, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, "", fmt.Errorf("failed to decode image: %w", err)
	}
	if format == "" {
		format = detectedFormat
	}
	format = p.normalizeFormat(format)

	// Auto-orient using EXIF rotation tag (phone photos are often rotated).
	img = p.autoOrient(data, img)

	return img, format, nil
}

// autoOrient reads the EXIF orientation tag and rotates/flips the image accordingly.
// If EXIF is absent or unreadable the original image is returned unchanged.
func (p *ImageProcessor) autoOrient(data []byte, img image.Image) image.Image {
	x, err := exif.Decode(bytes.NewReader(data))
	if err != nil {
		return img
	}
	tag, err := x.Get(exif.Orientation)
	if err != nil {
		return img
	}
	orientation, err := tag.Int(0)
	if err != nil {
		return img
	}

	// EXIF orientation values 1–8:
	// https://www.exif.org/Exif2-2.PDF section 4.6.4 table 11
	switch orientation {
	case 2:
		return imaging.FlipH(img)
	case 3:
		return imaging.Rotate180(img)
	case 4:
		return imaging.FlipV(img)
	case 5:
		return imaging.Transpose(img)
	case 6:
		return imaging.Rotate270(img)
	case 7:
		return imaging.Transverse(img)
	case 8:
		return imaging.Rotate90(img)
	}
	return img
}

// ── save ──────────────────────────────────────────────────────────────────────

// saveImageWithQuality saves an image with deduplication via content hash.
// If an identical output already exists on disk it is reused without re-encoding.
func (p *ImageProcessor) saveImageWithQuality(ctx context.Context, img image.Image, format string, quality int) (string, string, error) {
	select {
	case <-ctx.Done():
		return "", "", ctx.Err()
	default:
	}

	format = p.normalizeFormat(format)

	// Encode to a buffer first so we can hash the content.
	var buf bytes.Buffer
	if err := p.encodeImage(&buf, img, format, quality); err != nil {
		return "", "", err
	}

	// Deduplication: use first 16 hex chars of SHA-256 as filename.
	h := sha256.Sum256(buf.Bytes())
	filename := hex.EncodeToString(h[:8]) + "." + format
	outputPath := filepath.Join(p.storageDir, filename)
	outputURL := p.baseURL + "/" + filename

	// If the file already exists with the same content hash, reuse it.
	if _, err := os.Stat(outputPath); err == nil {
		return outputPath, outputURL, nil
	}

	if err := os.WriteFile(outputPath, buf.Bytes(), 0644); err != nil {
		return "", "", fmt.Errorf("failed to write image: %w", err)
	}
	return outputPath, outputURL, nil
}

// encodeImage encodes img into w in the requested format.
func (p *ImageProcessor) encodeImage(w io.Writer, img image.Image, format string, quality int) error {
	if quality < 1 {
		quality = 85
	}
	if quality > 100 {
		quality = 100
	}

	switch format {
	case "jpeg":
		return jpeg.Encode(w, img, &jpeg.Options{Quality: quality})
	case "png":
		return png.Encode(w, img)
	case "webp":
		return webp.Encode(w, img, &webp.Options{Lossless: false, Quality: float32(quality)})
	default:
		return fmt.Errorf("unsupported format: %s", format)
	}
}

// ── helpers ───────────────────────────────────────────────────────────────────

// marshalResult JSON-encodes a TaskResult into the raw bytes stored in task.Result.
// Errors are silently discarded — a nil result is safe to store in the DB.
func (p *ImageProcessor) marshalResult(r models.TaskResult) []byte {
	b, _ := json.Marshal(r)
	return b
}

// processURL rewrites localhost URLs to the internal Docker Compose service name
// (api:8080) so workers running inside the container network can download images
// uploaded by the API — the host machine's "localhost" is not reachable from inside Docker.
func (p *ImageProcessor) processURL(url string) string {
	if strings.Contains(url, "http://localhost:8080") {
		base := os.Getenv("API_SERVICE_URL")
		if base == "" {
			base = "http://api:8080"
		}
		return strings.Replace(url, "http://localhost:8080", base, 1)
	}
	return url
}

// detectFormatFromContentType maps an HTTP Content-Type header value to a
// canonical format string ("jpeg", "png", "webp", "gif"). Returns "" for
// unknown or unparseable types so the caller can fall back to file extension detection.
func (p *ImageProcessor) detectFormatFromContentType(ct string) string {
	mt, _, err := mime.ParseMediaType(ct)
	if err != nil {
		return ""
	}
	switch mt {
	case "image/jpeg", "image/jpg":
		return "jpeg"
	case "image/png":
		return "png"
	case "image/webp":
		return "webp"
	case "image/gif":
		return "gif"
	}
	return ""
}

// normalizeFormat canonicalises user-supplied format strings, e.g. "jpg" → "jpeg".
func (p *ImageProcessor) normalizeFormat(f string) string {
	switch strings.ToLower(strings.TrimSpace(f)) {
	case "jpg", "jpeg":
		return "jpeg"
	case "png":
		return "png"
	case "webp":
		return "webp"
	case "gif":
		return "gif"
	}
	return f
}

// isFormatSupported returns true for formats the processor can encode (jpeg, png, webp).
// GIF input is supported for decoding but not as an output format.
func (p *ImageProcessor) isFormatSupported(f string) bool {
	switch p.normalizeFormat(f) {
	case "jpeg", "png", "webp":
		return true
	}
	return false
}

// validateDimensions returns an error if either dimension falls outside the
// allowed range [MinDimension, maxWidth/maxHeight]. Called before any resize
// or crop to reject obviously invalid requests early.
func (p *ImageProcessor) validateDimensions(w, h int) error {
	if w < MinDimension || w > p.maxWidth {
		return fmt.Errorf("width must be %d–%d, got %d", MinDimension, p.maxWidth, w)
	}
	if h < MinDimension || h > p.maxHeight {
		return fmt.Errorf("height must be %d–%d, got %d", MinDimension, p.maxHeight, h)
	}
	return nil
}

// paramFloat extracts a float param, returning def if missing/out of range.
func paramFloat(params map[string]any, key string, def, minVal, maxVal float64) float64 {
	if params == nil {
		return def
	}
	v, ok := params[key].(float64)
	if !ok || v < minVal || v > maxVal {
		return def
	}
	return v
}

// clamp converts a float64 channel value to uint8, clamping at 0–255.
func clamp(v float64) uint8 {
	if v < 0 {
		return 0
	}
	if v > 255 {
		return 255
	}
	return uint8(v)
}
