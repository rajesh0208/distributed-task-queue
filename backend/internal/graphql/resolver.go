// File: internal/graphql/resolver.go
//
// Package graphql implements a GraphQL API as an alternative interface to the REST API.
//
// Why GraphQL instead of (or alongside) REST?
// REST requires a separate endpoint per resource and returns a fixed shape — clients
// often over-fetch (receiving fields they don't need) or under-fetch (needing multiple
// round-trips to collect related data).  GraphQL solves both:
//   - Clients declare exactly which fields they want; the server returns only those fields.
//   - A single POST /graphql endpoint handles all queries, mutations, and subscriptions.
//   - Introspection lets clients discover the schema at runtime (great for tooling).
//
// What are resolvers?
// The GraphQL schema defines types (Task, Worker, Metrics) and operations (Query, Mutation).
// A resolver is the Go function that backs each field in the schema — it maps a schema
// operation to an actual data source.  When a client queries { task(id:"x") { status } },
// the runtime calls resolveTask, which fetches from storage and returns the data.
//
// N+1 query problem:
// If a query asks for a list of tasks and a field on each task requires a separate DB lookup,
// you get N+1 DB queries (1 for the list + N for each item).  This file avoids the problem by
// fetching everything needed in a single storage call (ListTasks returns full Task objects)
// rather than making per-item child-resolver calls.
//
// Real-time over GraphQL:
// True GraphQL Subscriptions push updates over a persistent connection (WebSocket or SSE).
// This file only implements Query and Mutation.  For real-time updates, the main API
// uses a WebSocket hub instead — see cmd/api/main.go WebSocketHub.
//
// Build tag:
// This file is compiled only when the "graphql" build tag is set:
//
//	go build -tags graphql ./...
//
// Without the tag, graphql.NewServer (in server.go) returns a stub so the REST API
// still works and the GraphQL route is simply skipped.
//
// Connected to:
//   - storage.Storage  — reads and writes tasks and metrics from PostgreSQL/Redis
//   - broker.Broker    — publishes newly submitted tasks to the Redis Stream
//
// Called by:
//   - graphql.NewServer (server.go) — passes storage and broker here to build the schema
//   - cmd/api/main.go  — calls graphql.NewServer, then mounts the handler at POST /graphql
//
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

// RootResolver is the central struct that holds all dependencies needed by every resolver.
//
// By embedding storage and broker here, each resolver function receives them via the
// receiver (r *RootResolver) rather than through global variables, making the resolvers
// testable — you can substitute fakes for both fields in unit tests.
//
// Fields:
//   - storage: the data layer for reading/writing tasks and metrics
//   - broker:  the message broker for publishing newly created tasks to the Redis Stream
type RootResolver struct {
	storage storage.Storage // reads/writes tasks + metrics (PostgreSQL with Redis cache)
	broker  broker.Broker   // publishes tasks so workers consume them from the Redis Stream
}

// NewRootResolver is the constructor for RootResolver.
// It is called by NewServer in server.go, which already holds storage and broker references.
//
// Parameters:
//   - storage: an initialized storage.Storage implementation (PostgreSQL + Redis)
//   - broker:  an initialized broker.Broker implementation (Redis Streams)
//
// Returns: *RootResolver ready to call GetSchema() on.
func NewRootResolver(storage storage.Storage, broker broker.Broker) *RootResolver {
	return &RootResolver{
		storage: storage,
		broker:  broker,
	}
}

// GetSchema builds and returns the complete executable GraphQL schema.
//
// The schema has two root types:
//   - Query    — read-only operations (fetch a task, list tasks, list workers, get metrics)
//   - Mutation — write operations (submit, delete, cancel a task)
//
// Called by: NewServer in server.go, which passes the resulting schema to the HTTP handler.
//
// Returns:
//   - graphql.Schema: the compiled schema, ready for execution
//   - error:          if schema construction fails (e.g., circular type reference)
func (r *RootResolver) GetSchema() (graphql.Schema, error) {
	return graphql.NewSchema(graphql.SchemaConfig{
		Query:    r.getQueryType(),    // the root Query type with all read resolver fields
		Mutation: r.getMutationType(), // the root Mutation type with all write resolver fields
	})
}

