// File: internal/graphql/resolver.go
//go:build graphql
// +build graphql

package graphql

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/graphql-go/graphql"

	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/models"
	"distributed-task-queue/internal/storage"
)

// RootResolver contains all resolvers
type RootResolver struct {
	storage storage.Storage
	broker  broker.Broker
}

// NewRootResolver creates a new root resolver
func NewRootResolver(storage storage.Storage, broker broker.Broker) *RootResolver {
	return &RootResolver{
		storage: storage,
		broker:  broker,
	}
}

// GetSchema returns the GraphQL schema
func (r *RootResolver) GetSchema() (graphql.Schema, error) {
	return graphql.NewSchema(graphql.SchemaConfig{
		Query:    r.getQueryType(),
		Mutation: r.getMutationType(),
	})
}

func (r *RootResolver) getQueryType() *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name: "Query",
		Fields: graphql.Fields{
			"task": &graphql.Field{
				Type: taskType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
				},
				Resolve: r.resolveTask,
			},
			"tasks": &graphql.Field{
				Type: taskListType,
				Args: graphql.FieldConfigArgument{
					"status": &graphql.ArgumentConfig{
						Type: graphql.String,
					},
					"page": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
					"pageSize": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
				},
				Resolve: r.resolveTasks,
			},
			"workers": &graphql.Field{
				Type:    graphql.NewList(workerType),
				Resolve: r.resolveWorkers,
			},
			"metrics": &graphql.Field{
				Type:    metricsType,
				Resolve: r.resolveMetrics,
			},
		},
	})
}

func (r *RootResolver) getMutationType() *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation",
		Fields: graphql.Fields{
			"submitTask": &graphql.Field{
				Type: taskResponseType,
				Args: graphql.FieldConfigArgument{
					"type": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String),
					},
					"payload": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String),
					},
					"priority": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
					"maxRetries": &graphql.ArgumentConfig{
						Type: graphql.Int,
					},
				},
				Resolve: r.resolveSubmitTask,
			},
			"deleteTask": &graphql.Field{
				Type: deleteResponseType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
				},
				Resolve: r.resolveDeleteTask,
			},
			"cancelTask": &graphql.Field{
				Type: cancelResponseType,
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID),
					},
				},
				Resolve: r.resolveCancelTask,
			},
		},
	})
}

// Resolver functions
func (r *RootResolver) resolveTask(p graphql.ResolveParams) (interface{}, error) {
	id := p.Args["id"].(string)
	task, err := r.storage.GetTask(p.Context, id)
	if err != nil {
		return nil, err
	}
	return taskToMap(task), nil
}

func (r *RootResolver) resolveTasks(p graphql.ResolveParams) (interface{}, error) {
	status := ""
	if s, ok := p.Args["status"].(string); ok {
		status = s
	}

	page := 1
	if p, ok := p.Args["page"].(int); ok {
		page = p
	}

	pageSize := 20
	if ps, ok := p.Args["pageSize"].(int); ok {
		pageSize = ps
	}

	tasks, total, err := r.storage.ListTasks(p.Context, models.TaskStatus(status), "", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err
	}

	// Pre-allocate with exact capacity
	taskMaps := make([]interface{}, len(tasks))
	for i := range tasks {
		taskMaps[i] = taskToMap(tasks[i])
	}

	return map[string]interface{}{
		"tasks":    taskMaps,
		"total":    total,
		"page":     page,
		"pageSize": pageSize,
	}, nil
}

func (r *RootResolver) resolveWorkers(p graphql.ResolveParams) (interface{}, error) {
	// Implementation depends on storage methods
	return []interface{}{}, nil
}

