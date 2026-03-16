/**
 * src/graphql/queries.js
 *
 * Named GraphQL query documents used by Apollo Client's useQuery hook.
 *
 * Each export is a DocumentNode created by the gql template tag. Apollo parses
 * the SDL string at module load time and caches the AST, so there is no
 * repeated parsing at render time.
 *
 * # GET_TASK
 *   Fetches a single task by UUID. Used by SlideOver or any detail view.
 *   Returns the full field set including error, workerId, and processingTime
 *   which are only populated once a worker handles the task.
 *
 * # GET_TASKS
 *   Paginated task list. Variables:
 *     status   (optional) — filter by task status string
 *     page     (optional) — 1-based page number, default 1
 *     pageSize (optional) — results per page, default determined by the server
 *   Returns a TaskList object with `tasks`, `total`, `page`, `pageSize` fields
 *   matching the taskListType GraphQL type defined in resolvers.go.
 *
 * # GET_WORKERS
 *   Live worker snapshot — all registered worker processes with their counters.
 *   Used by an admin view (not yet built) to inspect worker health.
 *
 * # GET_METRICS
 *   System-wide aggregate counters used by MetricsCard. Polled every 5 s by
 *   DashboardGraphQL. `taskTypeCounts` is a list of {type, count} pairs for
 *   the per-type breakdown chart.
 *
 * # Field name convention
 *
 *   GraphQL fields use camelCase (createdAt, workerId) matching the
 *   camelCase → snake_case mapping performed by the resolver layer.
 *   Components that receive data from this client use camelCase field names
 *   directly; they do NOT need the `task.created_at || task.createdAt` dual
 *   access pattern used by the REST-based components.
 */
import { gql } from '@apollo/client'

export const GET_TASK = gql`
  query GetTask($id: ID!) {
    task(id: $id) {
      id
      type
      status
      priority
      retries
      maxRetries
      createdAt
      startedAt
      completedAt
      workerId
      processingTime
      error
    }
  }
`

export const GET_TASKS = gql`
  query GetTasks($status: String, $page: Int, $pageSize: Int) {
    tasks(status: $status, page: $page, pageSize: $pageSize) {
      tasks {
        id
        type
        status
        priority
        createdAt
        completedAt
      }
      total
      page
      pageSize
    }
  }
`

export const GET_WORKERS = gql`
  query GetWorkers {
    workers {
      workerId
      status
      tasksProcessed
      tasksFailed
      lastHeartbeat
      startTime
      currentTask
      activeGoroutines
    }
  }
`

export const GET_METRICS = gql`
  query GetMetrics {
    metrics {
      totalTasks
      queuedTasks
      processingTasks
      completedTasks
      failedTasks
      activeWorkers
      taskTypeCounts {
        type
        count
      }
    }
  }
`

