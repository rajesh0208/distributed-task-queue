/**
 * src/graphql/client.js
 *
 * Apollo Client singleton — imported by main.jsx via <ApolloProvider> and
 * used automatically by every useQuery/useMutation/useSubscription call in
 * the component tree.
 *
 * # HttpLink
 *
 *   All GraphQL operations go to /graphql over plain HTTP POST. The URI uses a
 *   relative path so the Vite dev-server proxy (vite.config.js) forwards the
 *   request to http://localhost:8080/graphql without CORS issues. In production,
 *   set VITE_GRAPHQL_URI to the absolute API URL.
 *
 * # InMemoryCache
 *
 *   Apollo normalises query results by `__typename + id` into an in-memory
 *   store. Subsequent queries for the same object (e.g. a task) are served
 *   from cache without a network round-trip. DashboardGraphQL uses
 *   pollInterval rather than cache-only reads, so the cache mainly deduplicates
 *   concurrent queries rendered by multiple components.
 *
 * # WebSocket subscriptions (commented out)
 *
 *   The block using GraphQLWsLink + createClient shows the intended future setup
 *   for real-time push updates. Currently disabled because:
 *     1. graphql-ws is not installed (npm install graphql-ws needed).
 *     2. The Go graphql-go server does not yet have a subscription resolver.
 *   When both are ready, swap `link: httpLink` for `link: splitLink` and
 *   uncomment the wsLink block.
 *
 * # split() — HTTP vs WebSocket routing
 *
 *   The commented-out splitLink uses Apollo's split() helper to route each
 *   operation to the correct transport:
 *     - Subscription operations → wsLink (WebSocket, persistent connection)
 *     - Query / Mutation        → httpLink (one-shot HTTP POST)
 *
 *   getMainDefinition inspects the parsed query AST to determine the operation
 *   type without any string parsing.
 */
import { ApolloClient, InMemoryCache, split, HttpLink } from '@apollo/client'
import { getMainDefinition } from '@apollo/client/utilities'
// Note: Install graphql-ws for subscriptions: npm install graphql-ws
// import { GraphQLWsLink } from '@apollo/client/link/subscriptions'
// import { createClient } from 'graphql-ws'

// Relative URI goes through Vite proxy → http://localhost:8080/graphql.
// Override with VITE_GRAPHQL_URI env var for production deployments.
const httpLink = new HttpLink({
  uri: import.meta.env.VITE_GRAPHQL_URI || '/graphql',
})

// WebSocket link for subscriptions (uncomment after installing graphql-ws)
// const wsLink = new GraphQLWsLink(
//   createClient({
//     url: 'ws://localhost:8080/ws',
//   })
// )

// split() routes subscription operations to wsLink and all others to httpLink.
// For now, use only HTTP link (subscriptions will fall back to polling).
// const splitLink = split(
//   ({ query }) => {
//     const definition = getMainDefinition(query)
//     return (
//       definition.kind === 'OperationDefinition' &&
//       definition.operation === 'subscription'   // true → wsLink, false → httpLink
//     )
//   },
//   wsLink,
//   httpLink
// )

export const client = new ApolloClient({
  link: httpLink,        // Replace with splitLink once WebSocket is configured
  cache: new InMemoryCache(),  // Normalised in-memory store keyed by __typename + id
})

