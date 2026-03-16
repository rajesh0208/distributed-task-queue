// File: internal/grpc/task_server.go
//
// Package grpc implements the gRPC server that exposes task management operations
// to other internal microservices using the Protocol Buffers (protobuf) binary format.
//
// What is gRPC, and why is it different from REST?
// REST communicates via HTTP/1.1 with JSON text bodies — human-readable but verbose.
// gRPC communicates via HTTP/2 with Protocol Buffer binary-encoded messages — compact,
// fast, and strongly typed.  Key differences:
//   - Contract-first: the proto file (proto/task.proto) is the authoritative API contract;
//     Go structs and server/client stubs are generated from it, so drift is impossible.
//   - Binary encoding: protobuf fields are encoded with their field number, not their name,
//     so "status" doesn't appear as the string "status" on the wire — just a small integer tag.
//     This makes messages 3–10x smaller than equivalent JSON.
//   - Multiplexing: HTTP/2 allows multiple concurrent RPC calls over a single TCP connection,
//     eliminating the head-of-line blocking that HTTP/1.1 suffers from.
//   - Streaming: gRPC supports server-streaming (one request, many responses), client-streaming,
//     and bidirectional streaming RPCs — REST has no equivalent without SSE/WebSocket workarounds.
//
// Why gRPC for internal service communication?
// When two services are both under our control and performance matters, gRPC wins over REST:
//   - Generated client stubs mean no hand-written HTTP request boilerplate.
//   - Strong typing: a field type mismatch is a compile error, not a runtime panic.
//   - Lower latency: binary encoding + HTTP/2 vs text encoding + HTTP/1.1.
//   - Deadline propagation: gRPC contexts carry deadlines across the network automatically.
//
// How the proto definition maps to Go structs:
// The proto file defines message Task { string id = 1; string type = 2; ... }.
// protoc-gen-go generates pb.Task with Id, Type, etc. as exported Go fields.
// taskToProto() converts the internal models.Task (which has snake_case DB tags) to pb.Task.
// The protoc-generated RegisterTaskServiceServer wires TaskServer to the gRPC server object.
//
// Streaming vs unary:
// All methods here are unary RPCs (one request → one response).  A streaming RPC (e.g.
// StreamTaskUpdates) would use a pb.TaskService_StreamTaskUpdatesServer and call Send()
// in a loop.  Streaming is not implemented here — the WebSocket hub in cmd/api serves
// real-time updates to browser clients instead.
//
// Build tag:
// This file is compiled only when the "grpc" build tag is set:
//
//	go build -tags grpc ./...
//
// Without the tag the gRPC server is excluded from the binary.
//
// Connected to:
//   - storage.Storage      — persists and retrieves tasks from PostgreSQL
//   - broker.Broker        — publishes tasks to the Redis Stream after creation
//   - proto/task.proto     — the protobuf contract that defines all request/response types
//   - pb (distributed-task-queue/proto) — the generated Go package from the proto file
//
// Called by:
//   - cmd/grpc-server/main.go — creates a TaskServer and registers it with grpc.NewServer()
//
//go:build grpc
// +build grpc

package grpc

import (
	"context"
	"encoding/json"
	"time"

	"github.com/google/uuid"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/storage"
	pb "distributed-task-queue/proto"
)

// TaskServer is the concrete gRPC server implementation for the TaskService RPC service
// defined in proto/task.proto.
//
// Embedding pb.UnimplementedTaskServiceServer is required by the grpc-go framework:
// it provides default "not implemented" responses for any RPC methods we haven't overridden,
// and it also makes the struct forward-compatible — if new RPC methods are added to the
// proto in the future, existing code won't break at compile time.
//
// Fields:
//   - pb.UnimplementedTaskServiceServer: embedded to satisfy the TaskServiceServer interface
//     for any methods not explicitly implemented here.
//   - storage: the data layer — stores and retrieves tasks from PostgreSQL (with Redis cache).
//   - broker:  the message queue — publishes tasks to the Redis Stream after creation so
//     a worker picks them up.
type TaskServer struct {
	pb.UnimplementedTaskServiceServer  // satisfies interface for unimplemented RPC methods
	storage storage.Storage            // reads/writes tasks to PostgreSQL + Redis
	broker  broker.Broker              // publishes new tasks to the Redis Stream
}

