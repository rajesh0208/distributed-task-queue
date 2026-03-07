// File: internal/graphql/resolvers.go
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