// getQueryType builds the root Query GraphQL object.
//
// Each field on the Query type represents a top-level read operation:
//
//	query { task(id: "...") { id status } }
//	query { tasks(status: "queued", page: 1, pageSize: 20) { tasks { id } total } }
//	query { workers { id status } }
//	query { metrics { totalTasks completedTasks } }
//
// Resolver wiring: each field's Resolve function is a method on RootResolver.
// The graphql runtime calls the matching resolve function when that field is requested.
func (r *RootResolver) getQueryType() *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name: "Query", // must match the schema's root query type name exactly
		Fields: graphql.Fields{
			// "task" — fetch a single task by its UUID.
			// The "id" argument is NonNull: the client must provide it or the request fails validation.
			"task": &graphql.Field{
				Type: taskType, // the GraphQL Task object type (defined in schema.go)
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID), // required — no default; omitting it is a schema violation
					},
				},
				Resolve: r.resolveTask, // calls storage.GetTask and returns a map
			},
			// "tasks" — paginated list of tasks with optional status filter.
			// All three arguments are optional (no NonNull wrapper) with sensible defaults in the resolver.
			"tasks": &graphql.Field{
				Type: taskListType, // a wrapper type with "tasks", "total", "page", "pageSize"
				Args: graphql.FieldConfigArgument{
					"status": &graphql.ArgumentConfig{
						Type: graphql.String, // optional filter: "queued", "processing", "completed", "failed"
					},
					"page": &graphql.ArgumentConfig{
						Type: graphql.Int, // optional: 1-indexed page number (defaults to 1 in resolver)
					},
					"pageSize": &graphql.ArgumentConfig{
						Type: graphql.Int, // optional: results per page (defaults to 20 in resolver)
					},
				},
				Resolve: r.resolveTasks,
			},
			// "workers" — returns live worker heartbeat data (IDs, status, tasks processed, etc.).
			// No arguments needed; the resolver returns all known workers.
			"workers": &graphql.Field{
				Type:    graphql.NewList(workerType), // a list of Worker objects
				Resolve: r.resolveWorkers,
			},
			// "metrics" — aggregate system statistics: task counts per status, active workers.
			// Used by admin dashboards.
			"metrics": &graphql.Field{
				Type:    metricsType, // a single Metrics object with multiple count fields
				Resolve: r.resolveMetrics,
			},
		},
	})
}

// getMutationType builds the root Mutation GraphQL object.
//
// Mutations change server-side state — they are the GraphQL equivalent of
// POST/PUT/DELETE in REST:
//
//	mutation { submitTask(type: "image_resize", payload: "{...}") { taskId status } }
//	mutation { deleteTask(id: "...") { message } }
//	mutation { cancelTask(id: "...") { message } }
//
// Auth: the resolveSubmitTask resolver extracts user_id from the context; if it's
// missing, the mutation returns an "unauthorized" error.  The context is populated by
// the auth middleware before the GraphQL handler is invoked.
func (r *RootResolver) getMutationType() *graphql.Object {
	return graphql.NewObject(graphql.ObjectConfig{
		Name: "Mutation", // root mutation type name
		Fields: graphql.Fields{
			// "submitTask" — creates a new task and enqueues it for a worker.
			// "type" and "payload" are required; "priority" and "maxRetries" are optional.
			"submitTask": &graphql.Field{
				Type: taskResponseType, // returns taskId, status, message, createdAt
				Args: graphql.FieldConfigArgument{
					"type": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String), // required — e.g. "image_resize"
					},
					"payload": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.String), // required — JSON string with task-specific params
					},
					"priority": &graphql.ArgumentConfig{
						Type: graphql.Int, // optional: higher numbers = higher priority (default 0)
					},
					"maxRetries": &graphql.ArgumentConfig{
						Type: graphql.Int, // optional: retry limit before marking failed (default 3)
					},
				},
				Resolve: r.resolveSubmitTask,
			},
			// "deleteTask" — permanently removes a task from the database.
			"deleteTask": &graphql.Field{
				Type: deleteResponseType, // returns a simple { message } object
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID), // required task UUID
					},
				},
				Resolve: r.resolveDeleteTask,
			},
			// "cancelTask" — marks a task as cancelled so workers skip it.
			// Unlike deleteTask, the task record is preserved for auditing.
			"cancelTask": &graphql.Field{
				Type: cancelResponseType, // returns a simple { message } object
				Args: graphql.FieldConfigArgument{
					"id": &graphql.ArgumentConfig{
						Type: graphql.NewNonNull(graphql.ID), // required task UUID
					},
				},
				Resolve: r.resolveCancelTask,
			},
		},
	})
}

