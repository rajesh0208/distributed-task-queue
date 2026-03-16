// File: internal/grpc/worker_server.go
//
// gRPC WorkerService server — excluded from all builds until proto files are
// generated (see client.go for the explanation of `// +build ignore`).
//
// # Role of WorkerServer
//
// The worker gRPC service provides a structured RPC surface for worker
// lifecycle management as an alternative to the current Redis Streams-based
// heartbeat mechanism (see cmd/worker/main.go). When enabled it allows:
//
//   RegisterWorker   — announce a new worker process to the coordinator
//   Heartbeat        — periodic liveness signal replacing Redis HSET heartbeats
//   GetWorkerStatus  — pull the current state of one worker by ID
//   ListWorkers      — enumerate all active workers (used by the metrics API)
//
// # Current state
//
// All four RPC methods return codes.Unimplemented because the proto-generated
// pb.UnimplementedWorkerServiceServer embed and the request/response message
// types don't exist yet. The struct and constructor ARE functional — they hold
// a real storage.Storage reference and will work once the methods are filled in.
//
// # workerToProto
//
// The private workerToProto helper is a placeholder for the mapping function
// that converts a *models.WorkerMetrics (the internal representation) to the
// protobuf wire type (pb.Worker). This mapping is intentionally left as a
// separate function rather than inline code so it can be tested independently.
//
// # Migration path
//
//  1. Define the WorkerService in proto/task_queue.proto
//  2. Run protoc to generate Go code
//  3. Change `// +build ignore` to `//go:build grpc`
//  4. Embed pb.UnimplementedWorkerServiceServer in WorkerServer
//  5. Implement each method body and workerToProto
//
// +build ignore

package grpc

import (
	"context"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/storage"
	// Uncomment after generating proto files:
	// pb "distributed-task-queue/proto"
)

// WorkerServer implements WorkerService gRPC server
type WorkerServer struct {
	// pb.UnimplementedWorkerServiceServer
	storage storage.Storage
}

// NewWorkerServer creates a new worker server
func NewWorkerServer(storage storage.Storage) *WorkerServer {
	return &WorkerServer{
		storage: storage,
	}
}

// RegisterWorker registers a new worker
func (s *WorkerServer) RegisterWorker(ctx context.Context, req interface{}) (interface{}, error) {
	return nil, status.Errorf(codes.Unimplemented, "gRPC requires proto files to be generated first")
}

// GetWorkerStatus retrieves worker status
func (s *WorkerServer) GetWorkerStatus(ctx context.Context, req interface{}) (interface{}, error) {
	return nil, status.Errorf(codes.Unimplemented, "gRPC requires proto files to be generated first")
}

// ListWorkers lists all workers
func (s *WorkerServer) ListWorkers(ctx context.Context, req interface{}) (interface{}, error) {
	return nil, status.Errorf(codes.Unimplemented, "gRPC requires proto files to be generated first")
}

// Heartbeat updates worker heartbeat
func (s *WorkerServer) Heartbeat(ctx context.Context, req interface{}) (interface{}, error) {
	return nil, status.Errorf(codes.Unimplemented, "gRPC requires proto files to be generated first")
}

func workerToProto(worker *models.WorkerMetrics) interface{} {
	// TODO: Implement after proto generation
	return nil
}
