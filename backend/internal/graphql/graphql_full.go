// File: internal/graphql/server.go
//go:build graphql
// +build graphql

// Full GraphQL implementation (requires dependencies)

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

