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

