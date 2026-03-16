// Package tracing bootstraps OpenTelemetry with a Jaeger exporter and provides
// helpers that make trace-context propagation across HTTP and Redis Streams easy.
//
// What is distributed tracing?
// In a distributed system, a single user request may touch many processes:
// the HTTP API creates a task, publishes it to Redis, and a separate worker process
// picks it up and processes it.  Without tracing, you only see isolated log lines in
// each process.  Distributed tracing stitches those log lines into a single timeline
// called a "trace" — showing exactly how much time each step took and where errors occurred.
//
// What is a span?
// A span is one named, timed operation.  Examples:
//   - "POST /api/v1/tasks"  — the HTTP handler span (starts when request arrives, ends when response is sent)
//   - "storage.CreateTask"  — a child span for the DB write
//   - "broker.Publish"      — a child span for the Redis Streams publish
//   - "worker.handleTask"   — a span in the worker process
//
// A trace is the tree of all spans that share the same TraceID.  Jaeger renders this
// as a waterfall diagram showing the parent–child relationships and durations.
//
// What is OpenTelemetry, and why not use the Jaeger SDK directly?
// OpenTelemetry (OTel) is a vendor-neutral observability standard.  By coding against
// the OTel API (go.opentelemetry.io/otel), this service is not locked to Jaeger — you can
// swap the exporter for Zipkin, Honeycomb, or Datadog by changing one line in Init().
// A vendor-specific SDK would require touching every tracing call if you ever switch backends.
//
// What does Jaeger do with trace data?
// Jaeger is an open-source distributed tracing backend.  The collector at
// http://jaeger:14268/api/traces receives span data over HTTP (Thrift format).
// Jaeger stores spans and exposes a UI at http://jaeger:16686 where you can:
//   - Search traces by service name, operation, tags, or time range.
//   - See the full waterfall of a request across all services.
//   - Identify slow spans, error spans, and retry loops.
//
// How trace context propagates across services:
// The W3C Trace Context standard defines two HTTP headers:
//   - traceparent: carries the trace ID and parent span ID
//   - tracestate:  carries vendor-specific state
//
// When the API publishes a task to Redis Streams, InjectToMap() writes these headers
// as extra fields on the stream message.  When the worker reads the message, it calls
// ExtractFromMap() to reconstruct the parent span context, then starts a new child span.
// Both spans share the same TraceID, so Jaeger links them into one end-to-end trace.
//
// Why trace IDs connect logs to traces:
// Structured log entries (e.g. slog.With("trace_id", span.SpanContext().TraceID()))
// embed the trace ID from the active span.  This lets you jump from a log line in
// Grafana/Loki directly to the matching Jaeger trace — without manually correlating
// timestamps across dashboards.
//
// Cross-process flow:
//
//	API server                    Redis Stream message          Worker
//	─────────────────────────     ─────────────────────────     ──────────────────────
//	Start HTTP span           →   InjectToMap → traceparent →   ExtractFromMap
//	Start "submit task" span                                     Start "process task" span
//
// Both spans carry the same trace ID, so Jaeger links them into one end-to-end trace.
//
// Connected to:
//   - cmd/api/main.go    — calls Init("task-queue-api"), then tracing.Start() in middleware
//   - cmd/worker/main.go — calls Init("task-queue-worker"), then tracing.ExtractFromMap + Start
//   - broker (Redis)     — InjectToMap/ExtractFromMap bridge the trace across stream messages
package tracing

import (
	"context"
	"fmt"
	"os"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/jaeger"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/trace"
)

// instrumentationScope is the name that identifies this library/package within OTel traces.
// It appears as the "library name" on spans in the Jaeger UI, helping distinguish spans
// created by this package from spans created by third-party instrumentation libraries.
const instrumentationScope = "distributed-task-queue"

