import { gql } from '@apollo/client'

export const TASK_UPDATED = gql`
  subscription TaskUpdated($id: ID!) {
    taskUpdated(id: $id) {
      id
      status
      startedAt
      completedAt
      processingTime
      error
    }
  }
`

export const TASK_STATUS_CHANGED = gql`
  subscription TaskStatusChanged {
    taskStatusChanged {
      id
      status
      startedAt
      completedAt
    }
  }
`

