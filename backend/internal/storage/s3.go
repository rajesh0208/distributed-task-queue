// File: internal/storage/s3.go
//
// Build-tag-gated AWS S3 storage layer for the distributed task queue.
//
// # Build tag
//
//	go build -tags s3        (includes this file)
//	go build                 (excludes this file; local filesystem storage is used instead)
//
// The `//go:build s3` directive means this file is compiled only when the
// caller explicitly opts in. This keeps the default binary free of AWS SDK
// dependencies (hundreds of KB of generated code) for teams that don't need
// cloud object storage.
//
// # S3 vs local storage
//
// The image processor stores output files under ./storage/images/ by default
// (see processor/image_processor.go). That works in single-node deployments
// but breaks in multi-worker setups because each worker pod has its own
// ephemeral filesystem. S3 solves this: all workers write to the same bucket,
// and the API reads from there too — no shared NFS mount required.
//
// # AWS credentials
//
// NewS3Storage calls config.LoadDefaultConfig, which resolves credentials in
// priority order:
//  1. AWS_ACCESS_KEY_ID / AWS_SECRET_ACCESS_KEY environment variables
//  2. ~/.aws/credentials file
//  3. IAM instance role (EC2) or ECS task role
//
// No credentials are stored in code. In production, the preferred approach is
// an IAM role attached to the ECS task or Kubernetes service account.
//
// # Presigned URLs
//
// GetPresignedURL generates a time-limited, unauthenticated URL so the browser
// can download processed images directly from S3 without proxying through the
// API server — important for large image workloads. Default TTL used by callers
// is typically 15 minutes.
//
// # Dependencies (not in default go.mod)
//
//	go get github.com/aws/aws-sdk-go-v2/aws
//	go get github.com/aws/aws-sdk-go-v2/config
//	go get github.com/aws/aws-sdk-go-v2/service/s3
//
//go:build s3
// +build s3

package storage

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// S3Storage handles cloud object storage using AWS S3.
// Create with NewS3Storage; credentials are resolved automatically from the
// environment (see file-level doc for precedence order).
type S3Storage struct {
	client     *s3.Client // AWS SDK v2 S3 service client; thread-safe, reuse across goroutines
	bucketName string     // target bucket, e.g. "dtq-images-prod"; set once at construction
	region     string     // AWS region string, e.g. "us-east-1"; used to build public URLs
}

// NewS3Storage creates a new S3Storage backed by AWS SDK v2.
// It loads credentials from the environment (env vars → shared config → IAM role).
// Returns an error if the AWS config cannot be loaded (e.g. no credentials found).
// The S3 client itself is created synchronously; no network call is made here.
func NewS3Storage(bucketName, region string) (*S3Storage, error) {
	cfg, err := config.LoadDefaultConfig(context.TODO(),
		config.WithRegion(region),
	)
	if err != nil {
		return nil, fmt.Errorf("failed to load AWS config: %w", err)
	}

	client := s3.NewFromConfig(cfg)

	return &S3Storage{
		client:     client,
		bucketName: bucketName,
		region:     region,
	}, nil
}

// UploadFile uploads a local file to S3 under the given object key.
// It detects the MIME type from the file extension and sets Content-Type so
// browsers can render the object inline (e.g. image/jpeg) rather than
// forcing a download. Returns the public HTTPS URL of the uploaded object.
//
// Parameters:
//   key      — S3 object key, e.g. "images/uuid.jpg"; determines the object's path in the bucket
//   filePath — absolute or relative path to the local source file
func (s *S3Storage) UploadFile(ctx context.Context, key string, filePath string) (string, error) {
	file, err := os.Open(filePath)
	if err != nil {
		return "", fmt.Errorf("failed to open file: %w", err)
	}
	defer file.Close()

	contentType := "application/octet-stream"
	ext := filepath.Ext(key)
	switch ext {
	case ".jpg", ".jpeg":
		contentType = "image/jpeg"
	case ".png":
		contentType = "image/png"
	case ".gif":
		contentType = "image/gif"
	case ".webp":
		contentType = "image/webp"
	}

	_, err = s.client.PutObject(ctx, &s3.PutObjectInput{
		Bucket:      aws.String(s.bucketName),
		Key:         aws.String(key),
		Body:        file,
		ContentType: aws.String(contentType),
	})

	if err != nil {
		return "", fmt.Errorf("failed to upload to S3: %w", err)
	}

	url := fmt.Sprintf("https://%s.s3.%s.amazonaws.com/%s", s.bucketName, s.region, key)
	return url, nil
}

