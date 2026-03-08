import { ApolloClient, InMemoryCache, split, HttpLink } from '@apollo/client'
import { getMainDefinition } from '@apollo/client/utilities'
// Note: Install graphql-ws for subscriptions: npm install graphql-ws
// import { GraphQLWsLink } from '@apollo/client/link/subscriptions'
// import { createClient } from 'graphql-ws'

// Use relative path to go through Vite proxy
const httpLink = new HttpLink({
  uri: import.meta.env.VITE_GRAPHQL_URI || '/graphql',
})

// WebSocket link for subscriptions (uncomment after installing graphql-ws)
// const wsLink = new GraphQLWsLink(
//   createClient({
//     url: 'ws://localhost:8080/ws',
//   })
// )

// For now, use only HTTP link (subscriptions will use polling)
// const splitLink = split(
//   ({ query }) => {
//     const definition = getMainDefinition(query)
//     return (
//       definition.kind === 'OperationDefinition' &&
//       definition.operation === 'subscription'
//     )
//   },
//   wsLink,
//   httpLink
// )

export const client = new ApolloClient({
  link: httpLink, // Use splitLink when WebSocket is configured
  cache: new InMemoryCache(),
})