// resolveTask is the resolver for the "task" query field.
//
// Called by the GraphQL runtime when a client requests:
//
//	query { task(id: "some-uuid") { ... } }
//
// Flow:
//  1. Extract the "id" argument from the query.
//  2. Call storage.GetTask to fetch from PostgreSQL (with Redis cache).
//  3. Convert the models.Task struct to a map[string]interface{} for the GraphQL runtime.
//
// Parameters:
//   - p.Args["id"]: the task UUID string from the query argument
//   - p.Context:    the request context (carries auth info, tracing spans, deadlines)
//
// Returns: a map representing the task, or nil + error if not found.
func (r *RootResolver) resolveTask(p graphql.ResolveParams) (interface{}, error) {
	id := p.Args["id"].(string) // type assertion is safe: "id" is NonNull in the schema
	task, err := r.storage.GetTask(p.Context, id)
	if err != nil {
		// storage returns an error if the task doesn't exist or if the DB is unreachable
		return nil, err
	}
	return taskToMap(task), nil // convert struct to map so the graphql runtime can project fields
}

// resolveTasks is the resolver for the "tasks" query field.
//
// Called by the GraphQL runtime when a client requests:
//
//	query { tasks(status: "queued", page: 1, pageSize: 20) { tasks { id } total } }
//
// All arguments are optional, so we use type assertions with comma-ok to set defaults.
// The resolver computes a SQL OFFSET from (page-1)*pageSize so storage.ListTasks
// receives the correct slice bounds.
//
// N+1 avoidance: ListTasks returns complete Task objects in a single query, so
// no child-resolver calls are needed — every field on each task is already populated.
//
// Returns: a map with keys "tasks", "total", "page", "pageSize".
func (r *RootResolver) resolveTasks(p graphql.ResolveParams) (interface{}, error) {
	// Extract optional "status" filter; defaults to "" (all statuses) if not provided.
	status := ""
	if s, ok := p.Args["status"].(string); ok {
		status = s
	}

	// Default page to 1; the client can override with page: N in the query.
	page := 1
	if p, ok := p.Args["page"].(int); ok {
		page = p
	}

	// Default page size to 20 results; the client can request a different size.
	pageSize := 20
	if ps, ok := p.Args["pageSize"].(int); ok {
		pageSize = ps
	}

	// ListTasks returns (tasks, totalCount, error).
	// The empty string for userID means "all users" (admin context).
	// Offset = (page-1)*pageSize converts 1-based page number to 0-based SQL offset.
	tasks, total, err := r.storage.ListTasks(p.Context, models.TaskStatus(status), "", pageSize, (page-1)*pageSize)
	if err != nil {
		return nil, err // DB or cache error; return to the client as a GraphQL error
	}

	// Pre-allocate with exact capacity to avoid slice growth during the loop.
	// This is an optimisation: make([]interface{}, len(tasks)) allocates exactly
	// len(tasks) slots rather than growing the backing array on each append.
	taskMaps := make([]interface{}, len(tasks))
	for i := range tasks {
		taskMaps[i] = taskToMap(tasks[i]) // convert each *models.Task to a plain map
	}

	return map[string]interface{}{
		"tasks":    taskMaps, // the page of task maps
		"total":    total,    // total matching tasks across all pages (for the frontend to compute page count)
		"page":     page,     // current page number (echoed back so clients don't have to track it separately)
		"pageSize": pageSize, // effective page size (echoed back in case the client's value was clamped elsewhere)
	}, nil
}

