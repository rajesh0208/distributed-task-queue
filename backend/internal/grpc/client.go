// File: internal/grpc/client.go
//
// gRPC client stub — excluded from all builds until proto files are generated.
//
// # Build tag: // +build ignore
//
// The `ignore` pseudo-tag is a Go convention for files that should NEVER be
// compiled by `go build` or `go test`. It is different from a real build tag
// like `grpc`: an `ignore` file cannot be enabled with `-tags`; it is
// permanently excluded. We use it here because the file imports a proto package
// (`pb "distributed-task-queue/proto"`) that doesn't exist yet, so including
// it would break compilation.
//
// This file serves as living documentation of the intended gRPC client API
// surface. When proto generation is complete:
//  1. Run: protoc --go_out=. --go-grpc_out=. proto/task_queue.proto
//  2. Change `// +build ignore` to `//go:build grpc` (or remove the file
//     and use the generated client directly).
//  3. Uncomment the pb import and method bodies.
//
// # Client architecture
//
// The Client struct wraps three gRPC service stubs generated from the proto:
//
//   TaskClient        — submit, get, list, cancel, retry tasks
//   WorkerClient      — register, heartbeat, get status, list workers
//   CoordinatorClient — system metrics and health checks
//
// All three share one underlying *grpc.ClientConn (TCP connection with
// multiplexed HTTP/2 streams), so there is no per-service overhead.
//
// # Transport security
//
// NewClient currently uses insecure.NewCredentials() (plain TCP, no TLS).
// For production, replace with credentials.NewTLS(tlsConfig) or
// credentials.NewClientTLSFromFile(certFile, "").
//
// # Timeouts
//
// Callers should pass a context with a deadline, e.g.:
//
//   ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
//   defer cancel()
//   resp, err := c.SubmitTask(ctx, ...)
//
// The `time` import is kept for future use in deadline helpers.
//
// +build ignore

package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"

	// Uncomment after generating proto files:
	// pb "distributed-task-queue/proto"
)

// Client wraps gRPC clients for all services
type Client struct {
	// TaskClient       pb.TaskServiceClient
	// WorkerClient     pb.WorkerServiceClient
	// CoordinatorClient pb.CoordinatorServiceClient
	conn             *grpc.ClientConn
}

// NewClient creates a new gRPC client
func NewClient(addr string) (*Client, error) {
	conn, err := grpc.Dial(addr, grpc.WithTransportCredentials(insecure.NewCredentials()))
	if err != nil {
		return nil, err
	}

	return &Client{
		// TaskClient:        pb.NewTaskServiceClient(conn),
		// WorkerClient:      pb.NewWorkerServiceClient(conn),
		// CoordinatorClient: pb.NewCoordinatorServiceClient(conn),
		conn:              conn,
	}, nil
}

// Close closes the client connection
func (c *Client) Close() error {
	return c.conn.Close()
}

// SubmitTask submits a task via gRPC
func (c *Client) SubmitTask(ctx context.Context, taskType string, payload []byte, priority, maxRetries int32) (interface{}, error) {
	// TODO: Implement after proto generation
	return nil, nil
}

// GetTask retrieves a task via gRPC
func (c *Client) GetTask(ctx context.Context, taskID string) (interface{}, error) {
	// TODO: Implement after proto generation
	return nil, nil
}

// ListTasks lists tasks via gRPC
func (c *Client) ListTasks(ctx context.Context, status string, page, pageSize int32) (interface{}, error) {
	// TODO: Implement after proto generation
	return nil, nil
}
