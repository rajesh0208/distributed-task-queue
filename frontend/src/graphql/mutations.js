/**
 * src/graphql/mutations.js
 *
 * Named GraphQL mutation documents used by Apollo Client's useMutation hook.
 *
 * # SUBMIT_TASK
 *   Creates a new task in the queue.
 *   Arguments:
 *     type        (String!, required) — e.g. "image_resize"
 *     payload     (String!, required) — JSON-serialised task parameters
 *     priority    (Int, optional)     — higher value = higher priority; default 0
 *     maxRetries  (Int, optional)     — number of automatic retries on failure; default 3
 *   Returns TaskResponse:
 *     taskId     — UUID of the newly created task
 *     status     — always "queued" immediately after creation
 *     message    — human-readable confirmation string
 *     createdAt  — ISO 8601 timestamp
 *
 *   Note: payload is a JSON *string*, not a JSON scalar, because graphql-go's
 *   default schema does not include a JSON scalar type. DashboardGraphQL serialises
 *   the payload object with JSON.stringify before passing it here.
 *
 * # DELETE_TASK
 *   Permanently deletes a task record from the database.
 *   Only safe to call on terminal tasks (completed/failed); deleting a
 *   processing task may leave the worker in an inconsistent state.
 *   Returns DeleteResponse { message }.
 *
 * # CANCEL_TASK
 *   Marks a queued task as cancelled so workers skip it.
 *   Unlike DELETE_TASK, the record remains in the database for audit purposes.
 *   Returns CancelResponse { message }.
 */
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

