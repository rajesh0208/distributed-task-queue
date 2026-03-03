package models

import (
	"encoding/json"
	"time"
)

type TaskStatus string

const (
	StatusQueued     TaskStatus = "queued"
	StatusProcessing TaskStatus = "processing"
	StatusCompleted  TaskStatus = "completed"
	StatusFailed     TaskStatus = "failed"
	StatusRetrying   TaskStatus = "retrying"
	StatusCancelled  TaskStatus = "cancelled"
)

type TaskType string

const (
	TaskImageResize     TaskType = "image_resize"
	TaskImageCompress   TaskType = "image_compress"
	TaskImageWatermark  TaskType = "image_watermark"
	TaskImageFilter     TaskType = "image_filter"
	TaskImageThumbnail  TaskType = "image_thumbnail"
	TaskImageFormat     TaskType = "image_format_convert"
	TaskImageCrop       TaskType = "image_crop"
	TaskImageResponsive TaskType = "image_responsive"
)

// Task represents a single unit of async work. Each task belongs to a user and
// optionally to a Batch when submitted via the batch endpoint.
type Task struct {
	ID             string          `json:"id" db:"id"`
	UserID         string          `json:"user_id" db:"user_id"`
	BatchID        string          `json:"batch_id,omitempty" db:"batch_id"`
	Type           TaskType        `json:"type" db:"type"`
	Payload        json.RawMessage `json:"payload" db:"payload"`
	Status         TaskStatus      `json:"status" db:"status"`
	Priority       int             `json:"priority" db:"priority"`
	Result         json.RawMessage `json:"result,omitempty" db:"result"`
	Error          string          `json:"error,omitempty" db:"error"`
	Retries        int             `json:"retries" db:"retries"`
	MaxRetries     int             `json:"max_retries" db:"max_retries"`
	CreatedAt      time.Time       `json:"created_at" db:"created_at"`
	StartedAt      *time.Time      `json:"started_at,omitempty" db:"started_at"`
	CompletedAt    *time.Time      `json:"completed_at,omitempty" db:"completed_at"`
	WorkerID       string          `json:"worker_id,omitempty" db:"worker_id"`
	ProcessingTime int64           `json:"processing_time,omitempty" db:"processing_time"`
}

// BatchStatus summarises the overall state of a batch of tasks.
type BatchStatus string

const (
	BatchStatusQueued     BatchStatus = "queued"     // all tasks still waiting
	BatchStatusProcessing BatchStatus = "processing" // at least one task is being worked on
	BatchStatusCompleted  BatchStatus = "completed"  // every task finished successfully
	BatchStatusPartial    BatchStatus = "partial"    // done but some tasks failed
	BatchStatusFailed     BatchStatus = "failed"     // every task failed
)

// Batch groups multiple tasks submitted together so the caller can track overall
// progress with a single ID instead of polling each task individually.
type Batch struct {
	ID          string      `json:"id" db:"id"`
	UserID      string      `json:"user_id" db:"user_id"`
	Type        TaskType    `json:"type" db:"type"`
	Status      BatchStatus `json:"status" db:"status"`
	Total       int         `json:"total" db:"total"`
	Queued      int         `json:"queued" db:"queued"`
	Processing  int         `json:"processing" db:"processing"`
	Completed   int         `json:"completed" db:"completed"`
	Failed      int         `json:"failed" db:"failed"`
	CreatedAt   time.Time   `json:"created_at" db:"created_at"`
	CompletedAt *time.Time  `json:"completed_at,omitempty" db:"completed_at"`
}

// BatchSubmitRequest is the body for POST /api/v1/tasks/batch.
// Every item in Images gets its own Task with the same Type and priority.
type BatchSubmitRequest struct {
	Type       TaskType          `json:"type"`
	Images     []json.RawMessage `json:"images"`     // one payload per image
	Priority   int               `json:"priority"`
	MaxRetries int               `json:"max_retries"`
}

// BatchSubmitResponse is returned after a successful batch submission.
type BatchSubmitResponse struct {
	BatchID   string    `json:"batch_id"`
	Total     int       `json:"total"`
	Status    string    `json:"status"`
	CreatedAt time.Time `json:"created_at"`
}

type ImageResizePayload struct {
	SourceURL      string `json:"source_url"`
	Width          int    `json:"width"`
	Height         int    `json:"height"`
	MaintainAspect bool   `json:"maintain_aspect"`
	OutputFormat   string `json:"output_format,omitempty"`
}

// ImageCompressPayload supports both fixed quality and target-size compression.
// If TargetSizeBytes > 0 it overrides Quality and binary-searches for the right quality.
type ImageCompressPayload struct {
	SourceURL       string `json:"source_url"`
	Quality         int    `json:"quality"`
	Format          string `json:"format"`
	TargetSizeBytes int64  `json:"target_size_bytes,omitempty"`
}

type ImageWatermarkPayload struct {
	SourceURL    string  `json:"source_url"`
	WatermarkURL string  `json:"watermark_url"`
	Position     string  `json:"position"`
	Opacity      float64 `json:"opacity"`
}

