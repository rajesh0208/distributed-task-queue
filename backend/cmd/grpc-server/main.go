// Package main is the entry point for the optional gRPC API server.
//
// This binary is compiled ONLY when the "grpc" build tag is supplied:
//
//	go build -tags grpc ./cmd/grpc-server
//
// Why a separate binary with a build tag?
//   gRPC introduces protobuf generated code and the google.golang.org/grpc module.
//   These add non-trivial compile-time and binary-size overhead. Most deployments
//   only need the REST API (cmd/api). By gating this file behind a build tag, the
//   default `go build ./...` produces a lean binary without gRPC dependencies.
//   Teams that want gRPC (e.g. internal service-to-service calls) opt in explicitly.
//
// What this server exposes:
//   The gRPC service is defined in proto/task.proto and compiled to proto/task.pb.go.
//   It exposes the same task operations as the REST API (SubmitTask, GetTask,
//   ListTasks, CancelTask) but over gRPC with protobuf binary encoding instead of
//   JSON. This is useful for internal microservice consumers that prefer lower
//   serialization overhead and strict schema enforcement via .proto contracts.
//
// gRPC reflection:
//   reflection.Register enables server reflection so tools like grpcurl and Postman
//   can introspect available services and RPC methods without a .proto file.
//   Should be DISABLED in production if the schema is not public.
//
// Graceful shutdown:
//   s.GracefulStop() waits for all in-progress RPCs to complete before stopping
//   the listener. This is the gRPC equivalent of Fiber's ShutdownWithTimeout.
//
// It connects to:
//   - internal/grpc    — TaskServer implementation (SubmitTask, GetTask, etc.)
//   - internal/broker  — Redis Streams for publishing tasks
//   - internal/storage — PostgreSQL + Redis for task persistence
//   - proto/           — generated protobuf bindings
//
// Startup sequence:
//   main() → getEnv → broker.NewRedisStreamBroker → storage.NewPostgresStorage
//          → grpc.NewServer → pb.RegisterTaskServiceServer → reflection.Register
//          → net.Listen → s.Serve (goroutine) → wait for signal → s.GracefulStop
//
//go:build grpc
// +build grpc

package main

import (
	"log"
	"net"
	"os"
	"os/signal"
	"syscall"

	"google.golang.org/grpc"
	"google.golang.org/grpc/reflection"

	"distributed-task-queue/internal/broker"
	grpcserver "distributed-task-queue/internal/grpc"
	"distributed-task-queue/internal/storage"
	pb "distributed-task-queue/proto"
)

// main is the gRPC server entry point.
// It reads configuration from environment variables, wires dependencies,
// and serves until a SIGTERM or SIGINT is received.
func main() {
	// Read all configuration from environment so the binary is 12-factor compliant.
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")                                                        // Redis host:port
	postgresDSN := getEnv("POSTGRES_DSN", "postgres://taskqueue_user:password@localhost/taskqueue?sslmode=disable") // Postgres connection string
	grpcPort := getEnv("GRPC_PORT", "50051")                                                                   // default gRPC port — distinct from the REST API's 8080

	// Connect to Redis Streams. Same stream key and consumer group as the REST API
	// so gRPC task submissions are processed by the same worker fleet.
	brokerInstance, err := broker.NewRedisStreamBroker(redisAddr, "task-queue", "workers")
	if err != nil {
		log.Fatalf("Failed to create broker: %v", err) // fatal: can't run without a message broker
	}
	defer brokerInstance.Close() // close the Redis connection pool on exit

	// Connect to PostgreSQL + Redis cache layer.
	storageInstance, err := storage.NewPostgresStorage(postgresDSN, redisAddr)
	if err != nil {
		log.Fatalf("Failed to create storage: %v", err) // fatal: can't run without persistent storage
	}
	defer storageInstance.Close() // close DB connection pool on exit

	// Create the bare gRPC server (no TLS — add grpc.Creds in production).
	s := grpc.NewServer()

	// Inject the storage and broker into the TaskServer implementation,
	// then register it under the generated protobuf service descriptor.
	taskServer := grpcserver.NewTaskServer(storageInstance, brokerInstance)
	pb.RegisterTaskServiceServer(s, taskServer) // wires the service into the gRPC multiplexer

	// Server reflection lets grpcurl/Postman discover available services at runtime.
	// Remove in production if the schema should not be publicly discoverable.
	reflection.Register(s)

	// Bind a TCP listener on the configured port before starting to serve.
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err) // port already in use or permission denied
	}

	log.Printf("gRPC server listening on :%s", grpcPort)

	// Serve in a goroutine so the main goroutine can wait for a shutdown signal.
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err) // fatal: server crashed unexpectedly
		}
	}()

	// Block until SIGINT (Ctrl+C) or SIGTERM (Docker/k8s stop) is received.
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM) // buffered channel prevents signal loss during startup
	<-quit

	log.Println("Shutting down gRPC server...")
	s.GracefulStop() // wait for in-flight RPCs to complete, then close the listener
	log.Println("gRPC server stopped")
}

// getEnv reads an environment variable and falls back to a default.
// Called for every configuration value so no hard-coded values are used in production.
func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" { // os.Getenv returns "" for unset vars
		return value
	}
	return defaultValue // safe default for local development
}
