package telemetry

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
	"go.opentelemetry.io/otel/trace"
	temporalotel "go.temporal.io/sdk/contrib/opentelemetry"
)

func TestResolveTracesEndpointPreventsPathDoubling(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"http://phoenix.localhost:8080":            "http://phoenix.localhost:8080/v1/traces",
		"http://phoenix.localhost:8080/":           "http://phoenix.localhost:8080/v1/traces",
		"http://phoenix.localhost:8080/v1/traces":  "http://phoenix.localhost:8080/v1/traces",
		"http://phoenix.localhost:8080/v1/traces/": "http://phoenix.localhost:8080/v1/traces",
	}
	for in, want := range cases {
		got, err := ResolveTracesEndpoint(in)
		if err != nil {
			t.Fatalf("ResolveTracesEndpoint(%q) unexpected error: %v", in, err)
		}
		if got != want {
			t.Fatalf("ResolveTracesEndpoint(%q) = %q, want %q", in, got, want)
		}
	}

	if _, err := ResolveTracesEndpoint("not-a-url"); err == nil {
		t.Fatalf("expected invalid endpoint error")
	}
}

func TestSetupTracingAndPerIssueIsolation(t *testing.T) {
	exp := tracetest.NewInMemoryExporter()
	tp, shutdown, err := SetupTracing(context.Background(), TracingConfig{
		Enabled:     true,
		ServiceName: "ai-factory-controlplane",
		ProjectName: "ai-factory",
		Exporter:    exp,
	})
	if err != nil {
		t.Fatalf("SetupTracing failed: %v", err)
	}

	// Verify Temporal interceptor initializes cleanly with our TracerProvider
	if _, err := temporalotel.NewTracingInterceptor(temporalotel.TracerOptions{
		Tracer: tp.Tracer("ai-factory.temporal"),
	}); err != nil {
		t.Fatalf("NewTracingInterceptor failed: %v", err)
	}

	tracer := tp.Tracer("test")
	ctx1, sp1 := tracer.Start(context.Background(), "issue-1",
		trace.WithNewRoot(),
		trace.WithAttributes(attribute.Int("issue.number", 1)))
	tid1 := CurrentTraceID(ctx1)
	sp1.End()

	ctx2, sp2 := tracer.Start(context.Background(), "issue-2",
		trace.WithNewRoot(),
		trace.WithAttributes(attribute.Int("issue.number", 2)))
	tid2 := CurrentTraceID(ctx2)
	sp2.End()

	if err := tp.ForceFlush(context.Background()); err != nil {
		t.Fatalf("force flush failed: %v", err)
	}
	defer func() {
		if err := shutdown(context.Background()); err != nil {
			t.Errorf("shutdown failed: %v", err)
		}
	}()

	if tid1 == "" || tid2 == "" || tid1 == tid2 {
		t.Fatalf("expected two distinct non-empty trace IDs, got %q and %q", tid1, tid2)
	}

	spans := exp.GetSpans()
	if len(spans) != 2 {
		t.Fatalf("expected 2 exported spans, got %d", len(spans))
	}
	var foundProject bool
	for _, attr := range spans[0].Resource.Attributes() {
		if string(attr.Key) == "openinference.project.name" && attr.Value.AsString() == "ai-factory" {
			foundProject = true
		}
	}
	if !foundProject {
		t.Fatalf("expected openinference.project.name=ai-factory on exported span resource")
	}
}