// Init bootstraps a Jaeger-backed TracerProvider and registers it as the global OTel
// provider so every otel.Tracer() call in this process shares the same backend.
//
// Why register globally?
// OTel's global provider is a process-wide singleton.  By registering once in Init(),
// all instrumented libraries (HTTP middleware, DB drivers, etc.) automatically use the
// same exporter without needing to pass the provider through every call chain.
//
// Returns a shutdown function that flushes buffered spans before the process exits —
// always call it: defer shutdown(ctx).
// Without calling shutdown, spans buffered in memory may be lost when the process exits,
// creating incomplete traces in Jaeger.
//
// Parameters:
//   - serviceName: the name that appears in Jaeger's "Service" dropdown (e.g. "task-queue-api")
//
// Configuration via environment:
//
//	JAEGER_ENDPOINT  HTTP Thrift collector URL (default: http://jaeger:14268/api/traces)
//	                 Use the Docker Compose service name "jaeger" in containers.
//
// Called by: cmd/api/main.go and cmd/worker/main.go at process startup.
func Init(serviceName string) (shutdown func(context.Context) error, err error) {
	// Read the Jaeger collector URL from the environment; fall back to the Docker Compose
	// service name so this works out of the box in the local development stack.
	endpoint := os.Getenv("JAEGER_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://jaeger:14268/api/traces" // Docker Compose service name
	}

	// Create a Jaeger exporter that sends spans over HTTP to the collector.
	// Jaeger also supports gRPC (port 14250) and Thrift UDP (port 6831), but HTTP is
	// the most reliable option in containerized environments where UDP may be dropped.
	exp, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(endpoint)))
	if err != nil {
		// A failed exporter means all traces will be dropped — still non-fatal
		// (tracing is observability, not critical path), but reported to the caller.
		return nil, fmt.Errorf("jaeger exporter: %w", err)
	}

	// resource identifies this service in Jaeger's UI service selector.
	// service.name is the most important attribute — it's what you search by in Jaeger.
	// service.version helps distinguish traces from old deployments vs new ones.
	res, _ := resource.New(context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),    // appears in the Jaeger "Service" dropdown
			attribute.String("service.version", "1.0.0"),    // helps identify which build produced a trace
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),                     // async batching — spans are buffered and sent in bulk; won't block HTTP handlers
		sdktrace.WithResource(res),                    // attaches service.name and service.version to every span in this process
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // 100% sampling in dev; in production use TraceIDRatioBased(0.01) for 1% sampling
	)

	// Register as the global provider — all otel.Tracer() calls in this process now use tp.
	otel.SetTracerProvider(tp)

	// W3C Trace Context + Baggage — the standard for cross-service propagation.
	// TraceContext handles the "traceparent" and "tracestate" headers.
	// Baggage carries user-defined key-value pairs across service boundaries.
	// Using the composite propagator means both are injected/extracted in one call.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{}, // W3C traceparent / tracestate headers
		propagation.Baggage{},      // W3C Baggage header for cross-service key-value metadata
	))

	// Return tp.Shutdown so the caller can flush spans on process exit.
	return tp.Shutdown, nil
}

// Start begins a new span as a child of any span already in ctx.
//
// If ctx contains an active span (from a parent operation), the new span becomes
// a child of it — inheriting the same TraceID.  This is how the span hierarchy
// (waterfall) in Jaeger is built.
//
// Caller MUST call span.End() when the operation completes, typically via defer:
//
//	ctx, span := tracing.Start(ctx, "storage.GetTask")
//	defer span.End()
//
// Parameters:
//   - ctx:  context carrying the parent span (if any)
//   - name: operation name shown in Jaeger (e.g. "worker.handleTask")
//   - opts: optional span attributes, kind (Server/Client/Internal), etc.
//
// Returns:
//   - context.Context: new context with the started span stored inside
//   - trace.Span:      the span — call span.End() when done, span.RecordError() on failure
//
// Called throughout cmd/api/main.go (tracing middleware) and cmd/worker/main.go.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	// instrumentationScope groups all spans from this package together in Jaeger.
	return otel.Tracer(instrumentationScope).Start(ctx, name, opts...)
}

