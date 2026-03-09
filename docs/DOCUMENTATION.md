# Distributed Task Queue — Complete Technical Documentation

> **Stack**: Go 1.25 · GoFiber v2 · PostgreSQL 15 · Redis 7 · React 18 · Docker · Kubernetes
> **Purpose**: Production-grade async image processing platform with horizontal scaling, full observability, and fault tolerance.
> **Recent additions**: OAuth 2.0 (Google + GitHub), batch task submission, OpenTelemetry distributed tracing, structured JSON logging, task cancellation.

---

## Table of Contents

1. [System Overview](#1-system-overview)
2. [Architecture Diagram](#2-architecture-diagram)
3. [Component Deep-Dive](#3-component-deep-dive)
   - 3.1 [API Server](#31-api-server)
   - 3.2 [Worker](#32-worker)
   - 3.3 [Message Broker — Redis Streams](#33-message-broker--redis-streams)
   - 3.4 [Storage Layer — PostgreSQL + Redis Cache](#34-storage-layer--postgresql--redis-cache)
   - 3.5 [Image Processor](#35-image-processor)
   - 3.6 [Security](#36-security)
   - 3.7 [Reliability Patterns](#37-reliability-patterns)
   - 3.8 [Observability](#38-observability)
   - 3.9 [Optional Interfaces — GraphQL & gRPC](#39-optional-interfaces--graphql--grpc)
   - 3.10 [Frontend](#310-frontend)
4. [Data Flow Walkthroughs](#4-data-flow-walkthroughs)
   - 4.1 [Submitting a Task (REST)](#41-submitting-a-task-rest)
   - 4.2 [Worker Processing a Task](#42-worker-processing-a-task)
   - 4.3 [Task Failure and Retry](#43-task-failure-and-retry)
   - 4.4 [Authentication Flow](#44-authentication-flow)
   - 4.5 [File Upload Flow](#45-file-upload-flow)
5. [Database Schema](#5-database-schema)
6. [API Reference](#6-api-reference)
7. [Image Processing Operations](#7-image-processing-operations)
8. [Configuration Reference](#8-configuration-reference)
9. [Infrastructure & Deployment](#9-infrastructure--deployment)
10. [Security Model](#10-security-model)
11. [Monitoring & Alerting](#11-monitoring--alerting)
12. [Project File Structure](#12-project-file-structure)

---

## 1. System Overview

This system solves a common real-world problem: **image processing is slow and resource-intensive**. Doing it synchronously in an HTTP request means the user waits, the server blocks threads, and a spike in traffic can crash the service.

The solution is a **distributed task queue**:

1. The client submits an image processing request and immediately gets a task ID back.
2. The API server publishes the task to a **Redis Stream**.
3. One or more **Worker** processes consume tasks from the stream and run the image operation.
4. The result is stored in **PostgreSQL** and the client can poll or receive a **WebSocket push** when done.

This decouples request handling from computation, enabling:
- **Horizontal scaling** — add more workers to handle more load
- **Fault tolerance** — failed tasks retry automatically
- **Backpressure** — tasks queue up safely during traffic spikes
- **Observability** — every task is tracked from submission to completion

---

## 2. Architecture Diagram

```
┌─────────────────────────────────────────────────────────────────────────────┐
│                           CLIENT LAYER                                       │
│                                                                               │
│   Browser (React)           External API clients        gRPC clients          │
│   http://localhost:80       REST / GraphQL              port 50051            │
└──────────────┬──────────────────────┬────────────────────────┬───────────────┘
               │ HTTP/WS              │ HTTP                   │ gRPC
               ▼                     ▼                         ▼
┌──────────────────────────────────────────────────────────────────────────────┐
│                         API SERVER  :8080                                     │
│                                                                               │
│  ┌─────────────┐  ┌──────────────┐  ┌─────────────┐  ┌──────────────────┐   │
│  │ Auth        │  │ Rate Limiter │  │ REST Routes │  │ WebSocket Hub    │   │
│  │ JWT / RBAC  │  │ Redis ZSET   │  │ /api/v1/*   │  │ Push task events │   │
│  └─────────────┘  └──────────────┘  └──────┬──────┘  └──────────────────┘   │
│                                            │                                  │
│                                     ┌──────▼──────┐                          │
│                                     │  Validator  │                          │
│                                     │  URL / SSRF │                          │
│                                     └──────┬──────┘                          │
└────────────────────────────────────────────┼─────────────────────────────────┘
               │ writes task                 │ publishes to stream
               ▼                             ▼
┌──────────────────────┐     ┌─────────────────────────────────────────────────┐
│   POSTGRESQL  :5432  │     │              REDIS  :6379                        │
│                      │     │                                                   │
│  ┌────────────────┐  │     │  DB 0 ─ JWT Blacklist + Rate Limit ZSETs         │
│  │ tasks          │  │     │  DB 1 ─ Task Cache (TTL 30s active / 10m done)   │
│  │ users          │  │     │  DB 2 ─ Distributed Cache                        │
│  │ workers        │  │     │                                                   │
│  └────────────────┘  │     │  Stream: task-queue                              │
│                      │     │  Group:  workers                                  │
│  Source of truth     │     │  ┌────────────────────────────────────────┐      │
│  for all task state  │     │  │ msg-1  msg-2  msg-3  msg-4  msg-5 ...  │      │
└──────────────────────┘     │  └──────────────────┬─────────────────────┘      │
         ▲                   │                      │                            │
         │                   └──────────────────────┼────────────────────────────┘
         │                                          │ XREADGROUP
         │                           ┌──────────────┴──────────────────────────────┐
         │                           │            WORKER POOL                       │
         │                           │                                              │
         │                           │  ┌────────────┐    ┌────────────┐           │
         │                           │  │  Worker 1  │    │  Worker 2  │   ...     │
         │                           │  │            │    │            │           │
         │                           │  │ ┌────────┐ │    │ ┌────────┐ │           │
         │                           │  │ │Circuit │ │    │ │Circuit │ │           │
         │                           │  │ │Breaker │ │    │ │Breaker │ │           │
         │                           │  │ └────┬───┘ │    │ └────┬───┘ │           │
         │                           │  │      │     │    │      │     │           │
         │                           │  │ ┌────▼───┐ │    │ ┌────▼───┐ │           │
         │                           │  │ │Image   │ │    │ │Image   │ │           │
         │                           │  │ │Processor│ │    │ │Processor│ │          │
         │                           │  │ └────────┘ │    │ └────────┘ │           │
         │                           │  └────────────┘    └────────────┘           │
         │                           │  Heartbeat every 10s ──────────────────►   │
         └───────────────────────────┤  Storage updates (status, result, error)    │
                                     └─────────────────────────────────────────────┘
                                                          │
                                         ┌────────────────▼───────────────┐
                                         │      OBSERVABILITY STACK        │
                                         │                                  │
                                         │  Prometheus :9090               │
                                         │  ┌──────────────────────────┐   │
                                         │  │ /metrics (Fiber handler) │   │
                                         │  └────────────┬─────────────┘   │
                                         │               │ scrapes          │
                                         │  Grafana :3000│                  │
                                         │  ┌────────────▼─────────────┐   │
                                         │  │ Dashboards + Alerts      │   │
                                         │  └──────────────────────────┘   │
                                         │                                  │
                                         │  Optional: Jaeger (tracing)      │
                                         └──────────────────────────────────┘
```

---

## 3. Component Deep-Dive

### 3.1 API Server

**File**: `backend/cmd/api/main.go`

The API server is the front door to the system. It handles all client requests, enforces authentication, validates input, and hands off work to the broker. It does **zero image processing itself** — its only job is accepting requests, persisting tasks, and publishing them.

#### Why GoFiber?

GoFiber is built on Fasthttp, which is faster than Go's standard `net/http` for high-concurrency scenarios. It provides familiar Express.js-style routing and excellent middleware support.

#### Core Struct

```go
type APIServer struct {
    app         *fiber.App       // HTTP router
    broker      broker.Broker    // Publishes tasks to Redis Streams
    storage     storage.Storage  // Reads/writes PostgreSQL + Redis cache
    authService *security.AuthService  // JWT generation + validation
    rateLimiter *security.RateLimiter  // Sliding-window rate limiting
    wsHub       *WebSocketHub    // Real-time push to browser clients
    redisClient *redis.Client    // Used for health checks
}
```

Each field has a single responsibility. The broker knows nothing about PostgreSQL; the storage layer knows nothing about Redis Streams; the auth service knows nothing about routing.

#### Route Structure

```
Public routes (no auth):
  POST /api/v1/auth/register
  POST /api/v1/auth/login
  GET  /api/v1/health

Protected routes (JWT required):
  POST /api/v1/auth/logout
  GET  /api/v1/auth/me
  PUT  /api/v1/users/me
  POST /api/v1/upload
  POST /api/v1/tasks
  GET  /api/v1/tasks
  GET  /api/v1/tasks/:id
  DEL  /api/v1/tasks/:id
  GET  /api/v1/ws              ← WebSocket
  GET  /api/v1/metrics/system

Admin-only routes (role=admin):
  GET  /api/v1/users
  GET  /api/v1/users/:id
  PUT  /api/v1/users/:id
  DEL  /api/v1/users/:id

Unguarded (Prometheus scrape):
  GET  /metrics
```

#### WebSocket Hub

The hub maintains a set of connected browser clients and broadcasts task state changes as they happen. When a task moves from `queued → processing → completed`, the API server calls `broadcastTaskUpdate(task)`, which writes a JSON message to all connected WebSocket clients.

```
Browser ──WS connect──► Hub registers client
Worker completes task ──► API updates DB ──► broadcastTaskUpdate ──► Hub fans out to all WS clients
```

This removes the need for the browser to constantly poll — it only polls as a fallback.

#### Task Ownership Enforcement

Every task carries a `user_id` field. When a non-admin user requests a task, the API checks:

```
task.UserID != requestingUserID  AND  user role != "admin"  →  403 Forbidden
```

Admins can see all tasks; regular users can only see their own.

---

### 3.2 Worker

**File**: `backend/cmd/worker/main.go`

The worker is the engine that actually processes tasks. Multiple worker processes can run simultaneously; they all compete for messages from the same Redis Stream consumer group.

#### Concurrency Model

```
Worker process
│
├── cleanupLoop goroutine      (1 goroutine) — removes old files every 1 hour
├── heartbeatLoop goroutine    (1 goroutine) — pings DB every 10 seconds
└── processLoop goroutines     (N goroutines, default = CPU count)
    ├── goroutine 0 ─── XREADGROUP ──► process task ──► XACK
    ├── goroutine 1 ─── XREADGROUP ──► process task ──► XACK
    └── goroutine N ─── XREADGROUP ──► process task ──► XACK
```

Each `processLoop` goroutine independently reads from the Redis stream. Redis Consumer Groups distribute messages so no two goroutines (across any worker process) handle the same message.

#### Task Processing Sequence

```
1. XREADGROUP — get next message from stream (blocks 2s if empty)
2. Unmarshal task JSON
3. Update task status → "processing" in PostgreSQL
4. Call circuit breaker Execute()
   ├── Circuit OPEN  → requeue task, continue (don't fail the message)
   └── Circuit CLOSED/HALFOPEN → call imageProcessor.ProcessTask(ctx, task)
       ├── SUCCESS → update task status → "completed", store result, XACK
       └── FAILURE → handleTaskFailure()
           ├��─ retries < maxRetries → update status → "retrying", requeue with delay
           └── retries >= maxRetries → update status → "failed", store error, XACK
```

#### Why "Always Acknowledge"?

Redis Streams retain unacknowledged messages in a pending list. If a worker crashes mid-task, the message stays pending. When the worker restarts (or another worker runs), the `Subscribe()` method first claims messages that have been pending for more than 30 seconds:

```go
claimed, _ := b.client.XClaim(ctx, &redis.XClaimArgs{
    MinIdle: 30 * time.Second,  // only claim if idle for 30+ seconds
    ...
})
```

This ensures no task is permanently lost due to worker crashes.

Once a task is handled (even if it fails), we always `XACK` to prevent the pending list from growing indefinitely. Retry logic is managed by the **database** (retry counter), not by leaving messages in the stream.

#### Circuit Breaker in the Worker

The circuit breaker wraps every `ProcessTask()` call:

```
Normal operation (CLOSED):
  Task fails 5 times consecutively → circuit OPENS

Circuit OPEN (30 second window):
  All task processing attempts are rejected immediately
  Tasks are requeued to the stream without being processed
  → Prevents a broken external service from exhausting all workers

After 30 seconds → circuit goes HALF-OPEN:
  Up to 3 test requests are allowed through
  If they succeed → circuit CLOSES (back to normal)
  If they fail → circuit OPENS again
```

This protects the system if, for example, an external image URL service becomes unavailable.

#### Heartbeat

Every 10 seconds, each worker UPSERTs its metrics to the `workers` table:

```
worker_id, status, tasks_processed, tasks_failed, last_heartbeat, active_goroutines
```

The API's `/api/v1/metrics/system` endpoint reads this table. If a worker's `last_heartbeat` is more than 30 seconds old, it's considered offline.

---

### 3.3 Message Broker — Redis Streams

**Files**: `backend/internal/broker/redis_stream.go`, `redis_cluster.go`

#### Why Redis Streams instead of a simple queue?

A simple list (LPUSH/BRPOP) loses messages if the worker crashes after dequeuing but before completing the task. Redis Streams provide:

- **Persistence**: Messages survive Redis restarts
- **Consumer Groups**: Multiple workers share the load without duplicating work
- **Pending Entries List (PEL)**: Tracks which messages have been delivered but not yet acknowledged
- **Message claiming**: A new worker can claim messages stuck on a crashed worker

#### Stream Layout

```
Stream key: "task-queue"
Consumer Group: "workers"

Message format:
  ID: auto-generated (Unix timestamp + sequence, e.g. "1720000000000-0")
  Fields:
    task     → JSON-encoded models.Task
    priority → integer priority value
```

#### Publish Flow

```go
func (b *RedisStreamBroker) Publish(ctx context.Context, task *models.Task) error {
    taskJSON, _ := json.Marshal(task)
    b.client.XAdd(ctx, &redis.XAddArgs{
        Stream: b.streamKey,
        ID:     "*",           // auto-generate ID
        Values: map[string]interface{}{
            "task":     taskJSON,
            "priority": task.Priority,
        },
    })
}
```

`ID: "*"` tells Redis to generate the message ID automatically based on the current timestamp.

#### Subscribe Flow (Pending Message Reclaim)

```
On each iteration:
  1. Check for pending messages idle > 30s (from crashed workers)
     If found → XClaim → process → XACK
  2. Read new messages with XREADGROUP
     Block 2s if stream is empty (avoids CPU spin)
     Process each message
     Always XACK after processing
```

#### Redis Cluster Variant

`redis_cluster.go` provides the same interface using `redis.ClusterClient` for production deployments where Redis is sharded across multiple nodes for higher availability. The `Broker` interface ensures the rest of the codebase is unaware of which variant is in use.

---

### 3.4 Storage Layer — PostgreSQL + Redis Cache

**File**: `backend/internal/storage/storage.go`

The storage layer is the **source of truth** for all task and user state. PostgreSQL is the authoritative database; Redis is a speed layer in front of it.

#### Why a Cache Layer?

Task status polling is a hot path — browsers poll every 5 seconds. Without caching, this would hit PostgreSQL on every request. The cache trades slight staleness (up to 30 seconds) for significantly lower database load.

#### Cache Strategy: Write-Through

```
CreateTask:
  1. INSERT into PostgreSQL          ← authoritative write
  2. SET "task:{id}" in Redis DB 1   ← warm the cache (30s TTL if active)

GetTask:
  1. GET "task:{id}" from Redis      ← fast path (cache hit)
     → return immediately if found
  2. SELECT from PostgreSQL          ← slow path (cache miss)
  3. SET "task:{id}" in Redis        ← populate cache for next time

UpdateTask:
  1. UPDATE in PostgreSQL
  2. SET "task:{id}" in Redis        ← update cache in place

DeleteTask:
  1. DELETE from PostgreSQL
  2. DEL "task:{id}" from Redis      ← invalidate cache
```

#### TTL Differentiation

```go
// Active tasks change frequently — short TTL
if task.Status == StatusQueued || task.Status == StatusProcessing {
    ttl = 30 * time.Second
} else {
    // Completed/failed tasks won't change — longer TTL
    ttl = 10 * time.Minute
}
```

#### Storage Interface

```go
type Storage interface {
    CreateTask(ctx, *Task) error
    UpdateTask(ctx, *Task) error
    GetTask(ctx, id string) (*Task, error)
    ListTasks(ctx, status, userID string, limit, offset int) ([]*Task, int64, error)
    DeleteTask(ctx, id string) error

    CreateUser(ctx, *User) error
    GetUserByID(ctx, id string) (*User, error)
    GetUserByEmail(ctx, email string) (*User, error)
    GetUserByUsername(ctx, username string) (*User, error)
    GetUserByAPIKey(ctx, key string) (*User, error)
    UpdateUser(ctx, *User) error
    ListUsers(ctx, limit, offset int) ([]*User, int64, error)

    GetMetrics(ctx) (*SystemMetrics, error)
    RecordWorkerHeartbeat(ctx, *WorkerMetrics) error
    GetDB() *sqlx.DB
    Close() error
}
```

The interface means the API and Worker never directly touch database drivers — they work through this contract. This makes testing and swapping backends straightforward.

#### Pagination with Window Functions

`ListTasks` uses a SQL window function to return the total count in a single query instead of two:

```sql
SELECT *, COUNT(*) OVER() as total_count
FROM tasks
WHERE status = $1 AND user_id = $2
ORDER BY created_at DESC
LIMIT $3 OFFSET $4
```

`COUNT(*) OVER()` computes the total count across all rows without a separate `COUNT(*)` query.

#### Redis DB Isolation

```
Redis DB 0  — JWT blacklist + rate limiting ZSETs  (auth/security)
Redis DB 1  — Task cache                            (storage layer)
Redis DB 2  — General distributed cache            (cache package)
```

Separate DBs prevent key collisions between different concerns. The broker uses DB 0's default stream key space which doesn't conflict with blacklist/ratelimit keys because those use prefixed namespaces (`blacklist:*`, `ratelimit:*`).

---

### 3.5 Image Processor

**File**: `backend/internal/processor/image_processor.go`

The image processor is the core business logic of the system. It handles eight distinct image operations.

#### Processor Struct

```go
type ImageProcessor struct {
    storageDir   string       // where to save output files
    baseURL      string       // base URL to serve files (e.g. http://api:8080/images)
    maxImageSize int64        // 10 MB limit on input images
    maxWidth     int          // 10,000 px limit
    maxHeight    int          // 10,000 px limit
    httpClient   *http.Client // reused client with connection pooling
    bufferPool   sync.Pool    // reusable byte buffers to reduce GC pressure
}
```

#### Download Pipeline

Every operation starts with `downloadImage()`:

```
1. Replace "localhost:8080" with "api:8080" (Docker networking)
2. Create HTTP request with context timeout (30s)
3. Set User-Agent and Accept: image/* headers
4. Check Content-Length against 10 MB limit before downloading
5. Stream body into pooled buffer via io.LimitReader (safety net)
6. Copy buffer to fixed-size byte slice (required for re-reading)
7. Decode image using standard library image.Decode
8. autoOrient() — read EXIF orientation tag, rotate image if needed
9. Return (image.Image, format string, error)
```

#### Why Buffer Pooling?

Image processing allocates large byte slices frequently. Without pooling, each operation allocates then discards megabytes of memory, putting heavy pressure on the garbage collector. `sync.Pool` reuses these allocations:

```go
buf := p.bufferPool.Get().(*bytes.Buffer)
buf.Reset()
defer p.bufferPool.Put(buf)  // return to pool after use
```

#### EXIF Auto-Orient

Phone cameras store photos in landscape orientation internally and embed an EXIF rotation tag to tell software how to display them. Without auto-orient, processing a portrait-mode phone photo would produce a rotated output.

```go
func (p *ImageProcessor) autoOrient(data []byte, img image.Image) image.Image {
    x, _ := exif.Decode(bytes.NewReader(data))
    tag, _ := x.Get(exif.Orientation)
    orientation, _ := tag.Int(0)
    switch orientation {
    case 6: return imaging.Rotate270(img)  // phone portrait: stored rotated 90°
    case 8: return imaging.Rotate90(img)
    case 3: return imaging.Rotate180(img)
    // ... flip variants 2, 4, 5, 7
    }
    return img  // orientation 1 = already correct
}
```

#### Output Deduplication (Content Hash)

The same source image with the same parameters should produce the same output. Rather than generating a UUID every time (which creates duplicate files), we hash the encoded output:

```
Encode image to bytes in memory
↓
SHA-256 hash the bytes
↓
Take first 8 bytes → hex string → filename  (e.g. "a3f7b2c1d4e5f6a7.jpeg")
↓
If file already exists at that path → skip write, return existing URL
↓
Otherwise → write file
```

This means:
- Repeated submissions of identical tasks produce no extra disk usage
- Cache-friendly: the same URL is returned for identical operations

#### Correct Sepia Filter

The old implementation applied incorrect channel multipliers to an already-grayscale image, producing warm gray rather than true sepia. The correct implementation applies the standard sepia matrix to the full RGB image:

```
R' = R×0.393 + G×0.769 + B×0.189
G' = R×0.349 + G×0.686 + B×0.168
B' = R×0.272 + G×0.534 + B×0.131
```

#### Target-Size Compression (Binary Search)

Real-world use cases need "compress this image to under 200KB", not "compress at quality 75". We implement this with binary search over the quality parameter:

```
lo=1, hi=95, bestQuality=1

while lo <= hi:
    mid = (lo + hi) / 2
    encode to buffer at quality=mid
    if buffer.size <= targetBytes:
        bestQuality = mid   ← this quality fits
        lo = mid + 1        ← try higher quality (better looking)
    else:
        hi = mid - 1        ← too big, reduce quality

save at bestQuality
```

This converges in ≤7 iterations (log₂ of 95 ≈ 6.5) and guarantees the output is at most `targetBytes` in size.

#### Responsive Resize (Parallel)

`image_responsive` downloads once and resizes to multiple widths in parallel goroutines:

```go
// Download image ONCE
img, format, _ := p.downloadImage(ctx, payload.SourceURL)

// Resize to all widths CONCURRENTLY
ch := make(chan result, len(payload.Widths))
for i, w := range payload.Widths {
    go func(idx, width int) {
        resized := imaging.Resize(img, width, 0, imaging.Lanczos)
        path, url, _ := p.saveImageWithQuality(ctx, resized, outFmt, quality)
        ch <- result{idx, ...}
    }(i, w)
}

// Collect results in original order
outputs := make([]ResponsiveOutput, len(payload.Widths))
for range payload.Widths {
    r := <-ch
    outputs[r.idx] = r.out
}
```

`height=0` in `imaging.Resize` means "auto — maintain aspect ratio".

#### Storage Cleanup

Processed images accumulate on disk. The worker runs a cleanup goroutine every hour:

```go
func (w *Worker) cleanupLoop() {
    ticker := time.NewTicker(1 * time.Hour)
    for {
        select {
        case <-w.ctx.Done(): return
        case <-ticker.C:
            w.processor.CleanupOldFiles(24 * time.Hour)
        }
    }
}
```

`CleanupOldFiles` reads the storage directory and removes any file whose `ModTime` is older than the max age. This keeps disk usage bounded even under sustained load.

---

### 3.6 Security

**Files**: `backend/internal/security/auth.go`, `validator.go`, `ratelimit.go`

#### JWT Authentication

```
User logs in → API generates JWT:
  Header:  { alg: "HS256", typ: "JWT" }
  Payload: { user_id, email, roles, iss: "taskqueue", exp: now+24h, iat: now }
  Signature: HMAC-SHA256(header + "." + payload, JWT_SECRET)

JWT stored in browser localStorage (not httpOnly cookie — trade-off for simplicity)

Every protected request:
  Authorization: Bearer <token>
  ↓
  AuthMiddleware:
    1. Extract token from header
    2. Parse + verify signature
    3. Check expiration
    4. Check Redis blacklist (was this token logged out?)
    5. Set user_id, email, roles in Fiber context locals
```

#### Why Redis Blacklist for Logout?

JWTs are stateless by design — once issued, there's no server-side way to invalidate them before expiration. The blacklist solves this:

```
POST /auth/logout:
  1. Extract token from request
  2. SETEX "blacklist:{token}" "1" {remaining TTL}
  3. Future requests with this token → fail at step 4 of AuthMiddleware
```

The TTL is set to the token's remaining lifetime so the blacklist entry auto-expires when the token would have expired anyway, keeping Redis storage bounded.

#### bcrypt Password Hashing

```go
cost := 14  // work factor: 2^14 iterations of Blowfish
hash, _ := bcrypt.GenerateFromPassword([]byte(password), cost)
```

Cost 14 means each password hash takes ~250ms to compute. This is slow enough to make brute-force attacks impractical but fast enough for normal login flows.

#### Rate Limiting (Sliding Window)

```
Redis ZSET key: "ratelimit:user:{user_id}"
Score: Unix timestamp in nanoseconds
Member: unique string per request

On each request:
  1. ZREMRANGEBYSCORE → remove entries older than 1 minute window
  2. ZCARD → count remaining entries (requests in last minute)
  3. If count >= limit (100) → return 429
  4. ZADD → add current timestamp as new entry
  5. EXPIRE → refresh key TTL to 1 minute
```

All five commands run in a Redis pipeline (single round-trip). The sliding window is more accurate than a fixed-window counter because it doesn't allow up to 2× the limit at window boundaries.

#### URL Validation (SSRF Prevention)

Server-Side Request Forgery (SSRF) is when an attacker tricks the server into making requests to internal services. We validate every image URL before downloading:

```
Checks performed:
  1. Max length: 2048 characters
  2. Parse URL — must be valid
  3. If host is localhost/127.0.0.1/api → only allow /uploads/ and /images/ paths
  4. Protocol → only https (for external URLs)
  5. Domain → must be in allowlist:
       picsum.photos, unsplash.com, *.unsplash.com,
       s3.amazonaws.com, storage.googleapis.com, cloudinary.com
  6. Path extension → must match image types (jpg, jpeg, png, gif, webp, bmp, svg)
  7. Blocked extensions → .exe, .bat, .sh, .php, .asp
  8. Path traversal → no ".." in path component
  9. Private IPs → block 10.*, 172.16-31.*, 192.168.*, etc.
```

The critical fix from the original code: the `//` check was previously applied to the full URL string (which always contains `://`), blocking all HTTPS URLs. It now correctly checks only the path component.

#### Role-Based Access Control

```
Roles stored per user: ["user"]  or  ["admin"]  or  ["user", "admin"]

Middleware:
  RoleMiddleware("admin") → checks if roles slice contains "admin"

Enforcement points:
  - GET /api/v1/tasks      → non-admins see only their tasks (filtered by user_id)
  - GET /api/v1/tasks/:id  → non-admins can only access their own tasks
  - DELETE /api/v1/tasks/:id → same ownership check
  - GET /api/v1/users/*    → admin only
```

---

### 3.7 Reliability Patterns

**Files**: `backend/internal/reliability/`

#### Circuit Breaker

```
States and transitions:

        failures > threshold
  CLOSED ──────────────────► OPEN
    ▲                          │
    │   success                │ timeout expires
    │                          ▼
  HALF-OPEN ◄─────────────── OPEN
    │
    │ failure
    └───────────────────────► OPEN
```

Implementation is a state machine protected by a `sync.Mutex`. The worker creates one circuit breaker per worker process:

```go
cb := reliability.NewCircuitBreaker(reliability.Settings{
    Name:        "task-processing",
    MaxRequests: 3,               // test requests allowed in HALF-OPEN
    Interval:    10 * time.Second, // reset failure counter interval
    Timeout:     30 * time.Second, // duration of OPEN state
    ReadyToTrip: func(counts Counts) bool {
        return counts.ConsecutiveFailures > 5
    },
    OnStateChange: func(name string, from, to State) {
        log.Printf("Circuit breaker %s: %s -> %s", name, from, to)
        monitoring.CircuitBreakerState.WithLabelValues(name).Set(float64(to))
    },
})
```

When the circuit is open, tasks are requeued to the stream rather than being failed immediately. They'll be picked up again when the circuit closes.

#### Retry with Exponential Backoff + Jitter

```
attempt 1: base delay = 1s
attempt 2: base delay = 2s  (1s × 2.0)
attempt 3: base delay = 4s  (2s × 2.0)

Jitter: ±25% random variation
  actual_delay = base_delay + random(-0.25×base, +0.25×base)

Why jitter?
  Without it: 100 workers all retry at exactly t=2s, t=4s, t=8s
              → thundering herd crushes the recovering service
  With jitter: retries spread randomly across the window
              → smooth recovery load
```

The jitter is computed with `rand.Float64()` (range [0,1)) to produce a value within the jitter window.

#### Health Checks

```go
// Health check endpoint returns:
{
  "status": "healthy",
  "checks": {
    "postgres": {
      "name": "postgresql",
      "status": "healthy",
      "timestamp": "...",
      "duration_ms": 0
    },
    "redis": {
      "name": "redis",
      "status": "healthy",
      "timestamp": "...",
      "duration_ms": 0
    }
  }
}
```

Checks run concurrently (parallel goroutines) with a mutex protecting the results map. The original code had a race condition here (concurrent map writes without synchronization) that was fixed by adding `sync.Mutex`.

Kubernetes uses this endpoint for:
- **Readiness probe**: Don't send traffic if unhealthy
- **Liveness probe**: Restart the pod if it stays unhealthy

#### Graceful Shutdown

```
SIGTERM received
↓
API: Fiber.ShutdownWithTimeout(30s) — drain in-flight HTTP requests
Worker: context cancellation propagates to all goroutines
↓
Worker goroutines finish current task or timeout
↓
sync.WaitGroup.Wait() — block until all goroutines exit
↓
Close: broker, storage, heartbeat ticker
↓
Process exits cleanly
```

The 30-second window gives workers enough time to finish processing in-flight tasks before Kubernetes forcibly kills the container.

---

### 3.8 Observability

**Files**: `backend/internal/monitoring/metrics.go`, `monitoring/middleware.go`

#### Prometheus Metrics

All metrics are defined with `promauto` (auto-registers on creation):

| Metric | Type | Labels | Purpose |
|---|---|---|---|
| `taskqueue_tasks_submitted_total` | Counter | `type` | Tasks submitted per type |
| `taskqueue_tasks_processed_total` | Counter | `type`, `status` | Completed/failed counts |
| `taskqueue_tasks_in_progress` | Gauge | `worker_id` | Current active tasks per worker |
| `taskqueue_queue_length` | Gauge | — | Total pending tasks |
| `taskqueue_task_processing_duration_seconds` | Histogram | `type` | Processing time distribution |
| `taskqueue_active_workers` | Gauge | — | Number of live workers |
| `taskqueue_worker_heartbeats_total` | Counter | `worker_id` | Heartbeat health |
| `taskqueue_api_request_duration_seconds` | Histogram | `method`, `path`, `status` | API latency |
| `taskqueue_api_requests_total` | Counter | `method`, `path`, `status` | API traffic |
| `taskqueue_cache_hits_total` | Counter | — | Redis cache effectiveness |
| `taskqueue_cache_misses_total` | Counter | — | Cache miss rate |
| `taskqueue_circuit_breaker_state` | Gauge | `name` | 0=closed, 1=half-open, 2=open |

#### API Request Middleware

```go
func PrometheusMiddleware() fiber.Handler {
    return func(c *fiber.Ctx) error {
        start := time.Now()
        err := c.Next()
        duration := time.Since(start).Seconds()

        TaskQueueAPIRequestDuration.WithLabelValues(
            c.Method(), c.Path(), strconv.Itoa(c.Response().StatusCode()),
        ).Observe(duration)

        return err
    }
}
```

Every HTTP request automatically records its duration and status code. Grafana can then show latency percentiles (p50, p95, p99) per endpoint.

#### Grafana Dashboards

Grafana is pre-configured to scrape Prometheus at `http://prometheus:9090`. Key dashboard panels:
- Task throughput (tasks/second)
- Processing latency histogram
- Queue depth over time
- Worker fleet health
- Error rate by task type
- Circuit breaker state timeline

#### OpenTelemetry Tracing (Optional)

When built with `-tags tracing`, the system emits distributed traces to Jaeger. Each trace covers the full journey of a task:

```
Trace: "Submit task"
  └── Span: "validatePayload"
  └── Span: "createTask" (PostgreSQL)
  └── Span: "publishBroker" (Redis XADD)

Trace: "Process task" (Worker)
  └── Span: "downloadImage" (HTTP)
  └── Span: "imageResize" (CPU)
  └── Span: "saveImage" (disk write)
  └── Span: "updateTask" (PostgreSQL)
```

Spans are connected via W3C TraceContext propagation, so the full path from API to worker is visible in one trace.

---

### 3.9 Optional Interfaces — GraphQL & gRPC

#### GraphQL

**File**: `backend/internal/graphql/resolver.go` (build tag: `graphql`)

The GraphQL endpoint is an alternative to REST, enabled when the binary is compiled with `-tags graphql`. It's served at `/graphql` and supports:

```graphql
type Query {
  task(id: ID!): Task
  tasks(page: Int, pageSize: Int, status: String): TaskList
  workers: [Worker]
  metrics: SystemMetrics
}

type Mutation {
  submitTask(type: String!, payload: String!, priority: Int): Task
  deleteTask(id: ID!): Response
  cancelTask(id: ID!): Response
}
```

The GraphQL interface is useful for clients that need flexible queries — for example, fetching only specific fields or requesting multiple resources in a single round-trip.

Auth is enforced by checking `user_id` from the request context (set by the same `AuthMiddleware` as REST routes).

#### gRPC

**File**: `backend/internal/grpc/task_server.go` (build tag: `grpc`)

The gRPC server provides a typed, binary-protocol alternative on port 50051. It's suitable for:
- Machine-to-machine integrations where performance matters
- Strongly typed clients (auto-generated from `.proto` files)
- Streaming use cases (e.g., streaming task status updates)

Methods mirror the REST API: `SubmitTask`, `GetTask`, `ListTasks`, `DeleteTask`, `CancelTask`.

A stub file (`internal/grpc/stub.go`) with build tag `!grpc` ensures the package is always importable even when gRPC is not compiled in.

---

### 3.10 Frontend

**Files**: `frontend/src/`

#### Application Structure

```
App.jsx
├── Checks localStorage for JWT token
├── If token exists → Dashboard
└── If no token → Login

Login.jsx
├── Toggle between login and register modes
├── Login: POST /api/v1/auth/login → store token
└── Register: POST /api/v1/auth/register → store returned token (auto-login)

Dashboard.jsx
├── Polls /api/v1/tasks every 5 seconds
├── Polls /api/v1/metrics/system every 5 seconds
├── Left panel: Task submission form
│   ├── Task type dropdown (8 types)
│   ├── File upload → POST /api/v1/upload → inject URL into payload
│   ├── Payload JSON editor (pre-populated defaults per task type)
│   ├── Priority and max retries
│   └── Submit → POST /api/v1/tasks
└── Right panel:
    ├── MetricsCard (queue depth, active workers, avg latency)
    └── TaskList (task table with status badges and download buttons)
```

#### API Client (Axios)

```javascript
const API_BASE = import.meta.env.VITE_API_BASE || '/api/v1'

// All requests include the JWT from localStorage
export const getAuthHeader = () => {
  const token = localStorage.getItem('token')
  return token ? { Authorization: `Bearer ${token}` } : {}
}
```

The Vite dev server proxies `/api/*` to `localhost:8080`, so the frontend never needs to handle CORS in development.

#### Vite Proxy Configuration

```javascript
// vite.config.js
proxy: {
  '/api':     { target: 'http://localhost:8080', ws: true },
  '/graphql': { target: 'http://localhost:8080', ws: true },
  '/images':  { target: 'http://localhost:8080' },
  '/uploads': { target: 'http://localhost:8080' },
}
```

`ws: true` enables WebSocket proxying, so the browser's WebSocket connection to `/api/v1/ws` is tunneled to the API server.

#### Download Button Logic

Completed tasks include an `output_url` like `http://api:8080/c4c7...jpeg`. The frontend rewrites this to `http://localhost:8080/images/c4c7...jpeg` before fetching, because `api:8080` is a Docker-internal hostname not reachable from the browser.

---

## 4. Data Flow Walkthroughs

### 4.1 Submitting a Task (REST)

```
Browser                API Server              Redis               PostgreSQL
  │                        │                     │                      │
  │─POST /api/v1/tasks────►│                     │                      │
  │  Authorization: Bearer │                     │                      │
  │  {type, payload, ...}  │                     │                      │
  │                        │                     │                      │
  │                   ┌────▼────┐                │                      │
  │                   │AuthMW   │                │                      │
  │                   │JWT check│                │                      │
  │                   │Blacklist│──GET blacklist:►│                     │
  │                   │check    │◄────── nil ────│                      │
  │                   └────┬────┘                │                      │
  │                        │                     │                      │
  │                   ┌────▼────────┐            │                      │
  │                   │Rate Limiter │            │                      │
  │                   │ZSET sliding │──ZCARD────►│                      │
  │                   │window check │◄──count────│                      │
  │                   └────┬────────┘            │                      │
  │                        │                     │                      │
  │                   ┌────▼──────────┐          │                      │
  │                   │isValidTaskType│          │                      │
  │                   │ValidatePayload│          │                      │
  │                   │(URL/SSRF check│          │                      │
  │                   └────┬──────────┘          │                      │
  │                        │                     │                      │
  │                        │ CreateTask()         │                      │
  │                        │──────────────────────────────────────────►│
  │                        │                     │                   INSERT│
  │                        │                     │                   tasks│
  │                        │◄─────────────────────────────────────────── │
  │                        │                     │                      │
  │                        │ SET task:{id}        │                      │
  │                        │────────────────────►│                      │
  │                        │                     │ (cache, 30s TTL)     │
  │                        │                     │                      │
  │                        │ XADD task-queue      │                      │
  │                        │────────────────────►│                      │
  │                        │                     │ (stream entry)       │
  │                        │                     │                      │
  │                        │ broadcastTaskUpdate()│                      │
  │                        │ (WebSocket push)     │                      │
  │                        │                     │                      │
  │◄─201 {task_id, status}─│                     │                      │
```

### 4.2 Worker Processing a Task

```
Worker Process             Redis                PostgreSQL          ImageProcessor
     │                       │                      │                      │
     │ XREADGROUP            │                      │                      │
     │──────────────────────►│                      │                      │
     │◄── message {task JSON}│                      │                      │
     │                       │                      │                      │
     │ UpdateTask(processing) │                      │                      │
     │──────────────────────────────────────────────►│                     │
     │                       │                      │ UPDATE status        │
     │                       │                      │                      │
     │ cb.Execute(func)      │                      │                      │
     │──────────────────────────────────────────────────────────────────►│
     │                       │                      │ downloadImage()      │
     │                       │                      │ (HTTP → external URL)│
     │                       │                      │                      │
     │                       │                      │ autoOrient (EXIF)    │
     │                       │                      │ processOperation()   │
     │                       │                      │ hash + save to disk  │
     │◄──────────────────────────────────────────────────────────────────│
     │ result / error        │                      │                      │
     │                       │                      │                      │
     │ [SUCCESS]             │                      │                      │
     │ UpdateTask(completed) │                      │                      │
     │──────────────────────────────────────────────►│                     │
     │                       │                      │ UPDATE result,status │
     │                       │                      │                      │
     │ XACK                  │                      │                      │
     │──────────────────────►│                      │                      │
     │                       │ message removed      │                      │
     │                       │ from pending list    │                      │
```

### 4.3 Task Failure and Retry

```
Worker                         PostgreSQL              Redis
  │                                │                     │
  │ processTask() returns error    │                     │
  │                                │                     │
  │ retries < maxRetries?          │                     │
  │ YES → handleTaskFailure()      │                     │
  │                                │                     │
  │ UpdateTask(retrying)           │                     │
  │───────────────────────────────►│                     │
  │                                │ UPDATE retries+1    │
  │                                │                     │
  │ XACK (remove from pending)     │                     │
  │────────────────────────────────────────────────────►│
  │                                │                     │
  │ time.Sleep(backoff delay)      │                     │
  │ (1s → 2s → 4s with ±25% jitter)                    │
  │                                │                     │
  │ broker.Publish(task)           │                     │
  │────────────────────────────────────────────────────►│
  │                                │                     │ XADD (requeue)
  │                                │                     │
  │ [on next iteration]            │                     │
  │ XREADGROUP picks it up again   │                     │
  │◄───────────────────────────────────────────────────│
  │                                │                     │
  │ [if retries >= maxRetries]     │                     │
  │ UpdateTask(failed)             │                     │
  │───────────────────────────────►│                     │
  │                                │ UPDATE status=failed│
  │                                │ error=err.Error()   │
  │ XACK                           │                     │
  │────────────────────────────────────────────────────►│
```

### 4.4 Authentication Flow

```
Browser                     API Server                Redis               PostgreSQL
  │                              │                       │                     │
  │─POST /auth/login────────────►│                       │                     │
  │  {username, password}        │                       │                     │
  │                              │ GetUserByUsername()   │                     │
  │                              │────────────────────────────────────────────►│
  │                              │◄──── user (hash,roles) ─────────────────────│
  │                              │                       │                     │
  │                              │ bcrypt.CompareHash()  │                     │
  │                              │ (250ms work factor)   │                     │
  │                              │                       │                     │
  │                              │ GenerateToken()       │                     │
  │                              │ JWT signed HS256      │                     │
  │                              │                       │                     │
  │◄─ 200 {token, user} ────────│                       │                     │
  │                              │                       │                     │
  │─Protected request────────────►│                      │                     │
  │  Authorization: Bearer <jwt> │                       │                     │
  │                              │ ParseToken()          │                     │
  │                              │ Verify signature      │                     │
  │                              │                       │                     │
  │                              │ GET blacklist:<token>  │                     │
  │                              │──────────────────────►│                     │
  │                              │◄──── nil (not blacklisted) ─────────────────│
  │                              │                       │                     │
  │                              │ Set context locals:   │                     │
  │                              │ user_id, email, roles │                     │
  │                              │                       │                     │
  │─POST /auth/logout────────────►│                      │                     │
  │                              │ BlacklistToken()      │                     │
  │                              │ SETEX blacklist:<jwt>──►│                  │
  │◄─ 200 OK ───────────────────│                       │ (TTL = remaining)   │
```

### 4.5 File Upload Flow

```
Browser                         API Server               Disk
  │                                  │                     │
  │─POST /api/v1/upload─────────────►│                     │
  │  multipart/form-data             │                     │
  │  file: <image data>              │                     │
  │                                  │ Validate file type  │
  │                                  │ (jpg/png/gif/webp)  │
  │                                  │                     │
  │                                  │ UUID filename       │
  │                                  │─────────────────────►
  │                                  │ Save to             │
  │                                  │ ./storage/uploads/  │
  │                                  │ {uuid}.{ext}        │
  │                                  │                     │
  │◄─ 200 {url: "http://api:8080/uploads/{uuid}.ext"} ────│
  │                                  │                     │
  │ Inject URL into task payload     │                     │
  │─POST /api/v1/tasks ─────────────►│                     │
  │  {type, payload: {source_url: "http://api:8080/..."}} │
  │                                  │                     │
  │ [Worker downloads from internal URL]                   │
  │                                  │ http://api:8080/uploads/{uuid}.ext
  │                                  │──────────────────────────────────────►
  │                                  │◄── image data ───────────────────────│
```

---

## 5. Database Schema

### `tasks` table

```sql
CREATE TABLE tasks (
    id              VARCHAR(36)    PRIMARY KEY,
    user_id         VARCHAR(36)    NOT NULL DEFAULT '',
    type            VARCHAR(50)    NOT NULL,
    payload         JSONB          NOT NULL,
    status          VARCHAR(20)    NOT NULL DEFAULT 'queued',
    priority        INTEGER        NOT NULL DEFAULT 0,
    result          JSONB,
    error           TEXT,
    retries         INTEGER        NOT NULL DEFAULT 0,
    max_retries     INTEGER        NOT NULL DEFAULT 3,
    created_at      TIMESTAMP      NOT NULL DEFAULT NOW(),
    started_at      TIMESTAMP,
    completed_at    TIMESTAMP,
    worker_id       VARCHAR(100),
    processing_time BIGINT         DEFAULT 0   -- milliseconds
);

-- Indexes for common query patterns
CREATE INDEX idx_tasks_status        ON tasks(status);
CREATE INDEX idx_tasks_created_at    ON tasks(created_at DESC);
CREATE INDEX idx_tasks_type          ON tasks(type);
CREATE INDEX idx_tasks_user_id       ON tasks(user_id);
CREATE INDEX idx_tasks_status_created ON tasks(status, created_at DESC);
```

**Why JSONB for payload and result?**

Image processing parameters vary by task type. Using JSONB avoids needing a separate table per task type and allows schema-less evolution — adding a new field to a payload never requires a migration.

### `workers` table

```sql
CREATE TABLE workers (
    worker_id         VARCHAR(100)   PRIMARY KEY,
    status            VARCHAR(20)    NOT NULL DEFAULT 'active',
    tasks_processed   INTEGER        NOT NULL DEFAULT 0,
    tasks_failed      INTEGER        NOT NULL DEFAULT 0,
    last_heartbeat    TIMESTAMP      NOT NULL DEFAULT NOW(),
    start_time        TIMESTAMP      NOT NULL DEFAULT NOW(),
    current_task      VARCHAR(36),
    active_goroutines INTEGER        NOT NULL DEFAULT 0
);
```

Workers UPSERT this record every 10 seconds. If `last_heartbeat` is more than 30 seconds old, the worker is considered dead.

### `batches` table

```sql
CREATE TABLE batches (
    id           VARCHAR(36) PRIMARY KEY,  -- UUID
    user_id      VARCHAR(36) NOT NULL,     -- owner
    type         VARCHAR(50) NOT NULL,     -- same task type for all items
    total        INTEGER NOT NULL,         -- number of images submitted
    created_at   TIMESTAMP NOT NULL DEFAULT NOW(),
    completed_at TIMESTAMP                 -- set when all tasks reach terminal state
);
```

Task counts (`queued`, `processing`, `completed`, `failed`) are **not stored** in this table — they are computed live by `GetBatch` via a single aggregate SQL query over the tasks table. This prevents counters from drifting out of sync.

### `users` table

```sql
CREATE TABLE users (
    id             VARCHAR(36)   PRIMARY KEY,
    username       VARCHAR(50)   UNIQUE NOT NULL,
    email          VARCHAR(100)  UNIQUE NOT NULL,
    password_hash  VARCHAR(255),              -- NULL for OAuth-only accounts
    api_key        VARCHAR(64)   UNIQUE,
    roles          TEXT[]        DEFAULT ARRAY['user'],
    oauth_provider VARCHAR(20),               -- 'google' | 'github' | NULL
    oauth_id       VARCHAR(200),              -- provider-scoped user ID
    created_at     TIMESTAMP     NOT NULL DEFAULT NOW(),
    updated_at     TIMESTAMP     NOT NULL DEFAULT NOW()
);

CREATE UNIQUE INDEX idx_users_username ON users(username);
CREATE UNIQUE INDEX idx_users_email    ON users(email);
CREATE UNIQUE INDEX idx_users_api_key  ON users(api_key);
-- Partial unique index prevents duplicate OAuth identities
-- while allowing NULL (password-only users) without conflict.
CREATE UNIQUE INDEX idx_users_oauth ON users(oauth_provider, oauth_id)
    WHERE oauth_provider IS NOT NULL;
```

`roles` is a PostgreSQL `TEXT[]` array (e.g. `{"user", "admin"}`).

`password_hash` is nullable: OAuth users created via Google/GitHub login have no local password. The partial index on `(oauth_provider, oauth_id)` prevents duplicate OAuth identities while allowing rows with `oauth_provider IS NULL` (password-auth users) to coexist without unique-constraint conflicts.

### Migration Files

```
migrations/
├── 001_initial_schema.up.sql     — Creates all three tables with base columns
├── 001_initial_schema.down.sql   — DROP TABLE all three
├── 002_add_indexes.up.sql        — Adds composite and covering indexes
├── 002_add_indexes.down.sql      — Drops the added indexes
├── 003_add_task_user_id.up.sql   — ALTER TABLE tasks ADD COLUMN user_id
└── 003_add_task_user_id.down.sql — ALTER TABLE tasks DROP COLUMN user_id
```

Migrations are numbered sequentially and pair `.up.sql` with `.down.sql` for rollback capability. They are applied in numerical order.

---

## 6. API Reference

### Authentication

| Method | Path | Body | Response | Notes |
|---|---|---|---|---|
| `POST` | `/api/v1/auth/register` | `{username, email, password}` | `{token, user, api_key}` | Password min 8 chars |
| `POST` | `/api/v1/auth/login` | `{username, password}` | `{token, user}` | Returns JWT |
| `POST` | `/api/v1/auth/logout` | — | `200 OK` | Blacklists token in Redis |
| `GET` | `/api/v1/auth/me` | — | `{user}` | Current user profile |
| `GET` | `/api/v1/auth/oauth/:provider` | — | Redirect | Starts OAuth flow (`google` or `github`) |
| `GET` | `/api/v1/auth/oauth/:provider/callback` | `?code=&state=` | `{token, user}` | OAuth callback — returns JWT |

#### OAuth 2.0 Flow

```
Browser → GET /api/v1/auth/oauth/google
        ← 307 redirect to Google consent screen

Google  → GET /api/v1/auth/oauth/google/callback?code=AUTH_CODE&state=CSRF_STATE
        ← { "token": "<JWT>", "user": { "id", "username", "email" } }
```

Required environment variables (leave blank to disable the provider):

```bash
GOOGLE_CLIENT_ID=your-google-client-id
GOOGLE_CLIENT_SECRET=your-google-client-secret
GITHUB_CLIENT_ID=your-github-client-id
GITHUB_CLIENT_SECRET=your-github-client-secret
APP_BASE_URL=https://yourdomain.com   # used to build redirect URIs
```

**Account linking**: if an OAuth email matches an existing password-auth account, the OAuth identity is linked to that account instead of creating a duplicate.

### Tasks

| Method | Path | Body/Query | Response | Notes |
|---|---|---|---|---|
| `POST` | `/api/v1/tasks` | `{type, payload, priority, max_retries}` | `{task_id, status}` | Single task submission |
| `POST` | `/api/v1/tasks/batch` | `{type, images:[...], priority, max_retries}` | `{batch_id, total, status}` | Submits N tasks at once |
| `GET` | `/api/v1/tasks` | `?page=1&page_size=20&status=queued` | `{tasks, total, page}` | Non-admins see own tasks only |
| `GET` | `/api/v1/tasks/:id` | — | `{task, message}` | Ownership enforced |
| `DELETE` | `/api/v1/tasks/:id` | — | `200 OK` | Ownership enforced |
| `POST` | `/api/v1/tasks/:id/cancel` | — | `{status: "cancelled"}` | Stops queued or running task |

#### Batch Submission

Submit up to 500 images in one request. Each image gets an independent task that is published to the Redis Stream and picked up by workers concurrently.

```bash
curl -X POST http://localhost:8080/api/v1/tasks/batch \
  -H "Authorization: Bearer $TOKEN" \
  -H "Content-Type: application/json" \
  -d '{
    "type": "image_resize",
    "images": [
      {"source_url": "https://example.com/img1.jpg", "width": 800, "height": 600},
      {"source_url": "https://example.com/img2.jpg", "width": 800, "height": 600},
      {"source_url": "https://example.com/img3.jpg", "width": 800, "height": 600}
    ],
    "priority": 0,
    "max_retries": 3
  }'
```

Response:
```json
{ "batch_id": "abc-123", "total": 3, "status": "queued", "created_at": "..." }
```

#### Batch Status

```bash
curl http://localhost:8080/api/v1/batches/$BATCH_ID \
  -H "Authorization: Bearer $TOKEN"
```

Response (live counts, computed from tasks table):
```json
{
  "id": "abc-123",
  "type": "image_resize",
  "status": "processing",
  "total": 3,
  "queued": 1,
  "processing": 1,
  "completed": 1,
  "failed": 0
}
```

Status transitions: `queued` → `processing` → `completed` | `partial` | `failed`

#### Task Cancellation

```bash
curl -X POST http://localhost:8080/api/v1/tasks/$TASK_ID/cancel \
  -H "Authorization: Bearer $TOKEN"
```

- Sets task status to `cancelled` in PostgreSQL.
- Sets a Redis key `task:cancel:{id}` (2h TTL) checked by the worker before and after processing.
- Returns `409 Conflict` if the task is already in a terminal state (`completed`, `failed`, `cancelled`).

### Batches

| Method | Path | Response | Notes |
|---|---|---|---|
| `GET` | `/api/v1/batches/:id` | `{id, type, status, total, queued, processing, completed, failed}` | Ownership enforced |

### File Upload

| Method | Path | Body | Response | Notes |
|---|---|---|---|---|
| `POST` | `/api/v1/upload` | `multipart/form-data file=<image>` | `{url, filename, size}` | Max 10MB |

### System

| Method | Path | Response | Notes |
|---|---|---|---|
| `GET` | `/api/v1/health` | `{status, checks:{postgres, redis}}` | Public endpoint |
| `GET` | `/api/v1/metrics/system` | `{queued_tasks, active_workers, ...}` | Auth required |
| `GET` | `/metrics` | Prometheus text format | Scraped by Prometheus |
| `GET` | `/api/v1/ws` | WebSocket upgrade | Real-time task events |

### Error Response Format

```json
{
  "code": 400,
  "error": "invalid payload",
  "path": "/api/v1/tasks"
}
```

HTTP status codes:
- `400` — Bad request (validation failed)
- `401` — Unauthorized (missing or invalid JWT)
- `403` — Forbidden (valid JWT but insufficient permission)
- `404` — Not found
- `409` — Conflict (duplicate username/email)
- `429` — Too many requests (rate limit exceeded)
- `500` — Internal server error

---

## 7. Image Processing Operations

All operations require `source_url` pointing to an image. External URLs must use HTTPS and come from the allowed domain list.

### `image_resize`

```json
{
  "type": "image_resize",
  "payload": {
    "source_url": "https://picsum.photos/1200/800.jpg",
    "width": 800,
    "height": 600,
    "maintain_aspect": true,
    "output_format": "webp"
  }
}
```

- `maintain_aspect: true` → resize to fit within width×height, preserving ratio
- `maintain_aspect: false` → crop to fill exactly width×height (center crop)
- `output_format` → optional format conversion (jpeg, png, webp)

### `image_compress`

```json
{
  "type": "image_compress",
  "payload": {
    "source_url": "https://picsum.photos/1200/800.jpg",
    "format": "jpeg",
    "quality": 80
  }
}
```

Or using **target file size** (binary search on quality):

```json
{
  "type": "image_compress",
  "payload": {
    "source_url": "https://picsum.photos/1200/800.jpg",
    "format": "jpeg",
    "target_size_bytes": 102400
  }
}
```

`target_size_bytes` overrides `quality` and guarantees the output is at most that size.

### `image_watermark`

```json
{
  "type": "image_watermark",
  "payload": {
    "source_url": "https://picsum.photos/800/600.jpg",
    "watermark_url": "https://picsum.photos/200/100.jpg",
    "position": "bottom-right",
    "opacity": 0.5
  }
}
```

Positions: `top-left`, `top-right`, `bottom-left`, `bottom-right`, `center`
Watermark is auto-scaled to a maximum of 30% of the base image dimensions.

### `image_filter`

```json
{
  "type": "image_filter",
  "payload": {
    "source_url": "https://picsum.photos/800/600.jpg",
    "filter_type": "blur",
    "params": { "sigma": 3.0 }
  }
}
```

Available filters:

| Filter | Params | Notes |
|---|---|---|
| `grayscale` | — | Converts to black & white |
| `sepia` | — | Warm sepia tone via RGB matrix |
| `blur` | `sigma` (0.1–100, default 5.0) | Gaussian blur |
| `sharpen` | `amount` (0.1–5.0, default 1.0) | Unsharp mask |
| `brightness` | `level` (-100 to 100, default 0) | Positive = brighter |
| `contrast` | `level` (-100 to 100, default 0) | Positive = more contrast |
| `saturation` | `level` (-100 to 100, default 0) | Negative = desaturate |

### `image_thumbnail`

```json
{
  "type": "image_thumbnail",
  "payload": {
    "source_url": "https://picsum.photos/1200/800.jpg",
    "width": 400,
    "height": 225,
    "fit_mode": "cover"
  }
}
```

Or square thumbnail shorthand:

```json
{ "source_url": "...", "size": 200 }
```

- `fit_mode: cover` (default) — crop to fill exactly width×height
- `fit_mode: contain` — letterbox to fit within width×height

### `image_crop`

```json
{
  "type": "image_crop",
  "payload": {
    "source_url": "https://picsum.photos/800/600.jpg",
    "x": 100,
    "y": 50,
    "width": 400,
    "height": 300,
    "output_format": "png"
  }
}
```

Crops a rectangular region starting at pixel `(x, y)` with the given dimensions. Coordinates are clamped to image bounds automatically.

### `image_format_convert`

```json
{
  "type": "image_format_convert",
  "payload": {
    "source_url": "https://picsum.photos/800/600.jpg",
    "output_format": "webp",
    "quality": 85
  }
}
```

Supported formats: `jpeg`, `png`, `webp`

### `image_responsive`

```json
{
  "type": "image_responsive",
  "payload": {
    "source_url": "https://picsum.photos/1920/1080.jpg",
    "widths": [320, 640, 1024, 1280, 1920],
    "output_format": "webp",
    "quality": 80
  }
}
```

Downloads the source image **once** and produces one output per width. Heights are computed automatically to preserve aspect ratio. Result contains an array of outputs:

```json
{
  "output_urls": [
    { "width": 320,  "height": 180, "output_url": "http://...", "file_size": 8192 },
    { "width": 640,  "height": 360, "output_url": "http://...", "file_size": 18432 },
    { "width": 1024, "height": 576, "output_url": "http://...", "file_size": 41984 },
    ...
  ]
}
```

---

## 8. Configuration Reference

All configuration is via environment variables. Defaults are shown for local development.

| Variable | Default | Description |
|---|---|---|
| `REDIS_ADDR` | `localhost:6379` | Redis host:port |
| `POSTGRES_DSN` | `postgres://taskqueue_user:password@localhost/taskqueue?sslmode=disable` | Full PostgreSQL DSN |
| `JWT_SECRET` | `change-me-in-production` | HMAC signing key (must be changed) |
| `CORS_ORIGINS` | `*` | Comma-separated allowed origins |
| `STORAGE_DIR` | `./storage/images` | Processed image output directory |
| `BASE_URL` | `http://localhost:8080/images` | Public URL prefix for output files |
| `API_SERVICE_URL` | `http://api:8080` | Internal API URL (Docker networking) |
| `WORKER_CONCURRENCY` | `runtime.NumCPU()` | Goroutines per worker process |
| `POSTGRES_PASSWORD` | — | Set in `.env` file |
| `GRAFANA_PASSWORD` | `admin` | Grafana admin password |

### `.env` File

```bash
# Place this file at the repository root alongside docker-compose.yml
# Never commit this file — add it to .gitignore

# ── Required ──────────────────────────────────────────────────────────────────
POSTGRES_PASSWORD=strong_random_password_here
JWT_SECRET=minimum_32_chars_random_secret_here

# ── Observability ─────────────────────────────────────────────────────────────
GRAFANA_PASSWORD=your_grafana_password   # Grafana admin password (default: admin)
LOG_LEVEL=info                           # debug | info | warn | error

# ── OAuth 2.0 (optional — leave blank to disable) ─────────────────────────────
APP_BASE_URL=http://localhost:8080       # public URL — used to build OAuth redirect URIs
GOOGLE_CLIENT_ID=
GOOGLE_CLIENT_SECRET=
GITHUB_CLIENT_ID=
GITHUB_CLIENT_SECRET=
```

#### Setting up Google OAuth

1. Go to [console.cloud.google.com](https://console.cloud.google.com) → APIs & Services → Credentials
2. Create an OAuth 2.0 Client ID (Web application)
3. Add Authorised redirect URI: `http://localhost:8080/api/v1/auth/oauth/google/callback`
4. Copy Client ID and Secret into `.env`

#### Setting up GitHub OAuth

1. Go to GitHub → Settings → Developer settings → OAuth Apps → New OAuth App
2. Set Authorization callback URL: `http://localhost:8080/api/v1/auth/oauth/github/callback`
3. Copy Client ID and Secret into `.env`

---

## 9. Infrastructure & Deployment

### Local Development (Docker Compose)

```bash
# Start all services
docker compose up -d

# Check status
docker compose ps

# View API logs
docker compose logs -f api

# View worker logs
docker compose logs -f worker

# Stop everything (preserve volumes)
docker compose down

# Stop and wipe all data
docker compose down -v
```

Services and their ports:

| Service | Port | Purpose |
|---|---|---|
| Frontend | `80` | React web application |
| API | `8080` | REST / GraphQL / WebSocket |
| PostgreSQL | `5432` | Primary database |
| Redis | `6379` | Broker + Cache |
| Jaeger UI | `16686` | Distributed trace viewer — `http://localhost:16686` |
| Prometheus | `9090` | Metrics collection |
| Grafana | `3000` | Dashboards (admin/admin) |

### Docker Image Build Details

Both images use a **multi-stage build** to minimize final image size:

```dockerfile
# Stage 1: Builder (golang:1.25-alpine, ~600MB)
FROM golang:1.25-alpine AS builder
RUN apk add --no-cache git gcc musl-dev libwebp-dev
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=1 GOOS=linux go build -ldflags="-s -w" -o /app/bin/api ./cmd/api

# Stage 2: Runtime (alpine:3.20, ~10MB)
FROM alpine:latest
RUN apk add --no-cache ca-certificates libwebp
COPY --from=builder /app/bin/api .
```

The builder stage is discarded; only the compiled binary and libwebp runtime are in the final image.

**CGO is enabled** because the WebP encoder (`chai2010/webp`) wraps the native `libwebp` C library. The runtime image includes `libwebp` (the shared library) so the binary can call it.

### Kubernetes Deployment

```
k8s/
├── namespace.yaml          — "taskqueue" namespace
├── configmap.yaml          — Non-secret configuration
├── api-deployment.yaml     — API server (3 replicas)
├── worker-deployment.yaml  — Worker (starts at 2 replicas)
├── hpa.yaml                — Horizontal Pod Autoscaler for worker
├── ingress.yaml            — Nginx ingress with TLS
└── services.yaml           — ClusterIP + LoadBalancer services
```

#### Horizontal Pod Autoscaler

```yaml
# k8s/hpa.yaml
spec:
  scaleTargetRef:
    name: worker
  minReplicas: 2
  maxReplicas: 20
  metrics:
    - type: Resource
      resource:
        name: cpu
        target:
          averageUtilization: 80
```

When workers are busy (CPU > 80%), Kubernetes adds more worker pods automatically. Each new pod connects to the same Redis Stream consumer group and begins consuming tasks immediately, with no configuration needed.

#### Health Check Probes

```yaml
livenessProbe:
  httpGet:
    path: /api/v1/health
    port: 8080
  initialDelaySeconds: 15
  periodSeconds: 10

readinessProbe:
  httpGet:
    path: /api/v1/health
    port: 8080
  initialDelaySeconds: 5
  periodSeconds: 5
```

---

## 10. Security Model

### Threat Model Summary

| Threat | Mitigation |
|---|---|
| Unauthorized API access | JWT authentication on all protected routes |
| Session persistence after logout | Redis token blacklist |
| Brute-force password attacks | bcrypt cost 14 (~250ms/attempt) |
| API abuse / DDoS | Sliding-window rate limit (100 req/min per user/IP) |
| SSRF via image URL | Domain allowlist, private IP blocking, HTTPS-only |
| Path traversal in URLs | Path component `..` check |
| Privilege escalation | RBAC enforcement at handler level |
| Task data leakage | user_id ownership check on every task operation |
| Sensitive config exposure | Secrets in `.env` file, not in docker-compose.yml directly |
| Horizontal request forgery | CORS origin restriction (configurable) |
| Race conditions in metrics | sync.Mutex protection on shared maps |

### What is NOT in scope (known gaps)

- **No HTTPS termination in the app** — TLS should be terminated at the load balancer or Nginx ingress
- **No refresh tokens** — JWT tokens expire after 24h; users must re-login
- **No MFA** — Single-factor authentication only
- **Allowlist is restrictive** — Add your CDN/storage domain to `allowedDomains` in `validator.go` for production use
- **File upload virus scanning** — Uploaded files are not scanned for malware

---

## 11. Structured Logging

All services use Go's standard `log/slog` package with JSON output. Every log line includes `time`, `level`, `msg`, plus structured key-value fields — making logs queryable in tools like Loki, Datadog, or `jq`.

```bash
# Example API log line
{"time":"2025-03-09T12:00:01Z","level":"INFO","msg":"task submitted",
 "task_id":"abc-123","user_id":"usr-456","type":"image_resize"}

# Example worker log line
{"time":"2025-03-09T12:00:02Z","level":"INFO","msg":"task completed",
 "task_id":"abc-123","worker_id":"worker-1","duration_ms":342}
```

Set `LOG_LEVEL=debug` to also log source file and line number for every entry.

---

## 12. Distributed Tracing (OpenTelemetry + Jaeger)

Every HTTP request creates a root span in the API, which propagates its trace context into the Redis Stream message. The worker extracts the context and creates a child span — so the full `submit → queue → process` path appears as one trace in Jaeger.

```
Jaeger trace view:
├── POST /api/v1/tasks                          (API server span, ~5ms)
│   └── broker.Publish → Redis XAdd             (broker span)
│
└── worker.handleTask                           (worker span, ~350ms)
    └── processor.image_resize                  (processor span)
```

**Opening Jaeger UI**: `http://localhost:16686` after `docker compose up`.

Trace propagation uses the **W3C Trace Context** standard (`traceparent` / `tracestate` headers), so spans can be correlated with other OpenTelemetry-instrumented services.

---

## 13. Task Cancellation

A running or queued task can be stopped via:

```bash
curl -X POST http://localhost:8080/api/v1/tasks/$TASK_ID/cancel \
  -H "Authorization: Bearer $TOKEN"
```

**Two-layer mechanism** to handle the race between API and worker:

1. **API** — sets `task.status = cancelled` in PostgreSQL AND writes a Redis key `task:cancel:{id}` (TTL: 2 hours).
2. **Worker** — checks `task.status` at the start of processing. If already `cancelled`, skips immediately. After processing completes, re-checks the Redis key — if the cancellation arrived while processing was in flight, the result is discarded and the status stays `cancelled`.

Returns `409 Conflict` if the task is already in a terminal state (`completed`, `failed`, `cancelled`).

---

## 14. Monitoring & Alerting

### Prometheus Scrape Config

```yaml
# configs/prometheus.yml
scrape_configs:
  - job_name: 'taskqueue-api'
    static_configs:
      - targets: ['api:8080']
    metrics_path: '/metrics'
    scrape_interval: 15s
```

### Key Metrics to Watch

**Queue Health**
```
taskqueue_queue_length > 1000        → workers can't keep up
taskqueue_tasks_in_progress == 0     → no workers running
```

**Processing Quality**
```
rate(taskqueue_tasks_processed_total{status="failed"}[5m])
  /
rate(taskqueue_tasks_processed_total[5m])
> 0.10                               → >10% failure rate, investigate
```

**Latency**
```
histogram_quantile(0.95,
  rate(taskqueue_task_processing_duration_seconds_bucket[5m]))
> 30                                 → p95 processing > 30s, under-provisioned
```

**Circuit Breaker**
```
taskqueue_circuit_breaker_state == 2 → circuit OPEN, processing halted
```

**API Performance**
```
histogram_quantile(0.99,
  rate(taskqueue_api_request_duration_seconds_bucket[5m]))
> 2                                  → p99 API latency > 2s
```

### Grafana Dashboard Panels

Access Grafana at `http://localhost:3000` (admin/admin in development).

Recommended panels to create:
1. **Tasks Submitted vs Completed** — rate comparison over time
2. **Queue Depth** — pending task count
3. **Processing Latency (p50/p95/p99)** — by task type
4. **Worker Count** — active workers over time
5. **Error Rate** — failed tasks as percentage
6. **Circuit Breaker State** — timeline of state changes
7. **Cache Hit Rate** — Redis cache effectiveness
8. **API Request Rate** — by endpoint and status code

---

## 12. Project File Structure

```
distributed-task-queue/
│
├── .env                            ← secrets (never commit)
├── docker-compose.yml              ← local orchestration (6 services)
│
├── backend/
│   ├── go.mod / go.sum             ← Go module dependencies
│   │
│   ├── cmd/
│   │   ├── api/
│   │   │   └── main.go             ← API server entry point
│   │   ├── worker/
│   │   │   └── main.go             ← Worker entry point
│   │   └── grpc-server/
│   │       └── main.go             ← gRPC server (optional)
│   │
│   ├── internal/
│   │   ├── broker/
│   │   │   ├── redis_stream.go     ← Redis Streams broker (primary)
│   │   │   └── redis_cluster.go    ← Redis Cluster broker (HA)
│   │   │
│   │   ├── storage/
│   │   │   ├── storage.go          ← PostgreSQL + Redis cache layer
│   │   │   └── s3.go               ← AWS S3 backend (build-tagged)
│   │   │
│   │   ├── models/
│   │   │   └── models.go           ← All data structures / types
│   │   │
│   │   ├── security/
│   │   │   ├── auth.go             ← JWT, bcrypt, blacklist, middleware
│   │   │   ├── validator.go        ← URL/SSRF validation, payload checks
│   │   │   └── ratelimit.go        ← Sliding window rate limiter
│   │   │
│   │   ├── processor/
│   │   │   └── image_processor.go  ← All 8 image processing operations
│   │   │
│   │   ├── reliability/
│   │   │   ├── circuitbreaker.go   ← State machine circuit breaker
│   │   │   ├── retry.go            ← Exponential backoff + jitter
│   │   │   ├── health.go           ← Health check registry
│   │   │   ├── deduplication.go    ← SHA256 task deduplication
│   │   │   └── graceful_shutdown.go← SIGTERM handling
│   │   │
│   │   ├── monitoring/
│   │   │   ├── metrics.go          ← Prometheus metric definitions
│   │   │   └── middleware.go       ← HTTP request instrumentation
│   │   │
│   │   ├── graphql/
│   │   │   ├── resolver.go         ← GraphQL schema + resolvers (build-tagged)
│   │   │   └── stub.go             ← Stub for non-graphql builds
│   │   │
│   │   ├── grpc/
│   │   │   ├── task_server.go      ← gRPC TaskService (build-tagged)
│   │   │   └── stub.go             ← Stub for non-grpc builds
│   │   │
│   │   ├── cache/
│   │   │   └── distributed.go      ← General Redis cache (DB 2)
│   │   │
│   │   ├── database/
│   │   │   └── pool.go             ← PostgreSQL connection pool
│   │   │
│   │   └── tracing/
│   │       └── tracing.go          ← OpenTelemetry + Jaeger (build-tagged)
│   │
│   ├── migrations/
│   │   ├── 001_initial_schema.up.sql
│   │   ├── 001_initial_schema.down.sql
│   │   ├── 002_add_indexes.up.sql
│   │   ├── 002_add_indexes.down.sql
│   │   ├── 003_add_task_user_id.up.sql
│   │   └── 003_add_task_user_id.down.sql
│   │
│   ├── proto/
│   │   ├── taskqueue.proto         ← Protobuf service definition
│   │   ├── taskqueue.pb.go         ← Generated types
│   │   └── taskqueue_grpc.pb.go    ← Generated gRPC client/server
│   │
│   ├── docker/
│   │   ├── Dockerfile.api          ← CGO-enabled build, libwebp runtime
│   │   ├── Dockerfile.worker       ← CGO-enabled build, libwebp runtime
│   │   └── Dockerfile.grpc         ← gRPC server build
│   │
│   ├── k8s/
│   │   ├── namespace.yaml
│   │   ├── configmap.yaml
│   │   ├── api-deployment.yaml
│   │   ├── worker-deployment.yaml
│   │   ├── hpa.yaml
│   │   ├── ingress.yaml
│   │   └── services.yaml
│   │
│   └── configs/
│       ├── prometheus.yml
│       ├── alerts.yml
│       ├── alertmanager.yml
│       └── jaeger.yml
│
├── frontend/
│   ├── src/
│   │   ├── main.jsx                ← React entry point
│   │   ├── App.jsx                 ← Auth check, navbar, layout
│   │   ├── index.css               ← Tailwind base styles
│   │   │
│   │   ├── api/
│   │   │   └── api.js              ← Axios client (all API calls)
│   │   │
│   │   ├── components/
│   │   │   ├── Login.jsx           ← Login + register form
│   │   │   ├── Dashboard.jsx       ← Main UI, polling, task submission
│   │   │   ├── TaskList.jsx        ← Task table with status badges
│   │   │   ├── TaskTable.jsx       ← Table structure
│   │   │   ├── TaskRow.jsx         ← Individual task row + download
│   │   │   ├── MetricsCard.jsx     ← System metrics display
│   │   │   ├── SubmitCard.jsx      ← Task submission form
│   │   │   ├── SlideOver.jsx       ← Slide-in detail panel
│   │   │   └── DashboardGraphQL.jsx← Alternative GraphQL dashboard
│   │   │
│   │   ├── graphql/
│   │   │   ├── client.js           ← Apollo Client setup
│   │   │   ├── queries.js          ← GraphQL query definitions
│   │   │   ├── mutations.js        ← GraphQL mutation definitions
│   │   │   └── subscriptions.js    ← GraphQL subscription definitions
│   │   │
│   │   └── types/                  ← TypeScript-style JSDoc types
│   │
│   ├── public/                     ← Static assets
│   ├── index.html                  ← HTML shell
│   ├── vite.config.js              ← Dev server + proxy config
│   ├── tailwind.config.js          ← CSS framework config
│   ├── postcss.config.js
│   ├── package.json
│   └── Dockerfile                  ← Node build + Nginx serve
│
└── docs/
    └── DOCUMENTATION.md            ← This file
```

---

*Documentation reflects the codebase as of the current state. All architectural decisions, security choices, and implementation details described here are directly derived from the source code.*
