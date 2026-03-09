.PHONY: help build run-api run-worker grpc-server test docker-up

help: ## Show this help
	@grep -E '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN {FS = ":.*?## "}; {printf "\033[36m%-20s\033[0m %s\n", $$1, $$2}'

build: ## Build all binaries
	@go build -o bin/api cmd/api/main.go
	@go build -o bin/worker cmd/worker/main.go
	@go build -o bin/grpc-server cmd/grpc-server/main.go

run-api: ## Run API server
	@go run cmd/api/main.go

run-worker: ## Run worker
	@go run cmd/worker/main.go

grpc-server: ## Run gRPC server
	@go run cmd/grpc-server/main.go

test: ## Run tests
	@go test -v ./...

docker-up: ## Start Docker services
	@docker-compose up -d

docker-down: ## Stop Docker services
	@docker-compose down

clean: ## Clean build artifacts
	@rm -rf bin/ storage/images/*
