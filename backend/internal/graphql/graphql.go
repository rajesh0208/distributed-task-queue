// File: internal/graphql/server.go  (stub — compiled when the "graphql" build tag is NOT set)
//
// # Why a stub?
//
// The full GraphQL implementation (graphql_full.go) depends on
// github.com/graphql-go/graphql and github.com/graphql-go/handler, which are
// sizeable dependencies that most teams won't need. Go's build-tag system lets
// us ship a single binary that either includes or excludes the feature at
// compile time without any runtime `if` checks:
//
//   go build                    → compiles THIS file (returns 501 for /graphql)
//   go build -tags graphql      → compiles graphql_full.go instead
//
// Both files declare the same exported symbols (NewServer, PlaygroundHandler)
// so cmd/api/main.go always compiles regardless of which tag is active —
// the linker just picks the right implementation.
//
// # Enabling GraphQL
//
//  1. Install deps:  go get github.com/graphql-go/graphql github.com/graphql-go/handler
//  2. Rebuild:       go build -tags graphql ./...
//  3. Browse:        http://localhost:8080/playground  (GraphQL Playground UI)
//
// # What the stub returns
//
// Both handlers write HTTP 501 Not Implemented with a JSON error body that
// tells the caller exactly what flag to set. This is intentional: a 501 is
// easier to notice than silent zeroes or a panic, and the error message is
// self-documenting.
//
//go:build !graphql
// +build !graphql

package graphql

import (
	"net/http"

	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/storage"
)

// NewServer creates a new GraphQL server (stub implementation)
func NewServer(b broker.Broker, s storage.Storage) (http.Handler, error) {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte(`{"error": "GraphQL not implemented - build with -tags graphql"}`))
	}), nil
}

// PlaygroundHandler returns a GraphQL playground handler
func PlaygroundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotImplemented)
		w.Write([]byte(`{"error": "Playground not implemented"}`))
	}
}