// NewTaskServer constructs a TaskServer and injects the storage and broker dependencies.
//
// Using dependency injection (rather than creating them inside the constructor) allows
// the gRPC server to share the same storage and broker instances as the HTTP API,
// avoiding duplicate connection pools.
//
// Parameters:
//   - storage: initialized storage.Storage (PostgreSQL + Redis)
//   - broker:  initialized broker.Broker (Redis Streams)
//
// Returns: *TaskServer, ready to be registered with grpc.NewServer() via
// pb.RegisterTaskServiceServer(s, taskServer).
//
// Called by: cmd/grpc-server/main.go.
func NewTaskServer(storage storage.Storage, broker broker.Broker) *TaskServer {
	return &TaskServer{
		storage: storage,
		broker:  broker,
	}
}

// SubmitTask implements the SubmitTask unary RPC.
//
// This is the gRPC equivalent of POST /api/v1/tasks in the HTTP API.
// It creates a new task record in PostgreSQL and publishes it to the Redis Stream
// so a worker processes it asynchronously.
//
// Why persist before publishing?
// Creating the DB record first means a failed stream publish leaves the task in a
// recoverable state.  If we published first and the DB write failed, the worker
// would try to look up a task that doesn't exist.
//
// How protobuf maps to Go here:
//   - req.Type     is a string field, field number 1 in the proto — maps to models.TaskType
//   - req.Payload  is a bytes field, field number 2 — stored as json.RawMessage
//   - req.Priority is an int32 field, field number 3 — cast to int for the model
//
// Parameters:
//   - ctx: carries deadline and cancellation from the calling service
//   - req: *pb.SubmitTaskRequest — the protobuf-decoded request message
//
// Returns:
//   - *pb.SubmitTaskResponse: task ID, initial status, and Unix timestamp
//   - error: a gRPC status error (codes.Internal) if storage or broker fails
func (s *TaskServer) SubmitTask(ctx context.Context, req *pb.SubmitTaskRequest) (*pb.SubmitTaskResponse, error) {
	// Build the internal task model from the protobuf request fields.
	// generateID() uses UUID v4 (random) — the same strategy as the HTTP API.
	task := &models.Task{
		ID:         generateID(),                // globally unique UUID
		Type:       models.TaskType(req.Type),   // convert proto string to typed alias
		Payload:    json.RawMessage(req.Payload), // store raw JSON bytes for the worker to interpret
		Status:     models.StatusQueued,          // all new tasks start as "queued"
		Priority:   int(req.Priority),            // int32 → int; higher priority = processed first
		Retries:    0,                            // no retries yet
		MaxRetries: int(req.MaxRetries),          // retry cap before permanent failure
		CreatedAt:  now(),                        // creation time used for display and sorting
	}

	// Persist to PostgreSQL.  codes.Internal signals the caller that this is a server-side
	// error (not a bad request from the caller), which is important for retry decisions.
	if err := s.storage.CreateTask(ctx, task); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create task: %v", err)
	}

	// Publish to the Redis Stream.  Workers subscribe to this stream via XREADGROUP;
	// the message contains the task JSON and trace context for distributed tracing.
	if err := s.broker.Publish(ctx, task); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to queue task: %v", err)
	}

	// Return the task ID and Unix timestamp so the caller can poll for status.
	// Unix timestamp (int64 seconds since epoch) is used instead of a string because
	// protobuf doesn't have a native timestamp scalar — google.protobuf.Timestamp is
	// the idiomatic choice for production, but int64 is simpler for this service.
	return &pb.SubmitTaskResponse{
		TaskId:    task.ID,
		Status:    string(task.Status),    // always "queued" at this point
		Message:   "Task queued successfully",
		CreatedAt: task.CreatedAt.Unix(),  // seconds since Unix epoch
	}, nil
}

