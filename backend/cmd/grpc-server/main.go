// File: cmd/grpc-server/main.go
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

func main() {
	// Get configuration from environment
	redisAddr := getEnv("REDIS_ADDR", "localhost:6379")
	postgresDSN := getEnv("POSTGRES_DSN", "postgres://taskqueue_user:password@localhost/taskqueue?sslmode=disable")
	grpcPort := getEnv("GRPC_PORT", "50051")

	// Initialize broker
	brokerInstance, err := broker.NewRedisStreamBroker(redisAddr, "task-queue", "workers")
	if err != nil {
		log.Fatalf("Failed to create broker: %v", err)
	}
	defer brokerInstance.Close()

	// Initialize storage
	storageInstance, err := storage.NewPostgresStorage(postgresDSN, redisAddr)
	if err != nil {
		log.Fatalf("Failed to create storage: %v", err)
	}
	defer storageInstance.Close()

	// Create gRPC server
	s := grpc.NewServer()

	// Register services
	taskServer := grpcserver.NewTaskServer(storageInstance, brokerInstance)
	pb.RegisterTaskServiceServer(s, taskServer)

	// Enable reflection for testing
	reflection.Register(s)

	// Start server
	lis, err := net.Listen("tcp", ":"+grpcPort)
	if err != nil {
		log.Fatalf("Failed to listen: %v", err)
	}

	log.Printf("gRPC server listening on :%s", grpcPort)

	// Graceful shutdown
	go func() {
		if err := s.Serve(lis); err != nil {
			log.Fatalf("Failed to serve: %v", err)
		}
	}()

	// Wait for interrupt signal
	quit := make(chan os.Signal, 1)
	signal.Notify(quit, syscall.SIGINT, syscall.SIGTERM)
	<-quit

	log.Println("Shutting down gRPC server...")
	s.GracefulStop()
	log.Println("gRPC server stopped")
}

func getEnv(key, defaultValue string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return defaultValue
}