// resolveWorkers is the resolver for the "workers" query field.
//
// Currently returns an empty list because the storage interface's worker-listing
// method depends on the underlying implementation.  A full implementation would
// call storage.ListWorkers(p.Context) and convert each worker to a map.
//
// Returns: empty slice (no workers exposed yet via GraphQL).
func (r *RootResolver) resolveWorkers(p graphql.ResolveParams) (interface{}, error) {
	// Implementation depends on storage methods
	return []interface{}{}, nil
}

// resolveMetrics is the resolver for the "metrics" query field.
//
// Gathers two data sources and merges them:
//  1. storage.GetMetrics — pre-aggregated counts from the DB (queued, processing, etc.)
//  2. A raw SQL query that counts tasks grouped by type — used for the per-type breakdown chart.
//
// Why a raw SQL query for type counts?
// The GetMetrics method on storage returns overall status counts but not per-type counts.
// Rather than adding a new storage method for a single GraphQL field, we query the DB
// directly.  In a larger codebase this would be refactored into storage.
//
// Returns: a map with keys totalTasks, queuedTasks, processingTasks, completedTasks,
// failedTasks, activeWorkers, taskTypeCounts.
func (r *RootResolver) resolveMetrics(p graphql.ResolveParams) (interface{}, error) {
	metrics, err := r.storage.GetMetrics(p.Context)
	if err != nil {
		return nil, err // failed to read aggregate counts from the DB
	}

	// Direct SQL to get per-task-type counts.
	// GROUP BY type gives one row per distinct task type with a count.
	// Pre-allocating with capacity 10 covers the typical number of task types without reallocation.
	typeCountQuery := `
		SELECT type, COUNT(*) as count
		FROM tasks
		GROUP BY type
	`
	rows, err := r.storage.GetDB().QueryContext(p.Context, typeCountQuery)
	typeCounts := make([]interface{}, 0, 10) // Pre-allocate with capacity
	if err == nil {
		defer rows.Close() // always close rows to release the DB connection back to the pool
		for rows.Next() {
			var taskType string
			var count int64
			if err := rows.Scan(&taskType, &count); err == nil {
				// Append a map for each type; the GraphQL runtime will project fields from it.
				typeCounts = append(typeCounts, map[string]interface{}{
					"type":  taskType,
					"count": count,
				})
			}
			// If Scan fails, we silently skip that row rather than aborting the whole response.
		}
	}
	// If the query itself failed (err != nil), typeCounts remains empty — callers still
	// get the status-level counts from GetMetrics even without the type breakdown.

	return map[string]interface{}{
		// totalTasks is derived by summing all terminal and in-flight status counts;
		// this avoids a separate COUNT(*) query on the entire table.
		"totalTasks":      metrics.QueuedTasks + metrics.ProcessingTasks + metrics.CompletedTasks + metrics.FailedTasks,
		"queuedTasks":     metrics.QueuedTasks,     // tasks waiting to be picked up by a worker
		"processingTasks": metrics.ProcessingTasks, // tasks currently being worked on
		"completedTasks":  metrics.CompletedTasks,  // tasks that finished successfully
		"failedTasks":     metrics.FailedTasks,      // tasks that exhausted all retries
		"activeWorkers":   metrics.ActiveWorkers,   // workers that sent a heartbeat within the last 30s
		"taskTypeCounts":  typeCounts,               // per-type breakdown for the admin dashboard chart
	}, nil
}