// InjectToMap writes the current span context (traceparent + tracestate) from ctx
// into the provided map. Use this when publishing to Redis Streams so the worker
// can reconstruct the parent span and continue the same distributed trace.
//
// How it works:
//  1. Creates a mapCarrier (a string map that implements OTel's TextMapCarrier interface).
//  2. Calls the global propagator's Inject() — this writes "traceparent" and optionally
//     "tracestate" keys into the mapCarrier.
//  3. Copies those string values into the caller's map[string]interface{}.
//
// The Redis Stream message ends up with extra fields like:
//
//	"traceparent" → "00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01"
//	"tracestate"  → "" (empty unless Baggage is set)
//
// Example usage in broker.Publish:
//
//	values := map[string]interface{}{"task": taskJSON, "priority": task.Priority}
//	tracing.InjectToMap(ctx, values)
//	client.XAdd(ctx, &redis.XAddArgs{Values: values})
//
// Called by: broker.Publish when putting a task message onto the Redis Stream.
func InjectToMap(ctx context.Context, m map[string]interface{}) {
	mc := make(mapCarrier)
	// Inject writes traceparent/tracestate into mc using the global W3C propagator.
	otel.GetTextMapPropagator().Inject(ctx, mc)
	// Copy string values from the carrier into the caller's mixed map.
	for k, v := range mc {
		m[k] = v
	}
}

// ExtractFromMap reads traceparent/tracestate from a Redis Stream message's Values
// map and returns a context that is a child of the remote span.
//
// How it works:
//  1. Copies only string values from the message map into a mapCarrier.
//     (Redis Stream values come back as map[string]interface{} because the
//      client doesn't know the types — non-string values like ints are ignored.)
//  2. Calls the global propagator's Extract() — this reads traceparent and rebuilds
//     the remote span context.
//  3. Returns a new context carrying that remote span context as the parent.
//
// After calling this, any span started with tracing.Start(ctx, ...) will be a child
// of the API's span, linking the worker's work to the original HTTP request in Jaeger.
//
// Example usage in the worker:
//
//	ctx = tracing.ExtractFromMap(workerCtx, message.Values)
//	ctx, span := tracing.Start(ctx, "worker.processTask")
//
// Called by: broker (Redis Streams consumer) when reading a task message.
func ExtractFromMap(ctx context.Context, m map[string]interface{}) context.Context {
	// Build a string-only carrier from the mixed-type Redis message values.
	mc := make(mapCarrier, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			// Only string values are valid trace headers; skip ints, JSON objects, etc.
			mc[k] = s
		}
	}
	// Extract rebuilds the remote SpanContext from the traceparent header in mc.
	return otel.GetTextMapPropagator().Extract(ctx, mc)
}

// ExtractFromHeaders reads trace context from HTTP headers captured as map[string]string
// (e.g. from a Fiber request). Returns an enriched context that is a child of any
// upstream span — used by the Fiber tracing middleware in the API server.
//
// When an upstream load balancer or the browser's OpenTelemetry JS SDK sends a request
// with a "traceparent" header, this function extracts that span context so the API
// server's spans become children of the client-side trace.
//
// Parameters:
//   - ctx:     the base context to enrich with the extracted span context
//   - headers: HTTP request headers as a plain string map
//
// Called by: tracingMiddleware in cmd/api/main.go.
func ExtractFromHeaders(ctx context.Context, headers map[string]string) context.Context {
	// mapCarrier(headers) is a type conversion (not allocation) — reuses the existing map.
	return otel.GetTextMapPropagator().Extract(ctx, mapCarrier(headers))
}

// mapCarrier implements propagation.TextMapCarrier over a plain map[string]string.
//
// Why a custom carrier?
// OTel's TextMapPropagator requires a carrier that supports Get, Set, and Keys.
// The standard library provides httptrace.Header for net/http, but this service
// needs to work with two other transports:
//   - Redis Stream message maps (map[string]interface{})
//   - Fiber's request header map (map[string]string)
//
// mapCarrier bridges both by implementing the three-method TextMapCarrier interface
// over a simple map[string]string, without importing net/http.
//
// Used by: InjectToMap, ExtractFromMap, and ExtractFromHeaders.
type mapCarrier map[string]string

// Get returns the value for a given header key.
// Called by the propagator when extracting trace context from an incoming message.
func (m mapCarrier) Get(key string) string { return m[key] }

// Set stores a header key-value pair in the carrier.
// Called by the propagator when injecting trace context into an outgoing message.
func (m mapCarrier) Set(key, val string) { m[key] = val }

// Keys returns all keys currently stored in the carrier.
// Required by the TextMapCarrier interface; used by the propagator to enumerate
// which headers it should extract when reading an incoming message.
func (m mapCarrier) Keys() []string {
	// Pre-allocate with the exact capacity to avoid growing the slice.
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
