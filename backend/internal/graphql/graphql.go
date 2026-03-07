// File: internal/graphql/server.go
//go:build !graphql
// +build !graphql

// Stub implementation when GraphQL is not enabled

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
