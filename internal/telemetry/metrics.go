package telemetry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Registry collects control-plane workflow metrics and exposes them in Prometheus text format
// alongside Kubernetes health/readiness probes.
type Registry struct {
	ready atomic.Bool

	mu                    sync.Mutex
	workflowsTotal        map[string]uint64
	workflowDurationSum   float64
	workflowDurationCount uint64
	repairAttemptsTotal   uint64
}

// NewRegistry creates an initialized telemetry registry.
func NewRegistry() *Registry {
	return &Registry{
		workflowsTotal: make(map[string]uint64),
	}
}

// NewLogger creates a stdlib structured slog.Logger configured for text or JSON output.
func NewLogger(format string, out io.Writer) *slog.Logger {
	if out == nil {
		out = io.Discard
	}
	opts := &slog.HandlerOptions{Level: slog.LevelInfo}
	if strings.EqualFold(strings.TrimSpace(format), "json") {
		return slog.New(slog.NewJSONHandler(out, opts))
	}
	return slog.New(slog.NewTextHandler(out, opts))
}

// SetReady sets the worker readiness state reported by GET /readyz and ai_factory_ready.
func (r *Registry) SetReady(ready bool) {
	if r == nil {
		return
	}
	r.ready.Store(ready)
}

// IsReady returns whether the worker is ready to process Temporal tasks.
func (r *Registry) IsReady() bool {
	if r == nil {
		return false
	}
	return r.ready.Load()
}

// RecordWorkflow records a completed IssueFactoryWorkflow outcome, duration, and attempts count.
func (r *Registry) RecordWorkflow(finalState string, d time.Duration, attempts int) {
	if r == nil {
		return
	}
	state := strings.TrimSpace(finalState)
	if state == "" {
		state = "unknown"
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.workflowsTotal[state]++
	r.workflowDurationSum += d.Seconds()
	r.workflowDurationCount++
	if attempts > 0 {
		r.repairAttemptsTotal += uint64(attempts)
	}
}

// Handler returns an HTTP handler exposing /healthz, /readyz, and /metrics.
func (r *Registry) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		writeProbeJSON(w, http.StatusOK, "ok")
	})
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if r.IsReady() {
			writeProbeJSON(w, http.StatusOK, "ready")
			return
		}
		writeProbeJSON(w, http.StatusServiceUnavailable, "not_ready")
	})
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		_, _ = io.WriteString(w, r.RenderPrometheus())
	})
	return mux
}

// StartServer serves /healthz, /readyz, and /metrics until ctx is canceled.
func StartServer(ctx context.Context, addr string, reg *Registry, logger *slog.Logger) (func(), error) {
	if strings.TrimSpace(addr) == "" || strings.EqualFold(strings.TrimSpace(addr), "off") {
		return func() {}, nil
	}
	lc := net.ListenConfig{}
	ln, err := lc.Listen(ctx, "tcp", addr)
	if err != nil {
		return nil, fmt.Errorf("listen observability %s: %w", addr, err)
	}

	srv := &http.Server{
		Handler:           reg.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) && logger != nil {
			logger.Warn("observability server exited", "error", err)
		}
	}()

	if logger != nil {
		logger.Info("observability server listening", "addr", ln.Addr().String())
	}

	return func() {
		shutCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutCtx)
	}, nil
}

// RenderPrometheus renders all metrics deterministically in Prometheus text exposition format.
func (r *Registry) RenderPrometheus() string {
	r.mu.Lock()
	defer r.mu.Unlock()

	var b strings.Builder

	b.WriteString("# HELP ai_factory_ready Worker readiness state (1=ready, 0=starting).\n")
	b.WriteString("# TYPE ai_factory_ready gauge\n")
	readyVal := 0
	if r.IsReady() {
		readyVal = 1
	}
	fmt.Fprintf(&b, "ai_factory_ready %d\n", readyVal)

	b.WriteString("# HELP ai_factory_workflows_total Completed IssueFactoryWorkflow executions by final state.\n")
	b.WriteString("# TYPE ai_factory_workflows_total counter\n")
	wfKeys := make([]string, 0, len(r.workflowsTotal))
	for k := range r.workflowsTotal {
		wfKeys = append(wfKeys, k)
	}
	sort.Strings(wfKeys)
	for _, k := range wfKeys {
		fmt.Fprintf(&b, "ai_factory_workflows_total{final_state=%q} %d\n", k, r.workflowsTotal[k])
	}

	b.WriteString("# HELP ai_factory_workflow_duration_seconds Total workflow runtime in seconds.\n")
	b.WriteString("# TYPE ai_factory_workflow_duration_seconds summary\n")
	fmt.Fprintf(&b, "ai_factory_workflow_duration_seconds_sum %.6f\n", r.workflowDurationSum)
	fmt.Fprintf(&b, "ai_factory_workflow_duration_seconds_count %d\n", r.workflowDurationCount)

	b.WriteString("# HELP ai_factory_repair_attempts_total Total build/verify attempts executed across workflows.\n")
	b.WriteString("# TYPE ai_factory_repair_attempts_total counter\n")
	fmt.Fprintf(&b, "ai_factory_repair_attempts_total %d\n", r.repairAttemptsTotal)

	return b.String()
}

func writeProbeJSON(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
