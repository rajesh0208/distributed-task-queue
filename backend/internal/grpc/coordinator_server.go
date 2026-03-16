// File: internal/grpc/coordinator_server.go
//
// gRPC CoordinatorService server — excluded from all builds until proto files
// are generated (see client.go for the full explanation of `// +build ignore`).
//
// # Role of the Coordinator
//
// The coordinator is the control-plane counterpart to the worker's data-plane:
//
//   Workers (WorkerServer)   — task lifecycle: pick up, heartbeat, complete
//   Coordinator (this file)  — observability: metrics aggregation, health
//
// In a multi-region deployment you might have multiple coordinator replicas;
// each reads from the shared PostgreSQL storage so their views are consistent.
//
// # GetMetrics
//
// Returns aggregate system metrics (queue depth, worker counts, throughput)
// pulled from storage.GetMetrics(). Currently returns Unimplemented because
// the proto response message (pb.MetricsResponse) hasn't been generated yet.
// When the proto is ready, replace the body with:
//
//   m, err := s.storage.GetMetrics(ctx)
//   // map m to pb.MetricsResponse and return
//
// # HealthCheck
//
// Unlike GetMetrics, HealthCheck has a real implementation: it pings the
// PostgreSQL connection via storage.GetDB().PingContext(ctx) and returns a
// map of component → "healthy"/"unhealthy". This works without a proto because
// the response is a plain map[string]interface{} (not a proto message).
//
// The overall status is "unhealthy" if ANY individual check fails, so callers
// (load balancers, k8s liveness probes) don't need to inspect the `checks` map.
//
// # TODO: add Redis health check
//
// The checks map currently only includes "database". A Redis ping should be
// added once the coordinator has access to the cache layer.
//
// +build ignore

package grpc

import (
	"context"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"distributed-task-queue/internal/storage"
	// Uncomment after generating proto files:
	// pb "distributed-task-queue/proto"
)

// CoordinatorServer implements CoordinatorService gRPC server
type CoordinatorServer struct {
	// pb.UnimplementedCoordinatorServiceServer
	storage storage.Storage
}

// NewCoordinatorServer creates a new coordinator server
func NewCoordinatorServer(storage storage.Storage) *CoordinatorServer {
	return &CoordinatorServer{
		storage: storage,
	}
}

// GetMetrics retrieves system metrics
func (s *CoordinatorServer) GetMetrics(ctx context.Context, req interface{}) (interface{}, error) {
	return nil, status.Errorf(codes.Unimplemented, "gRPC requires proto files to be generated first")
}

// HealthCheck performs health check
func (s *CoordinatorServer) HealthCheck(ctx context.Context, req interface{}) (interface{}, error) {
	// Perform health checks
	checks := make(map[string]string)
	
	// Check database
	if err := s.storage.GetDB().PingContext(ctx); err != nil {
		checks["database"] = "unhealthy"
	} else {
		checks["database"] = "healthy"
	}

	// Determine overall status
	status := "healthy"
	for _, check := range checks {
		if check != "healthy" {
			status = "unhealthy"
			break
		}
	}

	return map[string]interface{}{
		"status": status,
		"checks": checks,
	}, nil
}
