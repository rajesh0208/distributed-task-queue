// File: internal/graphql/resolvers.go
//
// GraphQL type definitions for the distributed task queue schema.
//
// This file declares the *graphql.Object variables that make up the SDL-free,
// programmatic schema used by graphql-go. Each exported variable corresponds
// to a named GraphQL type and must be registered in the schema built by
// resolver.go:GetSchema().
//
// # NonNull vs nullable fields
//
// graphql.NewNonNull(T) maps to `T!` in GraphQL SDL — the resolver MUST return
// a non-nil value or the entire parent object is nulled out with an error.
// Fields without NonNull are optional (`T`) — a nil/zero value becomes JSON
// `null`, which is valid.
//
// Rule of thumb used here:
//   - Core identity fields (id, type, status) → NonNull (always present)
//   - Lifecycle timestamps (startedAt, completedAt) → nullable (not set until
//     a worker picks up / finishes the task)
//   - Error and workerId → nullable (only set on failure / during processing)
//
// # Types defined in this file
//
//   taskType         — a single queued/running/completed task
//   workerType       — a registered worker process and its live counters
//   taskTypeCountType — {type, count} pair for the metrics breakdown
//   metricsType      — system-wide aggregate metrics
//   taskListType     — paginated task response (tasks + total + page + pageSize)
//   taskResponseType — returned by the submitTask mutation
//   deleteResponseType — returned by the deleteTask mutation
//   cancelResponseType — returned by the cancelTask mutation
//
// # Relationship to REST API models
//
// These GraphQL types mirror the JSON fields returned by the REST endpoints
// but use camelCase naming (GraphQL convention) rather than the snake_case used
// by the Go models. The resolver layer (resolver.go) performs the mapping when
// it builds response objects from *models.Task.
//
// +build graphql

package graphql

import (
	"github.com/graphql-go/graphql"
)

// Define GraphQL types
var taskType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Task",
	Fields: graphql.Fields{
		"id":            &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
		"type":          &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"payload":      &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"status":       &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"priority":     &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"retries":      &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"maxRetries":   &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"createdAt":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"startedAt":    &graphql.Field{Type: graphql.String},
		"completedAt":  &graphql.Field{Type: graphql.String},
		"workerId":     &graphql.Field{Type: graphql.String},
		"processingTime": &graphql.Field{Type: graphql.Int},
		"error":        &graphql.Field{Type: graphql.String},
	},
})

var workerType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Worker",
	Fields: graphql.Fields{
		"workerId":         &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
		"status":           &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"tasksProcessed":   &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"tasksFailed":      &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"lastHeartbeat":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"startTime":        &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"currentTask":      &graphql.Field{Type: graphql.String},
		"activeGoroutines": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
	},
})

var taskTypeCountType = graphql.NewObject(graphql.ObjectConfig{
	Name: "TaskTypeCount",
	Fields: graphql.Fields{
		"type":  &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"count": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
	},
})

var metricsType = graphql.NewObject(graphql.ObjectConfig{
	Name: "Metrics",
	Fields: graphql.Fields{
		"totalTasks":      &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"queuedTasks":     &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"processingTasks": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"completedTasks":  &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"failedTasks":     &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"activeWorkers":   &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"taskTypeCounts":  &graphql.Field{Type: graphql.NewList(taskTypeCountType)},
	},
})

var taskListType = graphql.NewObject(graphql.ObjectConfig{
	Name: "TaskList",
	Fields: graphql.Fields{
		"tasks":    &graphql.Field{Type: graphql.NewList(taskType)},
		"total":    &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"page":     &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
		"pageSize": &graphql.Field{Type: graphql.NewNonNull(graphql.Int)},
	},
})

var taskResponseType = graphql.NewObject(graphql.ObjectConfig{
	Name: "TaskResponse",
	Fields: graphql.Fields{
		"taskId":    &graphql.Field{Type: graphql.NewNonNull(graphql.ID)},
		"status":    &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"message":   &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
		"createdAt": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
	},
})

var deleteResponseType = graphql.NewObject(graphql.ObjectConfig{
	Name: "DeleteResponse",
	Fields: graphql.Fields{
		"message": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
	},
})

var cancelResponseType = graphql.NewObject(graphql.ObjectConfig{
	Name: "CancelResponse",
	Fields: graphql.Fields{
		"message": &graphql.Field{Type: graphql.NewNonNull(graphql.String)},
	},
})