// resolveSubmitTask is the resolver for the "submitTask" mutation.
//
// This is the GraphQL equivalent of POST /api/v1/tasks.  It:
//  1. Checks that a user_id is present in the context (set by auth middleware before the handler).
//  2. Builds a new Task model with a fresh UUID.
//  3. Persists it to PostgreSQL via storage.CreateTask.
//  4. Publishes it to the Redis Stream via broker.Publish so a worker picks it up.
//
// Why persist before publishing?
// If the stream publish fails after DB write, the task is in a known recoverable state —
// an operator can re-publish it.  If we published first and the DB write failed, the worker
// would try to process a task that doesn't exist in the DB, causing a confusing error.
//
// Parameters (via p.Args):
//   - "type"       (required): e.g. "image_resize"
//   - "payload"    (required): JSON string with task-specific configuration
//   - "priority"   (optional): integer priority, higher = processed sooner (default 0)
//   - "maxRetries" (optional): retry limit before permanently failing (default 3)
//
// Returns: a map with taskId, status, message, createdAt.
func (r *RootResolver) resolveSubmitTask(p graphql.ResolveParams) (interface{}, error) {
	// Extract user ID from the request context — set by the auth middleware.
	// If it's empty, the request reached here without a valid JWT (should not happen in
	// a correctly wired middleware chain, but we guard anyway).
	userID, _ := p.Context.Value("user_id").(string)
	if userID == "" {
		return nil, fmt.Errorf("unauthorized") // no authenticated user — reject the mutation
	}

	taskType := p.Args["type"].(string)      // safe: "type" is NonNull in the schema
	payloadStr := p.Args["payload"].(string) // safe: "payload" is NonNull in the schema

	// Extract optional priority; default to 0 (lowest priority) if not provided.
	priority := 0
	if p, ok := p.Args["priority"].(int); ok {
		priority = p
	}

	// Extract optional maxRetries; default to 3 if not provided.
	maxRetries := 3
	if mr, ok := p.Args["maxRetries"].(int); ok {
		maxRetries = mr
	}

	// Build the task model.  The ID is a fresh UUID; status starts as "queued".
	task := &models.Task{
		ID:         generateID(),                // globally unique UUID for tracking
		UserID:     userID,                      // ties the task to the authenticated user
		Type:       models.TaskType(taskType),   // convert string to the typed TaskType alias
		Payload:    json.RawMessage(payloadStr), // store as raw JSON; the worker unmarshals it
		Status:     models.StatusQueued,         // initial status — worker will change this to "processing"
		Priority:   priority,                    // higher priority tasks are consumed first
		Retries:    0,                           // no retries yet; worker increments on each failure
		MaxRetries: maxRetries,                  // how many times the worker retries before permanent failure
		CreatedAt:  time.Now(),                  // timestamp used for display and sorting
	}

	// Persist the task to PostgreSQL.  If this fails, no DB record is created and the
	// client gets an error — no partial state is left behind.
	if err := r.storage.CreateTask(p.Context, task); err != nil {
		return nil, err // DB write error; task was not created
	}

	// Publish to the Redis Stream so the worker consumer group picks it up.
	// If this fails after a successful DB write, the task exists in the DB but won't
	// be processed until a re-publish happens (e.g. by an operator or retry mechanism).
	if err := r.broker.Publish(p.Context, task); err != nil {
		return nil, err // stream publish error; task is in DB but not yet queued for processing
	}

	return map[string]interface{}{
		"taskId":    task.ID,                             // client uses this to poll GET /tasks/:id
		"status":    string(task.Status),                 // always "queued" at this point
		"message":   "Task queued successfully",
		"createdAt": task.CreatedAt.Format(time.RFC3339), // ISO 8601 timestamp for the client to display
	}, nil
}

// resolveDeleteTask is the resolver for the "deleteTask" mutation.
//
// Permanently removes a task from the database.  No ownership check is performed here
// (unlike the REST API) because GraphQL auth is handled at the middleware/context level.
// A full production implementation would check that the authenticated user owns the task.
//
// Parameters (via p.Args):
//   - "id" (required): UUID of the task to delete
//
// Returns: a map with a "message" key confirming deletion.
func (r *RootResolver) resolveDeleteTask(p graphql.ResolveParams) (interface{}, error) {
	id := p.Args["id"].(string) // safe: "id" is NonNull in the schema
	if err := r.storage.DeleteTask(p.Context, id); err != nil {
		// Could be "task not found" or a DB error — both surface as a GraphQL error to the client
		return nil, err
	}
	return map[string]interface{}{
		"message": "Task deleted successfully",
	}, nil
}

