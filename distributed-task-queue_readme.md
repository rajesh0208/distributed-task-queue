# Distributed Task Queue System - Comprehensive Documentation

## Table of Contents
1. [Overview](#overview)
2. [Codebase Structure](#codebase-structure)
3. [Why Use This System?](#why-use-this-system)
4. [Where Is It Used?](#where-is-it-used)
5. [Future Benefits](#future-benefits)
6. [System Architecture](#system-architecture)
7. [Component Deep Dive](#component-deep-dive)
8. [Technology Stack](#technology-stack)
9. [Features](#features)

---

## Overview

The **Distributed Task Queue System** is a production-ready, scalable, and resilient task processing system designed for handling asynchronous workloads, particularly image processing tasks. It provides a robust foundation for building distributed systems that require reliable task execution, monitoring, and management.

### Core Purpose
- **Asynchronous Task Processing**: Decouple task submission from execution
- **Scalability**: Horizontally scale workers based on load
- **Reliability**: Ensure task completion with retry mechanisms and circuit breakers
- **Observability**: Comprehensive monitoring and metrics
- **Multi-Protocol Support**: REST, gRPC, and GraphQL APIs

---

## Codebase Structure

### Directory Overview

```
distributed-task-queue/
├── cmd/                          # Application entry points
│   ├── api/                      # REST API server
│   │   └── main.go              # API server entry point
│   ├── worker/                  # Worker process
│   │   └── main.go              # Worker entry point
│   └── grpc-server/             # gRPC server (optional)
│       └── main.go              # gRPC server entry point
│
├── internal/                      # Internal packages (not exported)
│   ├── broker/                   # Message broker implementations
│   │   ├── redis_stream.go      # Redis Streams broker (primary)
│   │   └── redis_cluster.go     # Redis Cluster broker (optional)
│   │
│   ├── storage/                  # Storage layer
│   │   ├── storage.go           # PostgreSQL + Redis cache storage
│   │   └── s3.go                # S3 storage (optional, build tag: s3)
│   │
│   ├── cache/                    # Caching layer
│   │   └── distributed.go        # Distributed cache (optional, not used)
│   │
│   ├── processor/               # Task processors
│   │   └── image_processor.go   # Image processing tasks
│   │
│   ├── models/                  # Data models
│   │   └── models.go            # Task, User, Metrics models
│   │
│   ├── security/                 # Security features
│   │   ├── auth.go              # JWT authentication & RBAC
│   │   ├── ratelimit.go         # Rate limiting (Redis-based)
│   │   └── validator.go         # Input validation
│   │
│   ├── reliability/              # Reliability patterns
│   │   ├── retry.go             # Retry logic with exponential backoff
│   │   ├── circuitbreaker.go    # Circuit breaker pattern
│   │   ├── deduplication.go     # Task deduplication (Redis)
│   │   ├── graceful_shutdown.go # Graceful shutdown handling
│   │   └── health.go             # Health check endpoints
│   │
│   ├── monitoring/               # Observability
│   │   ├── metrics.go           # Prometheus metrics
│   │   └── middleware.go        # Metrics middleware
│   │
│   ├── tracing/                  # Distributed tracing
│   │   └── tracing.go           # OpenTelemetry/Jaeger (optional, build tag: tracing)
│   │
│   ├── database/                 # Database utilities
│   │   └── pool.go              # Connection pooling (optional)
│   │
│   ├── grpc/                     # gRPC implementations
│   │   ├── task_server.go       # TaskService gRPC server (build tag: grpc)
│   │   ├── worker_server.go     # WorkerService (stub, not used)
│   │   ├── coordinator_server.go # Coordinator service (stub)
│   │   └── client.go             # gRPC client utilities
│   │
│   └── graphql/                  # GraphQL implementation
│       ├── graphql.go           # GraphQL server stub (default)
│       ├── graphql_full.go      # Full GraphQL server (build tag: graphql)
│       ├── resolver.go          # GraphQL resolvers
│       ├── resolvers.go         # Additional resolvers
│       └── schema.graphql       # GraphQL schema definition
│
├── proto/                        # Protocol buffers
│   ├── taskqueue.proto          # gRPC service definitions
│   ├── taskqueue.pb.go         # Generated Go code
│   └── taskqueue_grpc.pb.go    # Generated gRPC code
│
├── dashboard/                    # React frontend (JavaScript)
│   ├── src/
│   │   ├── main.jsx             # React entry point
│   │   ├── App.jsx              # Main app component
│   │   ├── components/
│   │   │   ├── Dashboard.jsx   # REST API dashboard
│   │   │   ├── DashboardGraphQL.jsx # GraphQL dashboard
│   │   │   ├── TaskList.jsx    # Task list component
│   │   │   └── MetricsCard.jsx # Metrics display
│   │   └── graphql/
│   │       ├── client.js        # Apollo Client setup
│   │       ├── queries.js       # GraphQL queries
│   │       ├── mutations.js    # GraphQL mutations
│   │       └── subscriptions.js # GraphQL subscriptions
│   ├── package.json             # npm dependencies
│   └── vite.config.js           # Vite configuration
│
├── docker/                       # Dockerfiles
│   ├── Dockerfile.api           # API server image
│   ├── Dockerfile.worker        # Worker image
│   └── Dockerfile.grpc          # gRPC server image
│
├── k8s/                          # Kubernetes manifests
│   ├── namespace.yaml           # K8s namespace
│   ├── configmap.yaml           # Configuration
│   ├── api-deployment.yaml      # API deployment
│   ├── worker-deployment.yaml   # Worker deployment
│   ├── ingress.yaml             # Ingress configuration
│   └── hpa.yaml                 # Horizontal Pod Autoscaler
│
├── configs/                      # Configuration files
│   ├── prometheus.yml           # Prometheus config
│   ├── alerts.yml               # Alert rules
│   ├── alertmanager.yml         # Alertmanager config
│   └── jaeger.yml               # Jaeger config
│
├── migrations/                   # Database migrations
│   ├── 001_initial_schema.up.sql
│   ├── 001_initial_schema.down.sql
│   ├── 002_add_indexes.up.sql
│   └── 002_add_indexes.down.sql
│
├── scripts/                      # Utility scripts
│   ├── setup.sh                 # Initial setup
│   ├── migrate-db.sh            # Database migration
│   ├── generate-proto.sh        # Generate protobuf files
│   ├── test-all-apis.sh         # API testing
│   └── quick-test.sh            # Quick tests
│
├── tests/                        # Test files
│   ├── integration/              # Integration tests
│   │   ├── auth_test.go         # Auth tests
│   │   └── auth_manual_test.sh  # Manual auth tests
│   ├── unit/                    # Unit tests
│   └── load/                    # Load tests
│
├── grafana/                      # Grafana dashboards
│   ├── dashboards/              # Dashboard JSON files
│   └── datasources/             # Data source configs
│
├── storage/                      # Local file storage
│   └── images/                  # Processed images
│
├── examples/                     # Example code
├── docs/                         # Additional documentation
│
├── docker-compose.yml            # Docker Compose configuration
├── go.mod                        # Go module definition
├── go.sum                        # Go module checksums
├── Makefile                      # Build automation
└── distributed-task-queue_readme.md # This file
```

### Key Directories Explained

#### `cmd/` - Application Entry Points
- **Purpose**: Contains main application entry points
- **Files**:
  - `api/main.go`: REST API server with Fiber framework
  - `worker/main.go`: Worker process that consumes tasks
  - `grpc-server/main.go`: Standalone gRPC server (optional)

#### `internal/` - Internal Packages
- **Purpose**: Private packages not exported outside the module
- **Structure**:
  - **`broker/`**: Message broker implementations (Redis Streams, Redis Cluster)
  - **`storage/`**: Data persistence layer (PostgreSQL + Redis cache)
  - **`processor/`**: Task processing logic (image processing)
  - **`security/`**: Authentication, authorization, rate limiting
  - **`reliability/`**: Retry, circuit breaker, deduplication
  - **`monitoring/`**: Prometheus metrics collection
  - **`grpc/`**: gRPC service implementations
  - **`graphql/`**: GraphQL server and resolvers

#### `proto/` - Protocol Buffers
- **Purpose**: gRPC service definitions
- **Files**:
  - `taskqueue.proto`: Service and message definitions
  - `*.pb.go`: Generated Go code from proto files

#### `dashboard/` - Frontend Application
- **Purpose**: React dashboard for monitoring and task management
- **Technology**: React (JavaScript), Vite, Apollo Client, Tailwind CSS
- **Components**: REST and GraphQL dashboards

#### `docker/` - Containerization
- **Purpose**: Dockerfiles for building container images
- **Images**: API server, Worker, gRPC server

#### `k8s/` - Kubernetes Deployment
- **Purpose**: Kubernetes manifests for production deployment
- **Resources**: Deployments, Services, Ingress, HPA, ConfigMaps

#### `configs/` - Configuration Files
- **Purpose**: External service configurations
- **Services**: Prometheus, Grafana, Jaeger, Alertmanager

#### `migrations/` - Database Migrations
- **Purpose**: Database schema versioning
- **Format**: SQL up/down migrations

#### `scripts/` - Utility Scripts
- **Purpose**: Automation scripts for common tasks
- **Scripts**: Setup, migration, testing, proto generation

### Build Tags

The codebase uses Go build tags for optional features:

- **`+build graphql`**: Enables GraphQL server
- **`+build grpc`**: Enables gRPC server
- **`+build tracing`**: Enables OpenTelemetry tracing
- **`+build s3`**: Enables S3 storage backend

**Usage**:
```bash
# Build with GraphQL
go build -tags graphql ./cmd/api

# Build with all optional features
go build -tags "graphql,grpc,tracing" ./cmd/api
```

### File Naming Conventions

- **Go files**: `snake_case.go` (e.g., `redis_stream.go`)
- **Test files**: `*_test.go` (e.g., `auth_test.go`)
- **Build tag files**: `*_full.go` or `*_stub.go`
- **Config files**: `*.yml`, `*.yaml`
- **Migration files**: `NNN_description.up.sql` / `.down.sql`

### Module Structure

```
Module: distributed-task-queue
├── cmd/          → Executables
├── internal/     → Private packages
├── proto/        → Generated code
├── pkg/          → Public packages (if any)
└── dashboard/    → Frontend (separate npm project)
```

---

## Why Use This System?

### 1. **Decoupling and Asynchronous Processing**
- **Problem**: Synchronous processing blocks API responses and creates poor user experience
- **Solution**: Submit tasks immediately, process asynchronously, and notify when complete
- **Benefit**: Fast API responses, better user experience, and improved system throughput

### 2. **Scalability Requirements**
- **Problem**: Single-server processing creates bottlenecks
- **Solution**: Distributed workers that can scale independently
- **Benefit**: Handle varying loads by adding/removing workers dynamically

### 3. **Reliability and Fault Tolerance**
- **Problem**: Task failures can cause data loss or require manual intervention
- **Solution**: Built-in retry logic, circuit breakers, and task persistence
- **Benefit**: Automatic recovery from transient failures, no data loss

### 4. **Resource Management**
- **Problem**: CPU-intensive tasks (like image processing) can overwhelm servers
- **Solution**: Isolate processing in dedicated worker processes
- **Benefit**: API servers remain responsive, workers handle heavy computation

### 5. **Observability and Monitoring**
- **Problem**: Difficult to track task progress, system health, and performance
- **Solution**: Comprehensive metrics, health checks, and distributed tracing
- **Benefit**: Proactive issue detection, performance optimization, capacity planning

### 6. **Multi-Tenancy and Security**
- **Problem**: Need to support multiple users with different access levels
- **Solution**: JWT authentication, role-based access control, rate limiting
- **Benefit**: Secure multi-user system with proper isolation

---

## Where Is It Used?

### Use Cases

#### 1. **Image Processing Services**
- **E-commerce Platforms**: Generate product thumbnails, resize images for different devices
- **Social Media Platforms**: Process user uploads, apply filters, create thumbnails
- **Content Management Systems**: Optimize images for web delivery
- **Photo Editing Services**: Batch processing, format conversion, watermarking

#### 2. **Data Processing Pipelines**
- **ETL Jobs**: Transform and load data asynchronously
- **Report Generation**: Generate complex reports without blocking users
- **Data Analytics**: Process large datasets in background

#### 3. **Notification Systems**
- **Email Sending**: Queue emails for delivery
- **SMS/Push Notifications**: Send notifications asynchronously
- **Webhook Delivery**: Deliver webhooks reliably with retries

#### 4. **File Processing**
- **Document Conversion**: Convert PDFs, documents to different formats
- **Video Processing**: Transcode videos, generate thumbnails
- **Archive Extraction**: Extract and process compressed files

#### 5. **Scheduled Jobs**
- **Cron-like Tasks**: Execute scheduled tasks across distributed system
- **Batch Operations**: Process large batches of records
- **Maintenance Tasks**: Run system maintenance without downtime

### Industry Applications

- **SaaS Platforms**: Multi-tenant task processing
- **Microservices Architecture**: Inter-service communication and task delegation
- **Cloud Services**: Scalable background job processing
- **IoT Systems**: Process device data asynchronously
- **Financial Services**: Process transactions, generate reports
- **Healthcare**: Process medical images, generate reports

---

## Future Benefits

### 1. **Horizontal Scalability**
- **Current**: Can scale workers independently
- **Future**: Auto-scaling based on queue depth, Kubernetes HPA integration
- **Benefit**: Cost optimization, automatic resource management

### 2. **Multi-Region Deployment**
- **Current**: Single-region deployment
- **Future**: Multi-region support with geo-replication
- **Benefit**: Lower latency, disaster recovery, compliance

### 3. **Advanced Task Types**
- **Current**: Image processing focused
- **Future**: Plugin system for custom task types
- **Benefit**: Extensibility, support for diverse workloads

### 4. **Machine Learning Integration**
- **Current**: Rule-based processing
- **Future**: ML-based task prioritization, anomaly detection
- **Benefit**: Intelligent task routing, predictive scaling

### 5. **Enhanced Observability**
- **Current**: Prometheus metrics, basic tracing
- **Future**: Advanced analytics, predictive alerts, cost tracking
- **Benefit**: Better insights, proactive management

### 6. **Serverless Integration**
- **Current**: Container-based workers
- **Future**: Serverless worker execution (AWS Lambda, Google Cloud Functions)
- **Benefit**: Pay-per-execution, infinite scalability

### 7. **Workflow Orchestration**
- **Current**: Single task execution
- **Future**: Task dependencies, workflow graphs, conditional execution
- **Benefit**: Complex business logic, multi-step processes

### 8. **Cost Optimization**
- **Current**: Fixed worker resources
- **Future**: Spot instance support, resource optimization algorithms
- **Benefit**: Reduced infrastructure costs

---

## System Architecture

### High-Level Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────┐
│                         CLIENT LAYER                                    │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                │
│  │   Web App    │  │  Mobile App  │  │  API Client  │                │
│  └──────┬───────┘  └──────┬───────┘  └──────┬───────┘                │
│         │                  │                  │                          │
└─────────┼──────────────────┼──────────────────┼──────────────────────────┘
          │                  │                  │
          │  REST / gRPC / GraphQL / WebSocket
          │
┌─────────▼──────────────────────────────────────────────────────────────┐
│                         API GATEWAY LAYER                              │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │                    API Server (Fiber)                             │  │
│  │  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐          │  │
│  │  │   REST API   │  │   gRPC API  │  │  GraphQL API │          │  │
│  │  └──────────────┘  └──────────────┘  └──────────────┘          │  │
│  │                                                                   │  │
│  │  ┌──────────────────────────────────────────────────────────┐  │  │
│  │  │              Middleware Stack                              │  │  │
│  │  │  • Authentication (JWT)                                     │  │  │
│  │  │  • Rate Limiting                                            │  │  │
│  │  │  • Request Validation                                       │  │  │
│  │  │  • Metrics Collection                                       │  │  │
│  │  │  • CORS                                                     │  │  │
│  │  └──────────────────────────────────────────────────────────┘  │  │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└─────────┬──────────────────────────────────────────────────────────────┘
          │
          │ Task Submission
          │
┌─────────▼──────────────────────────────────────────────────────────────┐
│                      MESSAGE BROKER LAYER                               │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │                    Redis Streams                                  │  │
│  │  • Task Queue (Stream)                                           │  │
│  │  • Consumer Groups                                                │  │
│  │  • Message Persistence                                            │  │
│  │  • At-least-once Delivery                                         │  │
│  └──────────────────────────────────────────────────────────────────┘  │
└─────────┬──────────────────────────────────────────────────────────────┘
          │
          │ Task Consumption
          │
┌─────────▼──────────────────────────────────────────────────────────────┐
│                      WORKER LAYER                                       │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                 │
│  │   Worker 1   │  │   Worker 2   │  │   Worker N   │                 │
│  │              │  │              │  │              │                 │
│  │  • Subscribe │  │  • Subscribe │  │  • Subscribe │                 │
│  │  • Process   │  │  • Process   │  │  • Process   │                 │
│  │  • Update    │  │  • Update    │  │  • Update    │                 │
│  │  • Heartbeat │  │  • Heartbeat │  │  • Heartbeat │                 │
│  └──────────────┘  └──────────────┘  └──────────────┘                 │
│                                                                          │
│  ┌──────────────────────────────────────────────────────────────────┐  │
│  │              Image Processor                                      │  │
│  │  • Resize, Compress, Watermark, Filter, Thumbnail, Format Convert│  │
│  └──────────────────────────────────────────────────────────────────┘  │
└─────────┬──────────────────────────────────────────────────────────────┘
          │
          │ Task Status Updates
          │
┌─────────▼──────────────────────────────────────────────────────────────┐
│                      STORAGE LAYER                                      │
│  ┌──────────────────────┐  ┌──────────────────────┐                 │
│  │   PostgreSQL          │  │   Redis Cache        │                 │
│  │  • Task Metadata     │  │  • Task Status Cache │                 │
│  │  • User Data         │  │  • Session Data      │                 │
│  │  • Worker Metrics    │  │  • Rate Limit Data   │                 │
│  │  • System Metrics    │  │                      │                 │
│  └──────────────────────┘  └──────────────────────┘                 │
│                                                                          │
│  ┌──────────────────────┐                                              │
│  │   File Storage       │                                              │
│  │  • Processed Images  │                                              │
│  │  • Local / S3        │                                              │
│  └──────────────────────┘                                              │
└──────────────────────────────────────────────────────────────────────────┘

┌─────────────────────────────────────────────────────────────────────────┐
│                      OBSERVABILITY LAYER                                │
│  ┌──────────────┐  ┌──────────────┐  ┌──────────────┐                │
│  │  Prometheus  │  │   Grafana    │  │    Jaeger     │                │
│  │  • Metrics   │  │  • Dashboards│  │  • Tracing    │                │
│  └──────────────┘  └──────────────┘  └──────────────┘                │
└─────────────────────────────────────────────────────────────────────────┘
```

### Data Flow Diagram

```
┌──────────┐
│  Client  │
└────┬─────┘
     │ 1. POST /api/v1/tasks
     │    { type: "image_resize", payload: {...} }
     ▼
┌─────────────────┐
│   API Server    │
│  • Validate     │
│  • Authenticate │
│  • Rate Limit   │
└────┬────────────┘
     │ 2. Create Task Record
     ▼
┌─────────────────┐
│   PostgreSQL     │
│  • Store Task   │
│  • Status: queued│
└────┬────────────┘
     │ 3. Publish to Stream
     ▼
┌─────────────────┐
│  Redis Streams  │
│  • Add to Queue │
└────┬────────────┘
     │ 4. Worker Consumes
     ▼
┌─────────────────┐
│     Worker      │
│  • Subscribe    │
│  • Claim Task   │
└────┬────────────┘
     │ 5. Update Status: processing
     ▼
┌─────────────────┐
│   PostgreSQL     │
│  • Update Task  │
└────┬────────────┘
     │ 6. Process Image
     ▼
┌─────────────────┐
│ Image Processor │
│  • Download     │
│  • Transform   │
│  • Save        │
└────┬────────────┘
     │ 7. Update Status: completed
     ▼
┌─────────────────┐
│   PostgreSQL     │
│  • Final Update │
│  • Store Result │
└────┬────────────┘
     │ 8. WebSocket / Polling
     ▼
┌──────────┐
│  Client  │
│  • Get   │
│  • Result│
└──────────┘
```

### Component Interaction Flow

```
┌─────────────┐
│   Client    │
└──────┬──────┘
       │
       │ HTTP Request
       ▼
┌─────────────────────────────────────────────────────────┐
│                    API Server                             │
│  ┌───────────────────────────────────────────────────┐  │
│  │  Request → Auth Middleware → Rate Limiter         │  │
│  │  → Validator → Handler → Storage → Broker         │  │
│  └───────────────────────────────────────────────────┘  │
└──────┬───────────────────────────────────────────────────┘
       │
       ├───► PostgreSQL (Persist Task)
       │
       └───► Redis Streams (Queue Task)
                    │
                    │ Worker Subscribes
                    ▼
┌─────────────────────────────────────────────────────────┐
│                    Worker Process                        │
│  ┌───────────────────────────────────────────────────┐  │
│  │  Subscribe → Claim → Process → Update → ACK      │  │
│  │                                                     │  │
│  │  ┌─────────────────────────────────────────────┐  │  │
│  │  │  Circuit Breaker → Retry Logic             │  │  │
│  │  │  → Image Processor → Storage Update         │  │  │
│  │  └─────────────────────────────────────────────┘  │  │
│  └───────────────────────────────────────────────────┘  │
└──────┬───────────────────────────────────────────────────┘
       │
       ├───► PostgreSQL (Update Status)
       │
       ├───► File Storage (Save Result)
       │
       └───► Prometheus (Metrics)
                    │
                    ▼
            ┌───────────────┐
            │   Grafana     │
            │  (Visualize)  │
            └───────────────┘
```

---

## Component Deep Dive

### 1. API Server (`cmd/api/main.go`)

**Purpose**: Main entry point for all client interactions

**Responsibilities**:
- Handle HTTP requests (REST, GraphQL, WebSocket)
- Authentication and authorization
- Request validation and rate limiting
- Task submission and status queries
- User management
- Metrics collection

**Key Features**:
- **Multi-Protocol Support**: REST, gRPC, GraphQL, WebSocket
- **Middleware Stack**: Auth, rate limiting, CORS, logging, recovery
- **WebSocket Hub**: Real-time task updates
- **Graceful Shutdown**: Clean resource cleanup

**Flow**:
1. Client sends request
2. Middleware processes (auth, rate limit, validation)
3. Handler processes business logic
4. Storage persists data
5. Broker queues task
6. Response sent to client

---

### 2. Worker (`cmd/worker/main.go`)

**Purpose**: Process tasks from the queue

**Responsibilities**:
- Subscribe to Redis Streams
- Claim and process tasks
- Update task status
- Handle failures and retries
- Send heartbeats
- Report metrics

**Key Features**:
- **Concurrent Processing**: Multiple goroutines per worker
- **Circuit Breaker**: Prevent cascading failures
- **Retry Logic**: Exponential backoff for failures
- **Heartbeat**: Worker health monitoring
- **Graceful Shutdown**: Complete current tasks before exit

**Flow**:
1. Subscribe to Redis Stream consumer group
2. Claim pending messages
3. Update task status to "processing"
4. Process task (with circuit breaker)
5. Update status to "completed" or "failed"
6. Acknowledge message
7. Send heartbeat

---

### 3. Message Broker (`internal/broker/redis_stream.go`)

**Purpose**: Reliable message delivery between API and workers

**Responsibilities**:
- Publish tasks to stream
- Subscribe to stream with consumer groups
- Acknowledge processed messages
- Track pending messages

**Key Features**:
- **Redis Streams**: Persistent, ordered message queue
- **Consumer Groups**: Load balancing across workers
- **At-least-once Delivery**: Message acknowledgment
- **Pending Message Tracking**: Handle unprocessed messages

**Flow**:
1. API publishes task to stream
2. Worker subscribes via consumer group
3. Worker claims message
4. Worker processes and acknowledges
5. Unacknowledged messages remain pending

---

### 4. Storage Layer (`internal/storage/storage.go`)

**Purpose**: Persist task data and metadata

**Responsibilities**:
- Store task records
- Update task status
- Query tasks (by status, type, etc.)
- Store user data
- Record worker metrics
- Cache frequently accessed data

**Key Features**:
- **PostgreSQL**: Primary data store
- **Redis Cache**: Fast lookups
- **Connection Pooling**: Efficient database usage
- **Automatic Schema**: Table creation on startup
- **Indexes**: Optimized queries

**Data Model**:
- **Tasks Table**: Task metadata, status, results
- **Workers Table**: Worker metrics, heartbeats
- **Users Table**: User accounts, roles, API keys

---

### 5. Image Processor (`internal/processor/image_processor.go`)

**Purpose**: Execute image processing operations

**Responsibilities**:
- Download images from URLs
- Apply transformations (resize, compress, filter, etc.)
- Save processed images
- Generate thumbnails
- Format conversion

**Supported Operations**:
- **Resize**: Change dimensions (with aspect ratio option)
- **Compress**: Reduce file size
- **Watermark**: Add watermarks
- **Filter**: Apply filters (grayscale, blur, etc.)
- **Thumbnail**: Generate thumbnails
- **Format Convert**: Convert between formats

**Flow**:
1. Parse task payload
2. Download source image (with timeout)
3. Apply transformation
4. Save to storage
5. Update task with result URL

---

### 6. Security (`internal/security/`)

#### Authentication (`auth.go`)
- **JWT Tokens**: Stateless authentication
- **Password Hashing**: bcrypt with cost factor 14
- **Token Validation**: Expiration and blacklist checking
- **Role-Based Access**: Admin, user roles

#### Rate Limiting (`ratelimit.go`)
- **Token Bucket Algorithm**: Smooth rate limiting
- **Per-User Limits**: Individual user quotas
- **Redis-Backed**: Distributed rate limiting

#### Validation (`validator.go`)
- **URL Validation**: Prevent SSRF attacks
- **Payload Validation**: Ensure correct task structure
- **Domain Whitelist**: Restrict allowed domains

---

### 7. Reliability (`internal/reliability/`)

#### Circuit Breaker (`circuitbreaker.go`)
- **Three States**: Closed, Open, Half-Open
- **Failure Threshold**: Configurable failure count
- **Timeout**: Automatic recovery attempt
- **Prevents Cascading Failures**: Stops processing when downstream fails

#### Retry Logic (`retry.go`)
- **Exponential Backoff**: Increasing delay between retries
- **Max Retries**: Configurable retry limit
- **Jitter**: Randomize backoff to prevent thundering herd

#### Task Deduplication (`deduplication.go`)
- **Prevent Duplicates**: Same task not processed twice
- **Hash-Based**: Task content hashing
- **TTL**: Automatic cleanup

#### Graceful Shutdown (`graceful_shutdown.go`)
- **Signal Handling**: SIGTERM, SIGINT
- **Drain Connections**: Complete current requests
- **Resource Cleanup**: Close connections, save state

---

### 8. Monitoring (`internal/monitoring/`)

#### Metrics (`metrics.go`)
- **Task Metrics**: Submitted, processed, failed, duration
- **Worker Metrics**: Active workers, heartbeats
- **API Metrics**: Request count, duration, status codes
- **System Metrics**: Queue length, database connections

#### Middleware (`middleware.go`)
- **Request Duration**: Track API response times
- **Status Codes**: Monitor success/failure rates
- **Endpoint Tracking**: Per-endpoint metrics

**Prometheus Integration**:
- Exposes `/metrics` endpoint
- Standard Prometheus format
- Grafana dashboards available

---

### 9. GraphQL (`internal/graphql/`)

**Purpose**: Flexible query interface

**Features**:
- **Schema Definition**: Type-safe queries
- **Resolvers**: Business logic for queries/mutations
- **Playground**: Interactive query interface
- **Real-time Subscriptions**: WebSocket support

**Queries**:
- `task(id)`: Get single task
- `tasks(page, pageSize, status)`: List tasks
- `metrics`: System metrics

**Mutations**:
- `submitTask`: Create new task

---

### 10. gRPC (`internal/grpc/`)

**Purpose**: High-performance RPC interface

**Features**:
- **Protocol Buffers**: Efficient serialization
- **Streaming**: Bidirectional streaming support
- **Reflection**: Service discovery
- **Multiple Services**: Task, Worker, Coordinator

**Services**:
- **TaskService**: Task CRUD operations
- **WorkerService**: Worker management
- **CoordinatorService**: System coordination

---

## API Protocols: REST, gRPC, and GraphQL

### Overview

This system supports three different API protocols, each optimized for specific use cases and client requirements. Understanding when and why to use each protocol is crucial for optimal system integration.

---

### REST API

#### Where It's Used

**Primary Endpoint**: `http://localhost:8080/api/v1/`

**Available Endpoints**:
- `POST /api/v1/auth/register` - User registration
- `POST /api/v1/auth/login` - User authentication
- `GET /api/v1/users/me` - Get current user
- `PUT /api/v1/users/me` - Update current user
- `POST /api/v1/tasks` - Submit new task
- `GET /api/v1/tasks/:id` - Get task status
- `GET /api/v1/tasks` - List tasks (with pagination)
- `DELETE /api/v1/tasks/:id` - Delete task
- `GET /api/v1/metrics/system` - System metrics
- `GET /api/v1/health` - Health check
- `GET /metrics` - Prometheus metrics

#### Purpose and Use Cases

**1. Web Applications**
- **Why**: REST is the standard for web applications
- **Use Case**: React dashboard, web forms, AJAX requests
- **Benefits**: 
  - Simple HTTP requests
  - Easy to debug with browser DevTools
  - Works with standard HTTP libraries
  - Cache-friendly (HTTP caching)

**2. Mobile Applications**
- **Why**: Native mobile apps can easily consume REST APIs
- **Use Case**: iOS/Android apps submitting tasks, checking status
- **Benefits**:
  - Standard HTTP libraries available
  - Works over standard ports (80/443)
  - Easy to implement retry logic
  - Works with mobile network proxies

**3. Third-Party Integrations**
- **Why**: Most third-party services expect REST APIs
- **Use Case**: Webhooks, API integrations, external services
- **Benefits**:
  - Universal compatibility
  - Easy to document (OpenAPI/Swagger)
  - Works with API gateways
  - Standard authentication (JWT, OAuth)

**4. Quick Prototyping**
- **Why**: Fastest to implement and test
- **Use Case**: Testing, development, proof of concepts
- **Benefits**:
  - Can test with curl, Postman, or browser
  - No code generation needed
  - Human-readable requests/responses
  - Easy to share examples

**5. Public APIs**
- **Why**: Most developers are familiar with REST
- **Use Case**: Public-facing APIs, developer portals
- **Benefits**:
  - Lower learning curve
  - Extensive documentation available
  - Works with API documentation tools
  - Easy to version (URL-based versioning)

**Characteristics**:
- **Protocol**: HTTP/1.1, HTTP/2
- **Format**: JSON
- **Stateless**: Each request is independent
- **Cacheable**: Supports HTTP caching
- **Port**: 8080 (default)

---

### gRPC API

#### Where It's Used

**Primary Endpoint**: `localhost:50051` (default)

**Available Services**:
- **TaskService** (`proto/taskqueue.proto`):
  - `SubmitTask` - Submit new task
  - `GetTask` - Get task by ID
  - `ListTasks` - List tasks with filters
  - `DeleteTask` - Delete task
  - `CancelTask` - Cancel running task

- **WorkerService**:
  - `RegisterWorker` - Register new worker
  - `GetWorkerStatus` - Get worker status
  - `ListWorkers` - List all workers
  - `Heartbeat` - Worker heartbeat

- **CoordinatorService**:
  - `GetMetrics` - System metrics
  - `HealthCheck` - Health check

#### Purpose and Use Cases

**1. Worker-to-Worker Communication** 🟦 (Planned/Future)
- **Why**: High performance, type-safe inter-worker communication
- **Use Case**: Worker coordination, task distribution, health checks between workers
- **Current Implementation**: Workers currently use Redis Streams for task distribution
- **Future Enhancement**: gRPC WorkerService is defined in proto but not yet integrated into workers
- **Benefits** (when implemented):
  - **Performance**: 5-10x faster than REST (binary protocol)
  - **Type Safety**: Protocol buffers ensure type safety
  - **Streaming**: Bidirectional streaming for real-time worker updates
  - **Code Generation**: Auto-generated client/server code
  - **Low Latency**: Critical for worker coordination
- **Note**: The `WorkerService` proto definition exists (`proto/taskqueue.proto`) with methods like `RegisterWorker`, `GetWorkerStatus`, `ListWorkers`, and `Heartbeat`, but workers currently communicate via Redis Streams broker

**2. Microservices Communication**
- **Why**: High performance, type-safe inter-service communication
- **Use Case**: Service-to-service calls, internal APIs
- **Benefits**:
  - **Performance**: 5-10x faster than REST (binary protocol)
  - **Type Safety**: Protocol buffers ensure type safety
  - **Streaming**: Bidirectional streaming for real-time data
  - **Code Generation**: Auto-generated client/server code

**3. High-Performance Applications**
- **Why**: Lower latency and higher throughput
- **Use Case**: High-frequency trading, real-time systems, IoT
- **Benefits**:
  - **Binary Protocol**: Smaller payload size
  - **HTTP/2**: Multiplexing, header compression
  - **Efficient Serialization**: Protocol buffers are compact
  - **Lower CPU Usage**: Less parsing overhead

**4. Mobile Applications (Native)**
- **Why**: Better performance and battery efficiency
- **Use Case**: Native mobile apps with high data transfer
- **Benefits**:
  - **Reduced Bandwidth**: Smaller payloads save data
  - **Faster Response**: Lower latency
  - **Battery Efficient**: Less CPU usage
  - **Streaming Support**: Real-time updates

**5. Real-Time Systems**
- **Why**: Bidirectional streaming support
- **Use Case**: Real-time task updates, live monitoring
- **Benefits**:
  - **Server Streaming**: Server can push updates
  - **Client Streaming**: Client can send continuous data
  - **Bidirectional**: Both directions simultaneously
  - **Low Latency**: Minimal overhead

**6. Polyglot Environments**
- **Why**: Language-agnostic protocol
- **Use Case**: Services written in different languages
- **Benefits**:
  - **Language Support**: Go, Java, Python, Node.js, etc.
  - **Code Generation**: Client code for any language
  - **Consistent Interface**: Same API across languages
  - **Versioning**: Protocol buffer versioning

**7. Internal APIs**
- **Why**: Not exposed to external clients
- **Use Case**: Internal service communication
- **Benefits**:
  - **Security**: Not exposed to internet
  - **Performance**: Optimized for internal use
  - **Type Safety**: Compile-time type checking
  - **Documentation**: Auto-generated from proto files

**Characteristics**:
- **Protocol**: HTTP/2
- **Format**: Protocol Buffers (binary)
- **Port**: 50051 (default)
- **Streaming**: Unary, Server, Client, Bidirectional
- **Reflection**: Service discovery enabled

**When to Choose gRPC**:
- ✅ **Worker-to-worker communication** (planned - proto defined, integration pending)
- ✅ Internal microservices communication
- ✅ High-performance requirements
- ✅ Real-time streaming needs
- ✅ Type-safe APIs
- ✅ Polyglot service architecture
- ❌ Browser-based clients (limited support)
- ❌ Public APIs (complexity for clients)

**Current Status**: 
- ✅ gRPC TaskService is implemented and working
- ⚠️ WorkerService proto is defined but not yet integrated into workers (workers currently use Redis Streams)
- 📝 Future enhancement: Integrate gRPC WorkerService for direct worker-to-worker communication

#### Why gRPC is NOT Used for Worker-to-Worker Communication

**Architectural Pattern Mismatch:**

The system uses a **Producer-Consumer pattern** with Redis Streams, not peer-to-peer communication:

```
API (Producer) → Redis Streams (Queue) → Workers (Consumers)
```

**Key Reasons:**

1. **Different Communication Patterns**
   - **gRPC is designed for**: Direct service-to-service calls, request-response, peer-to-peer
   - **Current system needs**: Message queue pattern, asynchronous distribution, decoupled consumers
   - **Workers are consumers, not peers**: They don't communicate with each other directly

2. **Redis Streams is Better Fit**
   - ✅ **Consumer Groups**: Automatic load balancing across workers
   - ✅ **Message Persistence**: Tasks survive restarts
   - ✅ **At-least-once Delivery**: Built-in acknowledgment mechanism
   - ✅ **Pending Message Tracking**: Failed tasks can be retried
   - ✅ **No Service Discovery**: Workers just connect to Redis

3. **Workers Don't Need Direct Communication**
   - Workers are independent consumers of the same queue
   - Redis handles task distribution automatically
   - Database handles state persistence (heartbeats, status)
   - No coordination between workers needed

4. **Simplicity**
   - Current design: Simple subscribe-and-process model
   - gRPC would require: Coordinator service, custom load balancing, service discovery
   - More moving parts = more failure points

**Current Implementation:**
```go
// Workers use Redis Streams
redisBroker, err := broker.NewRedisStreamBroker(...)
err := w.broker.Subscribe(w.ctx, "workers", consumerName, w.handleTask)

// Heartbeats go to database (not gRPC)
w.storage.RecordWorkerHeartbeat(ctx, &metricsSnapshot)
```

**When Would gRPC Make Sense?**
- Direct worker coordination needed (task handoffs, state sharing)
- Real-time worker status synchronization between peers
- High-frequency worker-to-worker updates
- Distributed consensus algorithms

**Conclusion**: Redis Streams is the correct choice for this architecture. gRPC would add complexity without benefits for the current task distribution model. The `WorkerService` proto exists for future enhancements but isn't needed for current requirements.

---

### GraphQL API

#### Where It's Used

**Primary Endpoint**: `http://localhost:8080/graphql`

**Playground**: `http://localhost:8080/graphql` (GET request)

**Available Queries**:
```graphql
query {
  task(id: "task-id") {
    id
    type
    status
    payload
    result
  }
  
  tasks(page: 1, pageSize: 10, status: "queued") {
    total
    page
    pageSize
    tasks {
      id
      type
      status
      createdAt
    }
  }
  
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
```

**Available Mutations**:
```graphql
mutation {
  submitTask(
    type: "image_resize"
    payload: "{\"source_url\":\"...\",\"width\":400,\"height\":300}"
    priority: 0
    maxRetries: 3
  ) {
    taskId
    status
    message
  }
}
```

#### Purpose and Use Cases

**1. Dashboard Integration** 🟧 ✅ VERIFIED
- **Why**: Fetch metrics and task status in a single query, reduce over-fetching
- **Use Case**: React dashboard (`DashboardGraphQL.jsx`) fetching system metrics, task lists, and status in one request
- **Implementation**: 
  - Uses Apollo Client (`@apollo/client`)
  - Queries `GET_METRICS` and `GET_TASKS` from `dashboard/src/graphql/queries.js`
  - Fetches: `totalTasks`, `queuedTasks`, `processingTasks`, `completedTasks`, `failedTasks`, `activeWorkers`, `taskTypeCounts`
  - Single query gets both metrics and task status
- **Benefits**:
  - **Single Request**: Get metrics + tasks + status in one query
  - **No Over-fetching**: Request only required fields
  - **Flexible Queries**: Client controls response shape
  - **Type Safety**: Strongly typed schema
  - **Efficient**: Reduces network round-trips for dashboard
- **Status**: ✅ Fully implemented and working

**2. Frontend Applications**
- **Why**: Fetch exactly the data needed, reduce over-fetching
- **Use Case**: React/Vue/Angular dashboards, single-page applications
- **Benefits**:
  - **Single Request**: Get all needed data in one query
  - **No Over-fetching**: Request only required fields
  - **Flexible Queries**: Client controls response shape
  - **Type Safety**: Strongly typed schema

**3. Mobile Applications**
- **Why**: Reduce network requests and data transfer
- **Use Case**: Mobile apps with limited bandwidth
- **Benefits**:
  - **Fewer Requests**: Single query for multiple resources
  - **Reduced Payload**: Only fetch needed fields
  - **Better Performance**: Less network overhead
  - **Battery Efficient**: Fewer network calls

**4. Complex Data Relationships**
- **Why**: Easily query related data in one request
- **Use Case**: Dashboards showing tasks, metrics, and user data
- **Benefits**:
  - **Nested Queries**: Get related data automatically
  - **Relationships**: Define data relationships in schema
  - **Flexible**: Adapt to UI requirements
  - **Efficient**: Server optimizes query execution

**5. Rapid Frontend Development**
- **Why**: Frontend developers can query exactly what they need
- **Use Case**: Fast iteration, changing UI requirements
- **Benefits**:
  - **Self-Documenting**: Schema serves as documentation
  - **Playground**: Interactive query testing
  - **No Backend Changes**: Frontend can adapt without API changes
  - **Type Safety**: Auto-generated TypeScript types

**6. API Aggregation**
- **Why**: Combine data from multiple sources
- **Use Case**: Unified API for multiple services
- **Benefits**:
  - **Single Endpoint**: One API for all queries
  - **Resolver Pattern**: Combine data from multiple sources
  - **Flexible**: Easy to add new data sources
  - **Efficient**: Server-side data aggregation

**7. Real-Time Subscriptions**
- **Why**: WebSocket-based subscriptions for live updates
- **Use Case**: Real-time task status updates, live metrics
- **Benefits**:
  - **Live Updates**: Push updates to clients
  - **Efficient**: Only send changed data
  - **Scalable**: GraphQL subscriptions scale well
  - **Flexible**: Subscribe to specific data changes

**Characteristics**:
- **Protocol**: HTTP/1.1, WebSocket (for subscriptions)
- **Format**: JSON
- **Query Language**: GraphQL query language
- **Schema**: Strongly typed schema
- **Port**: 8080 (same as REST)

**When to Choose GraphQL**:
- ✅ **Dashboard integration** (fetch metrics + status in one query)
- ✅ Frontend applications with complex data needs
- ✅ Mobile apps with bandwidth constraints
- ✅ Rapid frontend development
- ✅ Need for flexible queries
- ✅ Real-time subscriptions required
- ❌ Simple CRUD operations (REST is simpler)
- ❌ High-performance internal APIs (gRPC is better)

---

### Protocol Comparison

| Feature | REST | gRPC | GraphQL |
|---------|------|------|---------|
| **Protocol** | HTTP/1.1, HTTP/2 | HTTP/2 | HTTP/1.1, WebSocket |
| **Format** | JSON | Protocol Buffers | JSON |
| **Performance** | Good | Excellent | Good |
| **Type Safety** | Runtime | Compile-time | Schema-based |
| **Streaming** | No | Yes | Yes (Subscriptions) |
| **Browser Support** | Excellent | Limited | Excellent |
| **Caching** | HTTP Cache | Limited | Custom |
| **Learning Curve** | Low | Medium | Medium |
| **Use Case** | Public APIs, Web | Microservices | Frontend Apps |
| **Code Generation** | Manual | Auto | Auto (Types) |
| **Payload Size** | Larger | Smaller | Variable |

### Decision Matrix

**Choose REST when**:
- Building public APIs
- Need HTTP caching
- Simple CRUD operations
- Browser-based clients
- Third-party integrations
- Quick prototyping

**Choose gRPC when**:
- Internal microservices
- High-performance requirements
- Real-time streaming
- Type-safe communication
- Polyglot environments
- Mobile native apps

**Choose GraphQL when**:
- Frontend applications
- Complex data relationships
- Need flexible queries
- Real-time subscriptions
- Mobile apps (bandwidth concerns)
- Rapid frontend development

### Implementation Details

**REST API** (`cmd/api/main.go`):
- Implemented using Fiber framework
- Standard HTTP handlers
- JSON request/response
- JWT authentication
- Rate limiting middleware

**gRPC API** (`cmd/grpc-server/main.go`):
- Standalone gRPC server
- Protocol buffer definitions
- Service implementations
- Reflection enabled
- Streaming support

**GraphQL API** (`internal/graphql/`):
- Integrated with REST API server
- Schema-first approach
- Resolver pattern
- Playground for testing
- Subscription support (WebSocket)

### Best Practices

**REST**:
- Use proper HTTP methods (GET, POST, PUT, DELETE)
- Follow RESTful conventions
- Version APIs (`/api/v1/`)
- Use appropriate status codes
- Implement pagination for lists

**gRPC**:
- Use meaningful service and method names
- Version proto files
- Handle errors with status codes
- Use streaming for real-time data
- Enable reflection for development

**GraphQL**:
- Design schema carefully
- Use fragments for reusable queries
- Implement query complexity limits
- Use DataLoader for N+1 problems
- Document with descriptions

---

### 11. React Dashboard (`dashboard/`)

**Purpose**: Web-based management interface

**Features**:
- **REST API Integration**: Traditional API calls
- **GraphQL Integration**: Apollo Client
- **Real-time Updates**: WebSocket connections
- **Task Management**: Submit, view, delete tasks
- **Metrics Visualization**: Charts and graphs

**Components**:
- `Dashboard.jsx`: REST API interface
- `DashboardGraphQL.jsx`: GraphQL interface
- `TaskList.jsx`: Task display
- `MetricsCard.jsx`: Metrics visualization

---

## Technology Stack

### Backend
- **Go 1.24**: Programming language
- **Fiber v2**: Web framework (fast, Express-like)
- **PostgreSQL**: Primary database
- **Redis**: Message broker and cache
- **Prometheus**: Metrics collection
- **Grafana**: Metrics visualization
- **Jaeger**: Distributed tracing

### Frontend
- **React**: UI framework
- **Vite**: Build tool
- **Tailwind CSS**: Styling
- **Apollo Client**: GraphQL client

### Infrastructure
- **Docker**: Containerization
- **Docker Compose**: Local development
- **Kubernetes**: Production orchestration
- **Helm**: Kubernetes package manager

### Protocols
- **REST**: HTTP/JSON API
- **gRPC**: High-performance RPC
- **GraphQL**: Flexible query language
- **WebSocket**: Real-time updates

---

## Features

### Core Features
✅ **Multi-Protocol APIs**: REST, gRPC, GraphQL  
✅ **Distributed Workers**: Horizontal scaling  
✅ **Redis Streams**: Reliable message queue  
✅ **PostgreSQL**: Persistent storage  
✅ **JWT Authentication**: Secure access  
✅ **Rate Limiting**: Prevent abuse  
✅ **Circuit Breakers**: Fault tolerance  
✅ **Retry Logic**: Automatic recovery  
✅ **Prometheus Metrics**: Observability  
✅ **Grafana Dashboards**: Visualization  
✅ **WebSocket Support**: Real-time updates  
✅ **Graceful Shutdown**: Clean exits  

### Advanced Features
✅ **S3 Storage**: Cloud storage integration  
✅ **Redis Cluster**: High availability  
✅ **Distributed Cache**: Performance optimization  
✅ **Distributed Tracing**: Request tracking  
✅ **Connection Pooling**: Database efficiency  
✅ **Task Deduplication**: Prevent duplicates  
✅ **Health Checks**: System monitoring  
✅ **User Management**: Multi-user support  
✅ **Role-Based Access**: Admin/user roles  

---

## Conclusion

This Distributed Task Queue System provides a robust, scalable, and production-ready solution for asynchronous task processing. It combines modern technologies, best practices, and comprehensive observability to create a system that can handle real-world workloads while maintaining reliability and performance.

The architecture is designed for growth, with clear separation of concerns, horizontal scalability, and extensibility built-in. Whether you're processing images, running ETL jobs, or handling any asynchronous workload, this system provides the foundation you need.

