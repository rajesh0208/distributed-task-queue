//go:build !grpc

// Package grpc provides gRPC server implementations.
// Build with -tags grpc to enable full gRPC support.
package grpc

import (
	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/storage"
)

// TaskServer is a stub type present in non-grpc builds so the package
// always exports the symbol. The grpc-server binary is itself build-tagged
// so this code path is never actually reachable at runtime.
type TaskServer struct{}

// NewTaskServer is a stub — only the grpc-tagged build provides a real implementation.
func NewTaskServer(_ storage.Storage, _ broker.Broker) *TaskServer {
	panic("grpc support not compiled in; rebuild with -tags grpc")
}