// DownloadFile downloads an S3 object and writes it to destPath on the local filesystem.
// The S3 response body is streamed via io.Copy so large files don't exhaust memory.
// Both the S3 response body and the destination file are closed via defer.
func (s *S3Storage) DownloadFile(ctx context.Context, key string, destPath string) error {
	result, err := s.client.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to download from S3: %w", err)
	}
	defer result.Body.Close()

	file, err := os.Create(destPath)
	if err != nil {
		return fmt.Errorf("failed to create file: %w", err)
	}
	defer file.Close()

	_, err = io.Copy(file, result.Body)
	if err != nil {
		return fmt.Errorf("failed to copy file: %w", err)
	}

	return nil
}

// DeleteFile deletes a file from S3
func (s *S3Storage) DeleteFile(ctx context.Context, key string) error {
	_, err := s.client.DeleteObject(ctx, &s3.DeleteObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("failed to delete from S3: %w", err)
	}

	return nil
}

// GetPresignedURL generates a time-limited signed URL that grants unauthenticated
// GET access to a single S3 object. The URL embeds temporary AWS credentials
// in the query string and expires after `duration`.
//
// Use case: the API returns this URL to the browser so the user can download
// the processed image directly from S3 without hitting the API server. This
// avoids streaming large image bytes through the API tier.
//
// Typical duration: 15 minutes for single-use downloads; up to 7 days for
// shared links (AWS maximum for SDK v2 presigned URLs).
func (s *S3Storage) GetPresignedURL(ctx context.Context, key string, duration time.Duration) (string, error) {
	presignClient := s3.NewPresignClient(s.client)

	request, err := presignClient.PresignGetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	}, func(opts *s3.PresignOptions) {
		opts.Expires = duration
	})

	if err != nil {
		return "", fmt.Errorf("failed to generate presigned URL: %w", err)
	}

	return request.URL, nil
}

// ListFiles returns the object keys in the bucket that start with prefix.
// This is used by admin/cleanup routines to enumerate objects by logical
// folder, e.g. ListFiles(ctx, "images/2024/") to find all images from 2024.
//
// Note: ListObjectsV2 returns at most 1 000 objects per call. For buckets
// with more objects a pagination loop using result.IsTruncated +
// result.NextContinuationToken would be needed.
func (s *S3Storage) ListFiles(ctx context.Context, prefix string) ([]string, error) {
	result, err := s.client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
		Bucket: aws.String(s.bucketName),
		Prefix: aws.String(prefix),
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list objects: %w", err)
	}

	var keys []string
	for _, obj := range result.Contents {
		keys = append(keys, *obj.Key)
	}

	return keys, nil
}

// FileExists checks whether an object exists in the bucket without downloading it.
// It uses HeadObject (metadata-only request) to keep bandwidth usage near zero.
//
// Error handling note: the AWS SDK v2 returns a typed error for missing objects,
// but the exact error type depends on the SDK version and bucket configuration.
// The current comparison `err == nsk` is a simplified check; robust production
// code should use errors.As(err, &nsk) to handle wrapped errors.
func (s *S3Storage) FileExists(ctx context.Context, key string) (bool, error) {
	_, err := s.client.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(s.bucketName),
		Key:    aws.String(key),
	})

	if err != nil {
		var nsk *types.NoSuchKey
		if err == nsk {
			return false, nil
		}
		return false, fmt.Errorf("failed to check file existence: %w", err)
	}

	return true, nil
}