type ImageFilterPayload struct {
	SourceURL  string                 `json:"source_url"`
	FilterType string                 `json:"filter_type"`
	Params     map[string]any `json:"params,omitempty"`
}

// ImageThumbnailPayload supports both square (Size) and rectangular (Width x Height) thumbnails.
// FitMode: "cover" (crop to fill, default) or "contain" (letterbox).
type ImageThumbnailPayload struct {
	SourceURL string `json:"source_url"`
	Size      int    `json:"size,omitempty"`   // square shorthand
	Width     int    `json:"width,omitempty"`  // explicit width
	Height    int    `json:"height,omitempty"` // explicit height
	FitMode   string `json:"fit_mode,omitempty"`
}

// ImageCropPayload crops a rectangular region from the source image.
type ImageCropPayload struct {
	SourceURL    string `json:"source_url"`
	X            int    `json:"x"`
	Y            int    `json:"y"`
	Width        int    `json:"width"`
	Height       int    `json:"height"`
	OutputFormat string `json:"output_format,omitempty"`
}

// ImageResponsivePayload produces multiple resized outputs from a single download.
// Returns one output URL per requested width in the result.
type ImageResponsivePayload struct {
	SourceURL    string `json:"source_url"`
	Widths       []int  `json:"widths"`
	OutputFormat string `json:"output_format,omitempty"` // default: same as source
	Quality      int    `json:"quality,omitempty"`       // default: 85
}

type TaskResult struct {
	OutputURL    string                 `json:"output_url,omitempty"`
	OutputURLs   []ResponsiveOutput     `json:"output_urls,omitempty"` // for image_responsive
	FileSize     int64                  `json:"file_size,omitempty"`
	Dimensions   *ImageDimensions       `json:"dimensions,omitempty"`
	ProcessedAt  time.Time              `json:"processed_at"`
	Metadata     map[string]any `json:"metadata,omitempty"`
}

type ResponsiveOutput struct {
	Width     int    `json:"width"`
	Height    int    `json:"height"`
	OutputURL string `json:"output_url"`
	FileSize  int64  `json:"file_size"`
}

type ImageDimensions struct {
	Width  int `json:"width"`
	Height int `json:"height"`
}

type WorkerMetrics struct {
	WorkerID         string    `json:"worker_id" db:"worker_id"`
	Status           string    `json:"status" db:"status"`
	TasksProcessed   int       `json:"tasks_processed" db:"tasks_processed"`
	TasksFailed      int       `json:"tasks_failed" db:"tasks_failed"`
	LastHeartbeat    time.Time `json:"last_heartbeat" db:"last_heartbeat"`
	StartTime        time.Time `json:"start_time" db:"start_time"`
	CurrentTask      *string   `json:"current_task,omitempty" db:"current_task"`
	ActiveGoroutines int       `json:"active_goroutines" db:"active_goroutines"`
}

type SystemMetrics struct {
	QueuedTasks       int64     `json:"queued_tasks"`
	ProcessingTasks   int64     `json:"processing_tasks"`
	CompletedTasks    int64     `json:"completed_tasks"`
	FailedTasks       int64     `json:"failed_tasks"`
	ActiveWorkers     int       `json:"active_workers"`
	Throughput        float64   `json:"throughput"`
	AvgProcessingTime float64   `json:"avg_processing_time"`
	Timestamp         time.Time `json:"timestamp"`
}

type SubmitTaskRequest struct {
	Type       TaskType        `json:"type"`
	Payload    json.RawMessage `json:"payload"`
	Priority   int             `json:"priority"`
	MaxRetries int             `json:"max_retries"`
}

type SubmitTaskResponse struct {
	TaskID    string     `json:"task_id"`
	Status    TaskStatus `json:"status"`
	Message   string     `json:"message"`
	CreatedAt time.Time  `json:"created_at"`
}

type TaskStatusResponse struct {
	Task    *Task  `json:"task"`
	Message string `json:"message,omitempty"`
}

type ListTasksResponse struct {
	Tasks    []*Task `json:"tasks"`
	Total    int64   `json:"total"`
	Page     int     `json:"page"`
	PageSize int     `json:"page_size"`
}

type WebSocketMessage struct {
	Type      string      `json:"type"`
	Data      interface{} `json:"data"`
	Timestamp time.Time   `json:"timestamp"`
}

type User struct {
	ID            string    `json:"id" db:"id"`
	Username      string    `json:"username" db:"username"`
	Email         string    `json:"email" db:"email"`
	Password      string    `json:"-" db:"password"`
	APIKey        string    `json:"api_key,omitempty" db:"api_key"`
	Roles         []string  `json:"roles" db:"roles"`
	OAuthProvider string    `json:"oauth_provider,omitempty" db:"oauth_provider"`
	OAuthID       string    `json:"oauth_id,omitempty" db:"oauth_id"`
	CreatedAt     time.Time `json:"created_at" db:"created_at"`
}