// GetTask implements the GetTask unary RPC.
//
// Fetches a single task by its UUID from storage and returns it as a protobuf Task message.
// The taskToProto helper converts the internal model to the wire format.
//
// Parameters:
//   - ctx:         carries deadline and cancellation
//   - req.TaskId:  the UUID string of the task to retrieve
//
// Returns:
//   - *pb.GetTaskResponse: contains the Task protobuf message and a confirmation message
//   - error: codes.NotFound if the task doesn't exist; codes.Internal for DB errors
func (s *TaskServer) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	task, err := s.storage.GetTask(ctx, req.TaskId)
	if err != nil {
		// codes.NotFound tells the caller "this task ID doesn't exist",
		// which is a different error class from a DB outage (codes.Internal).
		return nil, status.Errorf(codes.NotFound, "task not found: %v", err)
	}

	pbTask := taskToProto(task) // convert internal model to protobuf wire format
	return &pb.GetTaskResponse{
		Task:    pbTask,
		Message: "Task retrieved successfully",
	}, nil
}

// ListTasks implements the ListTasks unary RPC.
//
// Returns a paginated list of tasks, optionally filtered by status.
// Page and PageSize are validated and clamped to sane defaults to prevent
// excessively large queries or invalid SQL OFFSET values.
//
// Parameters:
//   - ctx:          carries deadline and cancellation
//   - req.Status:   optional filter string; empty string means all statuses
//   - req.Page:     1-indexed page number; clamped to minimum 1
//   - req.PageSize: results per page; clamped to 1–100
//
// Returns:
//   - *pb.ListTasksResponse: paginated tasks, total count, page, and page size
//   - error: codes.Internal if the storage query fails
func (s *TaskServer) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	// Clamp page to at least 1 to avoid negative SQL OFFSET.
	page := int(req.Page)
	if page < 1 {
		page = 1
	}
	// Clamp pageSize to a valid range: minimum 1, maximum 100.
	// Values outside this range are reset to the default 20.
	pageSize := int(req.PageSize)
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	// ListTasks computes LIMIT=pageSize, OFFSET=(page-1)*pageSize internally.
	// Empty string for userID means "all users" — appropriate for service-to-service calls.
	tasks, total, err := s.storage.ListTasks(ctx, models.TaskStatus(req.Status), "", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tasks: %v", err)
	}

	// Convert each internal task to its protobuf representation.
	// Pre-allocating with len(tasks) avoids multiple reallocations as the slice grows.
	pbTasks := make([]*pb.Task, len(tasks))
	for i, task := range tasks {
		pbTasks[i] = taskToProto(task)
	}

	// int32 is used in the proto for Total, Page, PageSize because int32 is the
	// default integer type in proto3 and is sufficient for pagination values.
	return &pb.ListTasksResponse{
		Tasks:    pbTasks,
		Total:    int32(total),    // total matching tasks across all pages
		Page:     int32(page),     // current page (echoed back)
		PageSize: int32(pageSize), // effective page size (echoed back)
	}, nil
}

// DeleteTask implements the DeleteTask unary RPC.
//
// Permanently removes the task record from the database.
// No ownership check is performed here — service-to-service callers are trusted.
//
// Parameters:
//   - ctx:         carries deadline and cancellation
//   - req.TaskId:  UUID of the task to delete
//
// Returns:
//   - *pb.DeleteTaskResponse: a confirmation message
//   - error: codes.Internal if the storage delete fails
func (s *TaskServer) DeleteTask(ctx context.Context, req *pb.DeleteTaskRequest) (*pb.DeleteTaskResponse, error) {
	if err := s.storage.DeleteTask(ctx, req.TaskId); err != nil {
		// Storage may return an error if the task doesn't exist or if the DB is unavailable.
		// Both cases are reported as Internal to the caller.
		return nil, status.Errorf(codes.Internal, "failed to delete task: %v", err)
	}

	return &pb.DeleteTaskResponse{
		Message: "Task deleted successfully",
	}, nil
}

