// File: internal/graphql/server.go  (full implementation — compiled only with -tags graphql)
//
// This file provides the real GraphQL HTTP handler using the graphql-go library.
// It is the counterpart to graphql.go (the stub). Exactly one of the two files
// is compiled into any given binary; the linker selects based on the build tag.
//
// # Architecture
//
// NewServer wires up the dependency graph:
//
//   broker.Broker  ─┐
//                   ├─► NewRootResolver ─► GetSchema ─► handler.New
//   storage.Storage ─┘
//
// The root resolver (resolver.go) holds references to the broker and storage
// so GraphQL resolvers can publish tasks and query the database without global
// state.
//
// # GraphiQL vs GraphQL Playground
//
// handler.New is configured with GraphiQL: false. GraphiQL is the older
// in-handler IDE that ships with graphql-go; it is disabled in favour of
// the richer GraphQL Playground served at /playground by PlaygroundHandler.
// Playground is loaded from CDN (jsdelivr) via an inline HTML string to avoid
// vendoring additional static assets.
//
// # Pretty-printing
//
// Pretty: true formats JSON responses with indentation for human readability.
// In production you may want to set this to false to save bandwidth.
//
// # Enabling this build variant
//
//   go build -tags graphql ./...
//   go test  -tags graphql ./...
//
//go:build graphql
// +build graphql

package graphql

import (
	"net/http"

	"github.com/graphql-go/handler"

	"distributed-task-queue/internal/broker"
	"distributed-task-queue/internal/storage"
)

// NewServer creates a new GraphQL server
func NewServer(b broker.Broker, s storage.Storage) (http.Handler, error) {
	resolver := NewRootResolver(s, b)
	schema, err := resolver.GetSchema()
	if err != nil {
		return nil, err
	}

	h := handler.New(&handler.Config{
		Schema:   &schema,
		Pretty:   true,
		GraphiQL: false,
	})

	return h, nil
}

// PlaygroundHandler returns a GraphQL playground handler
func PlaygroundHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(playgroundHTML))
	}
}

const playgroundHTML = `
<!DOCTYPE html>
<html>
<head>
  <title>GraphQL Playground</title>
  <link rel="stylesheet" href="https://cdn.jsdelivr.net/npm/graphql-playground-react/build/static/css/index.css" />
  <link rel="shortcut icon" href="https://cdn.jsdelivr.net/npm/graphql-playground-react/build/favicon.png" />
  <script src="https://cdn.jsdelivr.net/npm/graphql-playground-react/build/static/js/middleware.js"></script>
</head>
<body>
  <div id="root">
    <style>
      body {
        margin: 0;
        overflow: hidden;
      }
    </style>
    <script>
      window.addEventListener('load', function (event) {
        GraphQLPlayground.init(document.getElementById('root'), {
          endpoint: '/graphql'
        })
      })
    </script>
  </div>
</body>
</html>
`

