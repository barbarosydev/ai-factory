package telemetry

import (
	"context"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.26.0"
	"go.opentelemetry.io/otel/trace"
)

// TracingConfig configures OpenTelemetry trace export for the Go control plane.
type TracingConfig struct {
	Enabled      bool
	BaseEndpoint string
	ServiceName  string
	ProjectName  string
	Logger       *slog.Logger
	Exporter     sdktrace.SpanExporter
}

// ResolveTracesEndpoint normalizes either an OTLP base URL or a full /v1/traces URL
// into the exact single /v1/traces HTTP ingestion URL without doubling the path.
func ResolveTracesEndpoint(raw string) (string, error) {
	trimmed := strings.TrimSpace(raw)
	if trimmed == "" {
		return "", fmt.Errorf("OTLP endpoint must not be empty")
	}
	u, err := url.Parse(trimmed)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("invalid OTLP HTTP endpoint %q: expected http(s)://host[:port]", raw)
	}
	cleanPath := strings.TrimRight(u.Path, "/")
	if !strings.HasSuffix(cleanPath, "/v1/traces") {
		cleanPath += "/v1/traces"
	}
	u.Path = cleanPath
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// SetupTracing initializes OpenTelemetry W3C TraceContext propagation and bounded OTLP export.
// Exporter outages log rate-limited warnings and never fail workflow or activity execution.
func SetupTracing(ctx context.Context, cfg TracingConfig) (*sdktrace.TracerProvider, func(context.Context) error, error) {
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(propagation.TraceContext{}))

	if !cfg.Enabled {
		noopTP := sdktrace.NewTracerProvider(sdktrace.WithSampler(sdktrace.NeverSample()))
		return noopTP, func(context.Context) error { return nil }, nil
	}

	serviceName := strings.TrimSpace(cfg.ServiceName)
	if serviceName == "" {
		serviceName = "ai-factory-controlplane"
	}
	projectName := strings.TrimSpace(cfg.ProjectName)
	if projectName == "" {
		projectName = "ai-factory"
	}

	var lastWarnNs atomic.Int64
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) {
		if cfg.Logger == nil || err == nil {
			return
		}
		now := time.Now().UnixNano()
		prev := lastWarnNs.Load()
		if now-prev >= int64(30*time.Second) && lastWarnNs.CompareAndSwap(prev, now) {
			cfg.Logger.Warn("otel trace export warning (continuing factory work)", "error", err)
		}
	}))

	exp := cfg.Exporter
	if exp == nil {
		tracesURL, err := ResolveTracesEndpoint(cfg.BaseEndpoint)
		if err != nil {
			return nil, nil, err
		}
		exp, err = otlptracehttp.New(
			ctx,
			otlptracehttp.WithEndpointURL(tracesURL),
			otlptracehttp.WithTimeout(3*time.Second),
		)
		if err != nil {
			return nil, nil, fmt.Errorf("create OTLP HTTP trace exporter: %w", err)
		}
	}

	res := resource.NewSchemaless(
		semconv.ServiceName(serviceName),
		semconv.ServiceVersion("0.3.0"),
		attribute.String("deployment.environment", "local"),
		attribute.String("openinference.project.name", projectName),
	)

	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		sdktrace.WithSampler(sdktrace.AlwaysSample()),
		sdktrace.WithSpanProcessor(
			sdktrace.NewBatchSpanProcessor(
				exp,
				sdktrace.WithMaxQueueSize(512),
				sdktrace.WithMaxExportBatchSize(128),
				sdktrace.WithExportTimeout(3*time.Second),
			),
		),
	)
	otel.SetTracerProvider(tp)

	shutdown := func(shutCtx context.Context) error {
		c, cancel := context.WithTimeout(shutCtx, 3*time.Second)
		defer cancel()
		return tp.Shutdown(c)
	}
	return tp, shutdown, nil
}

// CurrentTraceID returns the 32-char lowercase hex trace ID from ctx when valid and sampled.
func CurrentTraceID(ctx context.Context) string {
	sc := trace.SpanContextFromContext(ctx)
	if !sc.IsValid() || !sc.IsSampled() {
		return ""
	}
	return sc.TraceID().String()
}
