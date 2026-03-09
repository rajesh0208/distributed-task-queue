# Distributed Task Queue

A production-ready distributed image-processing task queue built with **Go**, **Redis Streams**, and **PostgreSQL**. Submit image jobs (resize, compress, watermark, filter, crop…) via REST, GraphQL, or gRPC. Workers pick them up concurrently, process them, and stream results back in real time through a React dashboard.

![Go](https://img.shields.io/badge/Go-1.25-00ADD8?logo=go&logoColor=white)
![Redis](https://img.shields.io/badge/Redis-7-DC382D?logo=redis&logoColor=white)
![PostgreSQL](https://img.shields.io/badge/PostgreSQL-15-4169E1?logo=postgresql&logoColor=white)
![License](https://img.shields.io/badge/license-MIT-22c55e)
![Tests](https://img.shields.io/badge/tests-34%20passing-22c55e)

---

## Table of Contents

- [Features](#features)
- [Architecture](#architecture)
- [Tech Stack](#tech-stack)
- [Prerequisites](#prerequisites)
- [Quick Start (Docker)](#quick-start-docker)
- [Running Locally (without Docker)](#running-locally-without-docker)
  - [1. Start Infrastructure](#1-start-infrastructure)
  - [2. Run the API Server](#2-run-the-api-server)
  - [3. Run the Worker](#3-run-the-worker)
  - [4. Run the Frontend](#4-run-the-frontend)
- [Environment Variables](#environment-variables)
- [API Reference](#api-reference)
  - [Authentication](#authentication)
  - [Tasks](#tasks)
  - [Batch Tasks](#batch-tasks)
  - [Task Types and Payloads](#task-types-and-payloads)
  - [Admin](#admin)
- [OAuth Setup](#oauth-setup)
- [Running Tests](#running-tests)
- [Debugging](#debugging)
  - [View Logs](#view-logs)
  - [Common Errors and Fixes](#common-errors-and-fixes)
  - [Inspect Distributed Traces](#inspect-distributed-traces)
  - [Inspect Metrics](#inspect-metrics)
  - [Inspect the Redis Stream](#inspect-the-redis-stream)
  - [Inspect the Database](#inspect-the-database)
- [Project Structure](#project-structure)
- [Contributing](#contributing)
- [License](#license)

---

## Features

- **7 image operations** — resize, compress, watermark, filter, crop, thumbnail, format-convert, responsive srcset
- **Batch submission** — fan out N images in one request; poll live `queued / processing / completed / failed` counts per batch
- **Redis Stream consumer groups** — N worker replicas each independently claim tasks, zero coordination required
- **Circuit breaker** — trips after 5 consecutive task failures, auto-recovers after 30 s
- **Exponential backoff retry** — failed tasks wait `retries²` seconds before re-queuing
- **Task cancellation** — two-layer: DB status + Redis signal checked before and during processing
- **OAuth 2.0** — Google and GitHub social login; links to existing accounts by email
- **Distributed tracing** — OTel spans linked from HTTP → Redis Stream → worker visible in Jaeger
- **Prometheus + Grafana** — task counters, processing-time histograms, circuit-breaker state gauge, worker heartbeats
- **REST + GraphQL + gRPC** — pick the protocol your client needs
- **React dashboard** — live task table, metrics cards, single-image submit, and **batch upload** (drop N images, upload all, fan out as one batch)
- **OAuth login UI** — Google and GitHub sign-in buttons on the login page; token handed back via redirect and stored automatically

---

## Architecture

```
                     ┌─────────────────────────────────────────┐
                     │              Client                      │
                     │  (Browser / curl / gRPC stub)            │
                     └────────────────┬────────────────────────┘
                                      │
               ┌──────────────────────▼──────────────────────┐
               │              API Server (Fiber)              │
               │  REST  /  GraphQL  /  WebSocket              │
               │                                              │
               │  ┌──────────┐  ┌──────────┐  ┌──────────┐  │
               │  │   Auth   │  │  Tasks   │  │  Batch   │  │
               │  │ JWT/OAuth│  │  CRUD    │  │  API     │  │
               │  └──────────┘  └────┬─────┘  └────┬─────┘  │
               └───────────────────── │──────────────│───────┘
                                      │  Publish     │
                                      ▼              ▼
                              ┌──────────────────────────┐
                              │      Redis 7 Streams      │
                              │   consumer group: workers │
                              └─────────────┬────────────┘
                           ┌────────────────┼────────────────┐
                           ▼                ▼                ▼
                      ┌────────┐       ┌────────┐       ┌────────┐
                      │Worker 1│       │Worker 2│       │Worker N│
                      │        │       │        │       │        │
                      │ Image  │       │ Image  │       │ Image  │
                      │Processor       │Processor       │Processor
                      └───┬────┘       └───┬────┘       └───┬────┘
                          └───────────────►▼◄───────────────┘
                                    ┌──────────┐
                                    │PostgreSQL│
                                    │  + Redis │
                                    │  cache   │
                                    └──────────┘
```

---

## Tech Stack

| Layer | Technology |
|-------|------------|
| Language | Go 1.25 |
| API framework | [Fiber v2](https://github.com/gofiber/fiber) |
| Message broker | Redis 7 Streams + consumer groups |
| Database | PostgreSQL 15 |
| Image processing | [imaging](https://github.com/disintegration/imaging), libwebp (CGO) |
| Auth | JWT HS256, bcrypt, OAuth 2.0 (Google + GitHub) |
| Tracing | OpenTelemetry + Jaeger |
| Metrics | Prometheus + Grafana |
| Frontend | React 18, Vite, Tailwind CSS |
| Container | Docker multi-stage, Docker Compose |
| Orchestration | Kubernetes + HPA |

---

## Prerequisites

| Tool | Version | Install |
|------|---------|---------|
| Docker | 24+ | [docs.docker.com](https://docs.docker.com/get-docker/) |
| Docker Compose | v2 | bundled with Docker Desktop |
| Go | 1.25+ | [go.dev/dl](https://go.dev/dl/) — *only for local dev* |
| Node.js | 18+ | [nodejs.org](https://nodejs.org/) — *only for frontend dev* |

---

## Quick Start (Docker)

> The fastest way — everything runs in containers.

```bash
# 1. Clone
git clone https://github.com/rajesh0208/distributed-task-queue.git
cd distributed-task-queue

# 2. Configure
cp .env.example .env
#    Edit .env — set POSTGRES_PASSWORD and JWT_SECRET at minimum

# 3. Build and start all services
docker compose up --build

# 4. (Optional) scale to more workers
docker compose up --scale worker=4
```

Open the services:

| Service | URL |
|---------|-----|
| React dashboard | http://localhost |
| REST API | http://localhost:8080 |
| Jaeger tracing UI | http://localhost:16686 |
| Prometheus | http://localhost:9090 |
| Grafana | http://localhost:3000 (admin / admin) |

To stop everything:

```bash
docker compose down

# To also delete volumes (wipes database and Redis data)
docker compose down -v
```

---

## Running Locally (without Docker)

Use this when you want hot-reload or to step through code in a debugger.

### 1. Start Infrastructure

Start only the backing services in Docker, leaving Go and Node running natively:

```bash
docker compose up redis postgres jaeger -d
```

Verify they are healthy:

```bash
docker compose ps
# redis and postgres should show "(healthy)"
```

### 2. Run the API Server

```bash
cd backend

export REDIS_ADDR=localhost:6379
export POSTGRES_DSN="postgres://taskqueue_user:yourpassword@localhost:5432/taskqueue?sslmode=disable"
export JWT_SECRET="dev-secret-min-32-chars-xxxxxxxxx"
export STORAGE_DIR=./storage/images
export APP_BASE_URL=http://localhost:8080
export LOG_LEVEL=debug
export JAEGER_ENDPOINT=http://localhost:14268/api/traces

go run ./cmd/api
# → API listening on :8080
```

To enable hot-reload install [air](https://github.com/cosmtrek/air):

```bash
go install github.com/cosmtrek/air@latest
air -c .air.toml   # or just: air
```

### 3. Run the Worker

Open a second terminal:

```bash
cd backend

export REDIS_ADDR=localhost:6379
export POSTGRES_DSN="postgres://taskqueue_user:yourpassword@localhost:5432/taskqueue?sslmode=disable"
export STORAGE_DIR=./storage/images
export BASE_URL=http://localhost:8080
export API_SERVICE_URL=http://localhost:8080
export WORKER_CONCURRENCY=4
export LOG_LEVEL=debug
export JAEGER_ENDPOINT=http://localhost:14268/api/traces

go run ./cmd/worker
# → worker-xxxxxxxx starting with concurrency=4
```

You can run multiple workers in separate terminals — they automatically join the same Redis consumer group and share the task load.

### 4. Run the Frontend

```bash
cd frontend
npm install
npm run dev
# → Vite dev server at http://localhost:5173
```

The Vite config proxies `/api` requests to `http://localhost:8080`, so no CORS issues during development.

**Login options available in the UI:**
- Email + password (register or sign in)
- **Continue with Google** / **Continue with GitHub** — clicking these redirects to the backend OAuth flow. The backend redirects back to the frontend with `?token=<jwt>` which is picked up automatically.

**Batch image upload:**
On the Submit Task card, toggle **Batch mode** on. Drop multiple images into the drop zone, click **Upload All**, then **Submit Batch**. Each image becomes an individual task in a shared batch ID you can poll with `GET /api/v1/tasks/batch/:id`.

To build for production:

```bash
npm run build
# output → frontend/dist/
```

---

## Environment Variables

Copy `.env.example` to `.env` and fill in the values.

| Variable | Default | Required | Description |
|----------|---------|----------|-------------|
| `POSTGRES_PASSWORD` | — | ✅ | Password for the `taskqueue_user` DB user |
| `JWT_SECRET` | — | ✅ | HS256 signing key — use at least 32 random chars |
| `REDIS_ADDR` | `localhost:6379` | — | Redis host:port |
| `POSTGRES_DSN` | — | ✅ (local) | Full PostgreSQL connection string |
| `WORKER_CONCURRENCY` | `runtime.NumCPU()` | — | Goroutines per worker process |
| `STORAGE_DIR` | `./storage/images` | — | Where processed images are written |
| `BASE_URL` / `API_SERVICE_URL` | `http://localhost:8080` | — | URL workers use to download uploaded images |
| `APP_BASE_URL` | `http://localhost:8080` | — | Public API URL (used in OAuth redirect URIs) |
| `LOG_LEVEL` | `info` | — | `debug` / `info` / `warn` / `error` |
| `JAEGER_ENDPOINT` | `http://jaeger:14268/api/traces` | — | OTel collector URL (non-fatal if unreachable) |
| `GOOGLE_CLIENT_ID` | — | — | Google OAuth app ID (leave blank to disable) |
| `GOOGLE_CLIENT_SECRET` | — | — | Google OAuth secret |
| `GITHUB_CLIENT_ID` | — | — | GitHub OAuth app ID (leave blank to disable) |
| `GITHUB_CLIENT_SECRET` | — | — | GitHub OAuth secret |
| `GRAFANA_PASSWORD` | `admin` | — | Grafana admin password |

---

## API Reference

All endpoints are under `/api/v1`. Protected routes require `Authorization: Bearer <token>`.

### Authentication

```bash
BASE=http://localhost:8080/api/v1

# Register
curl -s -X POST $BASE/auth/register \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","email":"alice@example.com","password":"secret123"}'

# Login → copy the token
TOKEN=$(curl -s -X POST $BASE/auth/login \
  -H "Content-Type: application/json" \
  -d '{"username":"alice","password":"secret123"}' | jq -r .token)

# Logout (blacklists the token in Redis)
curl -s -X POST $BASE/auth/logout \
  -H "Authorization: Bearer $TOKEN"
```

OAuth social login (browser flow):

```
GET /api/v1/auth/oauth/google   → redirects to Google consent screen
GET /api/v1/auth/oauth/github   → redirects to GitHub consent screen
# Callback returns {"token":"<jwt>"}
```

### Tasks

```bash
# Submit a task
curl -s -X POST $BASE/tasks \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "resize",
    "payload": {
      "source_url": "https://upload.wikimedia.org/wikipedia/commons/thumb/4/47/PNG_transparency_demonstration_1.png/280px-PNG_transparency_demonstration_1.png",
      "width": 400,
      "height": 300,
      "maintain_aspect": true,
      "output_format": "webp"
    },
    "priority": 1,
    "max_retries": 3
  }'

# List tasks (supports ?status=queued|processing|completed|failed&limit=20&offset=0)
curl -s $BASE/tasks -H "Authorization: Bearer $TOKEN" | jq .

# Get a single task
curl -s $BASE/tasks/<task_id> -H "Authorization: Bearer $TOKEN" | jq .

# Cancel a task
curl -s -X POST $BASE/tasks/<task_id>/cancel \
  -H "Authorization: Bearer $TOKEN"

# Upload a local image then process it
curl -s -X POST $BASE/tasks/upload \
  -H "Authorization: Bearer $TOKEN" \
  -F "image=@/path/to/photo.jpg" \
  -F 'payload={"type":"compress","payload":{"quality":75}}'
```

### Batch Tasks

```bash
# Submit a batch (max 500 images)
BATCH=$(curl -s -X POST $BASE/tasks/batch \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "resize",
    "images": [
      {"source_url":"https://picsum.photos/800/600","width":400,"height":300,"maintain_aspect":true},
      {"source_url":"https://picsum.photos/1200/800","width":400,"height":300,"maintain_aspect":true},
      {"source_url":"https://picsum.photos/640/480","width":400,"height":300,"maintain_aspect":true}
    ],
    "priority": 0,
    "max_retries": 3
  }')
BATCH_ID=$(echo $BATCH | jq -r .batch_id)

# Poll batch progress
curl -s $BASE/batches/$BATCH_ID -H "Authorization: Bearer $TOKEN" | jq .
# {
#   "batch_id": "...",
#   "status": "processing",
#   "total": 3,
#   "queued": 1,
#   "processing": 1,
#   "completed": 1,
#   "failed": 0
# }
```

### Task Types and Payloads

| Type | Required fields | Optional fields |
|------|----------------|-----------------|
| `resize` | `source_url`, `width`, `height` | `maintain_aspect` (bool), `output_format` |
| `compress` | `source_url` | `quality` (1–100, default 85), `target_size_bytes` |
| `watermark` | `source_url`, `text` | `position` (`bottom-right`, `center`, etc.), `opacity` |
| `filter` | `source_url`, `filter_type` | `params` (map of filter-specific settings) |
| `crop` | `source_url`, `x`, `y`, `width`, `height` | — |
| `thumbnail` | `source_url`, `size` | `output_format` |
| `format_convert` | `source_url`, `output_format` | `quality` |
| `responsive` | `source_url`, `widths` (array) | `output_format` |

Filter types: `grayscale`, `blur` (`sigma`), `sharpen` (`amount`), `sepia`, `brightness` (`percentage`), `contrast` (`percentage`), `saturation` (`level`).

### Admin

```bash
# System metrics (requires admin role)
curl -s $BASE/admin/metrics -H "Authorization: Bearer $TOKEN" | jq .

# Worker health
curl -s $BASE/admin/workers -H "Authorization: Bearer $TOKEN" | jq .
```

---

## OAuth Setup

### Google

1. Go to [Google Cloud Console](https://console.cloud.google.com/) → **APIs & Services → Credentials**
2. Click **Create Credentials → OAuth 2.0 Client ID** → Application type: **Web application**
3. Under **Authorized redirect URIs** add:
   ```
   http://localhost:8080/api/v1/auth/oauth/google/callback
   ```
4. Copy the **Client ID** and **Client Secret** into `.env`:
   ```env
   GOOGLE_CLIENT_ID=your-client-id.apps.googleusercontent.com
   GOOGLE_CLIENT_SECRET=your-client-secret
   ```

### GitHub

1. Go to **GitHub → Settings → Developer Settings → OAuth Apps → New OAuth App**
2. Set **Authorization callback URL** to:
   ```
   http://localhost:8080/api/v1/auth/oauth/github/callback
   ```
3. Copy **Client ID** and generate a **Client Secret**, then add to `.env`:
   ```env
   GITHUB_CLIENT_ID=your-github-client-id
   GITHUB_CLIENT_SECRET=your-github-client-secret
   ```

Restart the API server after changing `.env`.

---

## Running Tests

```bash
cd backend

# Run all tests with verbose output
go test ./internal/api/... -v

# Run a specific test
go test ./internal/api/... -run TestSubmitTask -v

# Run with race detector (recommended before PRs)
go test ./internal/api/... -race -v

# Run all packages
go test ./...
```

The test suite (34 tests) runs entirely in-memory — no Redis or PostgreSQL required. It uses a `fakeStorage` and an in-process HTTP server.

---

## Debugging

### View Logs

**Docker:**

```bash
# All services
docker compose logs -f

# API only
docker compose logs -f api

# Worker only
docker compose logs -f worker

# Last 100 lines from worker
docker compose logs --tail=100 worker
```

**Local (debug level):**

```bash
LOG_LEVEL=debug go run ./cmd/api
```

Log output is structured JSON. To make it readable locally:

```bash
LOG_LEVEL=debug go run ./cmd/api 2>&1 | jq .
```

### Common Errors and Fixes

| Error | Likely cause | Fix |
|-------|-------------|-----|
| `failed to connect to PostgreSQL` | DB not running or wrong DSN | Run `docker compose up postgres -d` and verify `POSTGRES_DSN` |
| `failed to connect to Redis` | Redis not running | Run `docker compose up redis -d` |
| `invalid or expired token` | JWT_SECRET changed or token expired | Log in again to get a fresh token |
| `image too large` | Source image exceeds 5000×5000 px | Use a smaller test image |
| `unsupported output format` | Unknown format string | Use `jpeg`, `png`, or `webp` |
| `circuit breaker open` | 5+ consecutive task failures | Check worker logs for the underlying error; CB auto-resets in 30 s |
| `user not found` (OAuth) | Email not returned by provider | Make sure email scope is granted; GitHub users may need to set a public email |
| `BUSYGROUP Consumer Group name already exists` | Normal on worker restart | Harmless — safely ignored by the broker |
| Port `8080` already in use | Another process | `lsof -i :8080` to find and kill it |
| `libwebp` not found (CGO build) | Missing system library | `brew install webp` (macOS) or `apt install libwebp-dev` (Linux) |

### Inspect Distributed Traces

1. Open **Jaeger UI** at http://localhost:16686
2. Select service `task-queue-api` or `task-queue-worker`
3. Click **Find Traces**
4. Click any trace to see the full span from HTTP request → Redis publish → worker processing

Traces are only emitted when `JAEGER_ENDPOINT` is reachable. If Jaeger is down the API/worker continue normally (spans are dropped silently).

### Inspect Metrics

Open **Prometheus** at http://localhost:9090 and try these queries:

```promql
# Tasks processed per type
sum by (task_type) (worker_tasks_processed_total)

# Average processing time (seconds)
rate(worker_task_processing_duration_seconds_sum[5m])
  / rate(worker_task_processing_duration_seconds_count[5m])

# Tasks currently in progress
sum(worker_tasks_in_progress)

# Circuit breaker state (0=closed, 1=half-open, 2=open)
circuit_breaker_state
```

Or open **Grafana** at http://localhost:3000 (admin / admin) for pre-built dashboards.

### Inspect the Redis Stream

```bash
# Connect to the Redis CLI inside Docker
docker compose exec redis redis-cli

# See stream length
XLEN task-queue

# See last 5 messages
XREVRANGE task-queue + - COUNT 5

# See consumer group lag
XPENDING task-queue workers - + 10

# See which consumers are active
XINFO CONSUMERS task-queue workers
```

### Inspect the Database

```bash
# Connect to psql inside Docker
docker compose exec postgres psql -U taskqueue_user -d taskqueue

# Check task status distribution
SELECT status, COUNT(*) FROM tasks GROUP BY status;

# Check recent tasks
SELECT id, type, status, created_at, processing_time FROM tasks
ORDER BY created_at DESC LIMIT 10;

# Check batch progress
SELECT b.id, b.total,
  COUNT(*) FILTER (WHERE t.status='completed') AS done,
  COUNT(*) FILTER (WHERE t.status='failed') AS failed
FROM batches b LEFT JOIN tasks t ON t.batch_id = b.id
GROUP BY b.id, b.total
ORDER BY b.created_at DESC;

# Check active workers
SELECT worker_id, status, last_heartbeat, tasks_processed
FROM workers ORDER BY last_heartbeat DESC;
```

---

## Project Structure

```
distributed-task-queue/
├── backend/
│   ├── cmd/
│   │   ├── api/                  # REST API + GraphQL + WebSocket entry point
│   │   ├── worker/               # Task consumer entry point
│   │   └── grpc-server/          # Optional gRPC coordinator
│   ├── internal/
│   │   ├── models/               # Shared data types (Task, Batch, User, …)
│   │   ├── broker/               # Redis Stream producer + consumer group
│   │   ├── storage/              # PostgreSQL + Redis write-through cache
│   │   ├── processor/            # Image processing handlers (7 operations)
│   │   ├── security/             # JWT middleware, bcrypt, rate limiter
│   │   ├── oauth/                # Google + GitHub OAuth 2.0 client
│   │   ├── tracing/              # OpenTelemetry setup + map carrier helpers
│   │   ├── logging/              # slog JSON logger init
│   │   ├── monitoring/           # Prometheus metric registrations + middleware
│   │   ├── reliability/          # Circuit breaker, retry, deduplication, health
│   │   ├── graphql/              # gqlgen schema, resolvers, subscriptions
│   │   ├── grpc/                 # gRPC server/client implementations
│   │   ├── cache/                # Distributed cache abstraction
│   │   ├── database/             # Connection pool helpers
│   │   └── api/                  # Integration tests
│   ├── docker/
│   │   ├── Dockerfile.api        # Multi-stage build for API binary
│   │   └── Dockerfile.worker     # Multi-stage build for worker binary
│   ├── configs/
│   │   ├── prometheus.yml        # Scrape config
│   │   ├── alerts.yml            # Alerting rules
│   │   └── alertmanager.yml      # Alert routing
│   ├── migrations/               # SQL schema migrations (up + down)
│   ├── proto/                    # Protobuf definitions + generated Go code
│   ├── k8s/                      # Kubernetes Deployments, HPA, Ingress
│   ├── scripts/                  # setup.sh, migrate-db.sh, test helpers
│   └── Makefile
├── frontend/
│   ├── src/
│   │   ├── components/           # Dashboard, TaskList, SubmitCard (single + batch), Login (OAuth)
│   │   ├── api/                  # REST API client — axios, createTask, createBatch, uploadFile
│   │   └── graphql/              # Apollo client, queries, mutations
│   ├── Dockerfile                # nginx serving built assets
│   └── vite.config.js
├── docs/
│   └── DOCUMENTATION.md          # Full API reference + architecture notes
├── docker-compose.yml
├── .env.example
└── README.md
```

---

## Contributing

Contributions, bug reports, and feature requests are welcome.

### Getting started

```bash
# Fork the repo on GitHub, then:
git clone https://github.com/<your-username>/distributed-task-queue.git
cd distributed-task-queue

# Create a feature branch
git checkout -b feat/my-feature

# Make changes, then run tests
cd backend && go test ./... -race

# Commit using conventional commits
git commit -m "feat: add thumbnail strip operation"

# Push and open a PR against main
git push origin feat/my-feature
```

### Commit message convention

```
feat:     new feature
fix:      bug fix
docs:     documentation only
refactor: code change that doesn't add a feature or fix a bug
test:     add or update tests
chore:    build, CI, dependency updates
```

### Code style

```bash
cd backend

# Format
gofmt -w ./...

# Lint (install golangci-lint first)
golangci-lint run

# Vet
go vet ./...
```

Please make sure all 34 tests pass (`go test ./... -race`) before submitting a PR.

---

## License

MIT — use freely, attribution appreciated.

---

> Built as a portfolio project to demonstrate distributed systems design in Go.
> Questions or ideas? Open an [issue](https://github.com/rajesh0208/distributed-task-queue/issues).
