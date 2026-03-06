// Package tracing bootstraps OpenTelemetry with a Jaeger exporter and provides
// helpers that make trace-context propagation across HTTP and Redis Streams easy.
//
// Cross-process flow:
//
//	API server                    Redis Stream message          Worker
//	─────────────────────────     ─────────────────────────     ──────────────────────
//	Start HTTP span           →   InjectToMap → traceparent →   ExtractFromMap
//	Start "submit task" span                                     Start "process task" span
//
// Both spans carry the same trace ID, so Jaeger links them into one end-to-end trace.
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

const instrumentationScope = "distributed-task-queue"

// Init bootstraps a Jaeger-backed TracerProvider and registers it as the global OTel
// provider so every otel.Tracer() call in this process shares the same backend.
//
// Returns a shutdown function that flushes buffered spans before the process exits —
// always call it: defer shutdown(ctx).
//
// Configuration via environment:
//
//	JAEGER_ENDPOINT  HTTP Thrift collector URL (default: http://jaeger:14268/api/traces)
func Init(serviceName string) (shutdown func(context.Context) error, err error) {
	endpoint := os.Getenv("JAEGER_ENDPOINT")
	if endpoint == "" {
		endpoint = "http://jaeger:14268/api/traces" // Docker Compose service name
	}

	exp, err := jaeger.New(jaeger.WithCollectorEndpoint(jaeger.WithEndpoint(endpoint)))
	if err != nil {
		return nil, fmt.Errorf("jaeger exporter: %w", err)
	}

	// resource identifies this service in Jaeger's UI service selector.
	res, _ := resource.New(context.Background(),
		resource.WithAttributes(
			attribute.String("service.name", serviceName),
			attribute.String("service.version", "1.0.0"),
		),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exp),                    // async — won't block HTTP handlers
		sdktrace.WithResource(res),                   // attaches service.name to every span
		sdktrace.WithSampler(sdktrace.AlwaysSample()), // 100% in dev; use TraceIDRatioBased in prod
	)

	otel.SetTracerProvider(tp)
	// W3C Trace Context + Baggage — the standard for cross-service propagation.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	return tp.Shutdown, nil
}

// Start begins a new span as a child of any span already in ctx.
// Caller MUST call span.End() when the operation completes.
func Start(ctx context.Context, name string, opts ...trace.SpanStartOption) (context.Context, trace.Span) {
	return otel.Tracer(instrumentationScope).Start(ctx, name, opts...)
}

// InjectToMap writes the current span context (traceparent + tracestate) from ctx
// into the provided map. Use this when publishing to Redis Streams so the worker
// can reconstruct the parent span and continue the same distributed trace.
//
//	values := map[string]interface{}{"task": taskJSON, "priority": task.Priority}
//	tracing.InjectToMap(ctx, values)
//	client.XAdd(ctx, &redis.XAddArgs{Values: values})
func InjectToMap(ctx context.Context, m map[string]interface{}) {
	mc := make(mapCarrier)
	otel.GetTextMapPropagator().Inject(ctx, mc)
	for k, v := range mc {
		m[k] = v
	}
}

// ExtractFromMap reads traceparent/tracestate from a Redis Stream message's Values
// map and returns a context that is a child of the remote span.
//
//	ctx = tracing.ExtractFromMap(workerCtx, message.Values)
//	ctx, span := tracing.Start(ctx, "worker.processTask")
func ExtractFromMap(ctx context.Context, m map[string]interface{}) context.Context {
	mc := make(mapCarrier, len(m))
	for k, v := range m {
		if s, ok := v.(string); ok {
			mc[k] = s
		}
	}
	return otel.GetTextMapPropagator().Extract(ctx, mc)
}

// ExtractFromHeaders reads trace context from HTTP headers captured as map[string]string
// (e.g. from a Fiber request). Returns an enriched context that is a child of any
// upstream span — used by the Fiber tracing middleware in the API server.
func ExtractFromHeaders(ctx context.Context, headers map[string]string) context.Context {
	return otel.GetTextMapPropagator().Extract(ctx, mapCarrier(headers))
}

// mapCarrier implements propagation.TextMapCarrier over a plain map[string]string.
// Lets us use OTel's standard propagation API with arbitrary key-value stores
// (Redis Stream message values, HTTP header maps) without needing net/http.Header.
type mapCarrier map[string]string

func (m mapCarrier) Get(key string) string { return m[key] }
func (m mapCarrier) Set(key, val string)   { m[key] = val }
func (m mapCarrier) Keys() []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	return keys
}