// CancelTask implements the CancelTask unary RPC.
//
// Marks the task status as "cancelled" in the database.  Tasks that are already
// in a terminal state (completed or failed) cannot be cancelled — attempting to do
// so returns codes.FailedPrecondition, which tells the caller "the operation is
// valid in principle but impossible given the current state".
//
// Note: this method does not set the Redis cancellation signal key that the worker
// checks (see worker/main.go isCancelled).  For that signal to be set, the HTTP
// API's cancelTask handler must also be called.  Service-to-service callers that
// invoke this RPC directly should be aware of this limitation.
//
// Parameters:
//   - ctx:         carries deadline and cancellation
//   - req.TaskId:  UUID of the task to cancel
//
// Returns:
//   - *pb.CancelTaskResponse: a confirmation message
//   - error: codes.NotFound if task doesn't exist; codes.FailedPrecondition if terminal;
//     codes.Internal if the DB update fails
func (s *TaskServer) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	task, err := s.storage.GetTask(ctx, req.TaskId)
	if err != nil {
		// codes.NotFound: task with this ID doesn't exist
		return nil, status.Errorf(codes.NotFound, "task not found: %v", err)
	}

	// Completed and failed tasks are terminal — they cannot be undone by cancellation.
	// codes.FailedPrecondition is the gRPC standard code for "operation invalid for current state".
	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot cancel task in status: %s", task.Status)
	}

	// Update status to cancelled and persist.  The worker reads this before and after
	// processing to respect the cancellation.
	task.Status = models.StatusCancelled
	if err := s.storage.UpdateTask(ctx, task); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cancel task: %v", err)
	}

	return &pb.CancelTaskResponse{
		Message: "Task cancelled successfully",
	}, nil
}

// taskToProto converts an internal models.Task struct to the protobuf pb.Task message.
//
// Why a separate conversion function?
// The internal model uses Go idioms (time.Time, *time.Time for optionals, string aliases
// for enums, json.RawMessage for payloads) while the proto message uses Unix timestamps
// (int64), byte slices, and plain strings.  Keeping the conversion in one place means
// any field mapping changes only need to be made here.
//
// Optional fields (StartedAt, CompletedAt, WorkerID, ProcessingTime, Error) are only
// set on the proto message when they have meaningful values; the proto default (0 / "")
// signals "not set" to the caller.
//
// Called by: GetTask and ListTasks.
func taskToProto(task *models.Task) *pb.Task {
	pbTask := &pb.Task{
		Id:         task.ID,                    // UUID string
		Type:       string(task.Type),           // typed alias → plain string
		Payload:    []byte(task.Payload),        // json.RawMessage → []byte (protobuf bytes field)
		Status:     string(task.Status),         // typed alias → plain string
		Priority:   int32(task.Priority),        // int → int32 (proto default integer type)
		Retries:    int32(task.Retries),          // int → int32
		MaxRetries: int32(task.MaxRetries),      // int → int32
		CreatedAt:  task.CreatedAt.Unix(),       // time.Time → Unix epoch seconds
	}

	// Only set StartedAt if the worker has actually claimed this task.
	if task.StartedAt != nil {
		pbTask.StartedAt = task.StartedAt.Unix()
	}
	// Only set CompletedAt if the task has finished (success or failure).
	if task.CompletedAt != nil {
		pbTask.CompletedAt = task.CompletedAt.Unix()
	}
	// Only set WorkerID if a worker is assigned to this task.
	if task.WorkerID != "" {
		pbTask.WorkerId = task.WorkerID
	}
	// Only set ProcessingTime if the task has been processed (milliseconds elapsed).
	if task.ProcessingTime > 0 {
		pbTask.ProcessingTime = task.ProcessingTime
	}
	// Only set Error if the task failed or was retried.
	if task.Error != "" {
		pbTask.Error = task.Error
	}

	return pbTask
}

// generateID returns a new UUID v4 string.
//
// UUID v4 is cryptographically random — field number 1 in the proto message.
// Using the same generation strategy as the HTTP API ensures task IDs are
// consistent regardless of which interface created the task.
//
// Called by: SubmitTask.
func generateID() string {
	return uuid.New().String()
}

// now returns the current local time.
//
// Wrapping time.Now() in a function makes it easy to mock in tests by swapping
// the function reference.  In production it always returns the real wall-clock time.
//
// Called by: SubmitTask.
func now() time.Time {
	return time.Now()
}