// resolveCancelTask is the resolver for the "cancelTask" mutation.
//
// Marks the task's status as "cancelled" in PostgreSQL.  This is a soft stop:
// if the task is already being processed by a worker, the worker will check the DB
// status or a Redis cancellation signal before writing the result (see worker/main.go isCancelled).
//
// Parameters (via p.Args):
//   - "id" (required): UUID of the task to cancel
//
// Returns: a map with a "message" key confirming cancellation.
func (r *RootResolver) resolveCancelTask(p graphql.ResolveParams) (interface{}, error) {
	id := p.Args["id"].(string) // safe: "id" is NonNull in the schema

	// Fetch the current task to ensure it exists before updating.
	task, err := r.storage.GetTask(p.Context, id)
	if err != nil {
		// task not found or DB error — return as a GraphQL error
		return nil, err
	}

	// Update the status field and persist.  The worker reads this status before and after
	// processing to detect cancellation.
	task.Status = models.StatusCancelled
	if err := r.storage.UpdateTask(p.Context, task); err != nil {
		return nil, err // DB update failed; status remains unchanged
	}

	return map[string]interface{}{
		"message": "Task cancelled successfully",
	}, nil
}

// taskToMap converts a *models.Task struct to a plain map[string]interface{}.
//
// Why convert to a map?
// The graphql-go library uses reflection to project fields from the resolver's return value.
// Returning a map allows it to work generically without needing a registered Go type for
// every GraphQL object type.
//
// Optional fields (StartedAt, CompletedAt, WorkerID, ProcessingTime, Error) are only
// added to the map if they contain meaningful values.  This avoids sending null/zero
// values to clients when the task hasn't started yet.
//
// Called by: resolveTask and resolveTasks.
func taskToMap(task *models.Task) map[string]interface{} {
	m := map[string]interface{}{
		"id":         task.ID,                             // globally unique task UUID
		"type":       string(task.Type),                   // convert typed alias back to string for JSON
		"payload":    string(task.Payload),                // JSON payload as a string
		"status":     string(task.Status),                 // "queued", "processing", "completed", "failed", "cancelled"
		"priority":   task.Priority,                       // integer priority; higher = processed sooner
		"retries":    task.Retries,                        // how many times the worker has retried
		"maxRetries": task.MaxRetries,                     // maximum retries before marking as failed
		"createdAt":  task.CreatedAt.Format(time.RFC3339), // ISO 8601 creation timestamp
	}

	// Only include StartedAt if the task has actually been picked up by a worker.
	if task.StartedAt != nil {
		m["startedAt"] = task.StartedAt.Format(time.RFC3339)
	}
	// Only include CompletedAt if the task has finished (successfully or otherwise).
	if task.CompletedAt != nil {
		m["completedAt"] = task.CompletedAt.Format(time.RFC3339)
	}
	// Only include WorkerID if a worker claimed this task.
	if task.WorkerID != "" {
		m["workerId"] = task.WorkerID
	}
	// Only include ProcessingTime if the task has run (milliseconds elapsed during processing).
	if task.ProcessingTime > 0 {
		m["processingTime"] = task.ProcessingTime
	}
	// Only include Error if the task failed or was retried.
	if task.Error != "" {
		m["error"] = task.Error
	}

	return m
}

// generateID returns a new random UUID as a string.
//
// UUID v4 is used (random) rather than UUID v1 (time-based) to avoid exposing
// the server's MAC address in task IDs and to prevent ID enumeration attacks.
//
// Called by: resolveSubmitTask when creating a new Task.
func generateID() string {
	return uuid.New().String()
}