func (r *RootResolver) resolveMetrics(p graphql.ResolveParams) (interface{}, error) {
	metrics, err := r.storage.GetMetrics(p.Context)
	if err != nil {
		return nil, err
	}

	// Get task type counts from database (optimized - pre-allocate slice)
	typeCountQuery := `
		SELECT type, COUNT(*) as count 
		FROM tasks 
		GROUP BY type
	`
	rows, err := r.storage.GetDB().QueryContext(p.Context, typeCountQuery)
	typeCounts := make([]interface{}, 0, 10) // Pre-allocate with capacity
	if err == nil {
		defer rows.Close()
		for rows.Next() {
			var taskType string
			var count int64
			if err := rows.Scan(&taskType, &count); err == nil {
				typeCounts = append(typeCounts, map[string]interface{}{
					"type":  taskType,
					"count": count,
				})
			}
		}
	}

	return map[string]interface{}{
		"totalTasks":      metrics.QueuedTasks + metrics.ProcessingTasks + metrics.CompletedTasks + metrics.FailedTasks,
		"queuedTasks":     metrics.QueuedTasks,
		"processingTasks": metrics.ProcessingTasks,
		"completedTasks":  metrics.CompletedTasks,
		"failedTasks":     metrics.FailedTasks,
		"activeWorkers":   metrics.ActiveWorkers,
		"taskTypeCounts":  typeCounts,
	}, nil
}

func (r *RootResolver) resolveSubmitTask(p graphql.ResolveParams) (interface{}, error) {
	userID, _ := p.Context.Value("user_id").(string)
	if userID == "" {
		return nil, fmt.Errorf("unauthorized")
	}

	taskType := p.Args["type"].(string)
	payloadStr := p.Args["payload"].(string)
	priority := 0
	if p, ok := p.Args["priority"].(int); ok {
		priority = p
	}
	maxRetries := 3
	if mr, ok := p.Args["maxRetries"].(int); ok {
		maxRetries = mr
	}

	task := &models.Task{
		ID:         generateID(),
		UserID:     userID,
		Type:       models.TaskType(taskType),
		Payload:    json.RawMessage(payloadStr),
		Status:     models.StatusQueued,
		Priority:   priority,
		Retries:    0,
		MaxRetries: maxRetries,
		CreatedAt:  time.Now(),
	}

	if err := r.storage.CreateTask(p.Context, task); err != nil {
		return nil, err
	}

	if err := r.broker.Publish(p.Context, task); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"taskId":    task.ID,
		"status":    string(task.Status),
		"message":   "Task queued successfully",
		"createdAt": task.CreatedAt.Format(time.RFC3339),
	}, nil
}

func (r *RootResolver) resolveDeleteTask(p graphql.ResolveParams) (interface{}, error) {
	id := p.Args["id"].(string)
	if err := r.storage.DeleteTask(p.Context, id); err != nil {
		return nil, err
	}
	return map[string]interface{}{
		"message": "Task deleted successfully",
	}, nil
}

func (r *RootResolver) resolveCancelTask(p graphql.ResolveParams) (interface{}, error) {
	id := p.Args["id"].(string)
	task, err := r.storage.GetTask(p.Context, id)
	if err != nil {
		return nil, err
	}

	task.Status = models.StatusCancelled
	if err := r.storage.UpdateTask(p.Context, task); err != nil {
		return nil, err
	}

	return map[string]interface{}{
		"message": "Task cancelled successfully",
	}, nil
}

// Helper functions
func taskToMap(task *models.Task) map[string]interface{} {
	m := map[string]interface{}{
		"id":         task.ID,
		"type":       string(task.Type),
		"payload":    string(task.Payload),
		"status":     string(task.Status),
		"priority":   task.Priority,
		"retries":    task.Retries,
		"maxRetries": task.MaxRetries,
		"createdAt":  task.CreatedAt.Format(time.RFC3339),
	}

	if task.StartedAt != nil {
		m["startedAt"] = task.StartedAt.Format(time.RFC3339)
	}
	if task.CompletedAt != nil {
		m["completedAt"] = task.CompletedAt.Format(time.RFC3339)
	}
	if task.WorkerID != "" {
		m["workerId"] = task.WorkerID
	}
	if task.ProcessingTime > 0 {
		m["processingTime"] = task.ProcessingTime
	}
	if task.Error != "" {
		m["error"] = task.Error
	}

	return m
}

func generateID() string {
	return uuid.New().String()
}
