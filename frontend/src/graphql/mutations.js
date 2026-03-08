import { gql } from '@apollo/client'

export const SUBMIT_TASK = gql`
  mutation SubmitTask($type: String!, $payload: String!, $priority: Int, $maxRetries: Int) {
    submitTask(type: $type, payload: $payload, priority: $priority, maxRetries: $maxRetries) {
      taskId
      status
      message
      createdAt
    }
  }
`

export const DELETE_TASK = gql`
  mutation DeleteTask($id: ID!) {
    deleteTask(id: $id) {
      message
    }
  }
`

export const CANCEL_TASK = gql`
  mutation CancelTask($id: ID!) {
    cancelTask(id: $id) {
      message
    }
  }
`

