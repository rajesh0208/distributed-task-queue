// File: internal/grpc/task_server.go
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

// TaskServer implements TaskService gRPC server
type TaskServer struct {
	pb.UnimplementedTaskServiceServer
	storage storage.Storage
	broker  broker.Broker
}

// NewTaskServer creates a new task server
func NewTaskServer(storage storage.Storage, broker broker.Broker) *TaskServer {
	return &TaskServer{
		storage: storage,
		broker:  broker,
	}
}

// SubmitTask submits a new task
func (s *TaskServer) SubmitTask(ctx context.Context, req *pb.SubmitTaskRequest) (*pb.SubmitTaskResponse, error) {
	task := &models.Task{
		ID:         generateID(),
		Type:       models.TaskType(req.Type),
		Payload:    json.RawMessage(req.Payload),
		Status:     models.StatusQueued,
		Priority:   int(req.Priority),
		Retries:    0,
		MaxRetries: int(req.MaxRetries),
		CreatedAt:  now(),
	}

	if err := s.storage.CreateTask(ctx, task); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to create task: %v", err)
	}

	if err := s.broker.Publish(ctx, task); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to queue task: %v", err)
	}

	return &pb.SubmitTaskResponse{
		TaskId:    task.ID,
		Status:    string(task.Status),
		Message:   "Task queued successfully",
		CreatedAt: task.CreatedAt.Unix(),
	}, nil
}

// GetTask retrieves a task by ID
func (s *TaskServer) GetTask(ctx context.Context, req *pb.GetTaskRequest) (*pb.GetTaskResponse, error) {
	task, err := s.storage.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "task not found: %v", err)
	}

	pbTask := taskToProto(task)
	return &pb.GetTaskResponse{
		Task:    pbTask,
		Message: "Task retrieved successfully",
	}, nil
}

// ListTasks lists tasks with filters
func (s *TaskServer) ListTasks(ctx context.Context, req *pb.ListTasksRequest) (*pb.ListTasksResponse, error) {
	page := int(req.Page)
	if page < 1 {
		page = 1
	}
	pageSize := int(req.PageSize)
	if pageSize < 1 || pageSize > 100 {
		pageSize = 20
	}

	tasks, total, err := s.storage.ListTasks(ctx, models.TaskStatus(req.Status), "", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, status.Errorf(codes.Internal, "failed to list tasks: %v", err)
	}

	pbTasks := make([]*pb.Task, len(tasks))
	for i, task := range tasks {
		pbTasks[i] = taskToProto(task)
	}

	return &pb.ListTasksResponse{
		Tasks:    pbTasks,
		Total:    int32(total),
		Page:     int32(page),
		PageSize: int32(pageSize),
	}, nil
}

// DeleteTask deletes a task
func (s *TaskServer) DeleteTask(ctx context.Context, req *pb.DeleteTaskRequest) (*pb.DeleteTaskResponse, error) {
	if err := s.storage.DeleteTask(ctx, req.TaskId); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to delete task: %v", err)
	}

	return &pb.DeleteTaskResponse{
		Message: "Task deleted successfully",
	}, nil
}

// CancelTask cancels a running task
func (s *TaskServer) CancelTask(ctx context.Context, req *pb.CancelTaskRequest) (*pb.CancelTaskResponse, error) {
	task, err := s.storage.GetTask(ctx, req.TaskId)
	if err != nil {
		return nil, status.Errorf(codes.NotFound, "task not found: %v", err)
	}

	if task.Status == models.StatusCompleted || task.Status == models.StatusFailed {
		return nil, status.Errorf(codes.FailedPrecondition, "cannot cancel task in status: %s", task.Status)
	}

	task.Status = models.StatusCancelled
	if err := s.storage.UpdateTask(ctx, task); err != nil {
		return nil, status.Errorf(codes.Internal, "failed to cancel task: %v", err)
	}

	return &pb.CancelTaskResponse{
		Message: "Task cancelled successfully",
	}, nil
}

// Helper functions
func taskToProto(task *models.Task) *pb.Task {
	pbTask := &pb.Task{
		Id:        task.ID,
		Type:      string(task.Type),
		Payload:   []byte(task.Payload),
		Status:    string(task.Status),
		Priority:  int32(task.Priority),
		Retries:   int32(task.Retries),
		MaxRetries: int32(task.MaxRetries),
		CreatedAt: task.CreatedAt.Unix(),
	}

	if task.StartedAt != nil {
		pbTask.StartedAt = task.StartedAt.Unix()
	}
	if task.CompletedAt != nil {
		pbTask.CompletedAt = task.CompletedAt.Unix()
	}
	if task.WorkerID != "" {
		pbTask.WorkerId = task.WorkerID
	}
	if task.ProcessingTime > 0 {
		pbTask.ProcessingTime = task.ProcessingTime
	}
	if task.Error != "" {
		pbTask.Error = task.Error
	}

	return pbTask
}

func generateID() string {
	return uuid.New().String()
}

func now() time.Time {
	return time.Now()
}
