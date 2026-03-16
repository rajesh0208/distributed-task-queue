/**
 * src/graphql/subscriptions.js
 *
 * GraphQL subscription documents for real-time task status push updates.
 *
 * # Current status: UNUSED
 *
 *   These documents are defined but not yet imported by any component because:
 *     1. The WebSocket link in graphql/client.js is commented out.
 *     2. The graphql-go server has no subscription resolver implemented.
 *     3. graphql-ws is not installed.
 *
 *   DashboardGraphQL currently falls back to polling (pollInterval: 5000).
 *
 * # How to enable subscriptions
 *
 *  1. Install:   npm install graphql-ws @apollo/client
 *  2. Uncomment the wsLink block in graphql/client.js and set link: splitLink.
 *  3. Add a subscription resolver to the Go schema (resolver.go) that publishes
 *     task updates over a channel.
 *  4. Import TASK_UPDATED or TASK_STATUS_CHANGED in a component and use
 *     Apollo's useSubscription hook.
 *
 * # TASK_UPDATED
 *   Subscribe to changes on a SINGLE task by ID. Useful in a task detail view
 *   to update the status badge in real time once a worker picks it up or
 *   completes it. Returns a partial Task with only the fields that change
 *   during processing (status, startedAt, completedAt, processingTime, error).
 *
 * # TASK_STATUS_CHANGED
 *   Broadcast subscription — fires whenever ANY task changes status.
 *   Useful for the task list to automatically refresh rows without polling.
 *   Returns a minimal set of fields to keep the WebSocket payload small.
 */
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

