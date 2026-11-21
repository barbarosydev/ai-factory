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

// Registry collects thread-safe runtime metrics for workflows, agent LLM calls, and Playwright runs,
// exposing them in OpenMetrics/Prometheus text format alongside Kubernetes health/readiness probes.
type Registry struct {
	ready atomic.Bool

	mu                    sync.Mutex
	workflowsTotal        map[string]uint64
	workflowDurationSum   float64
	workflowDurationCount uint64
	repairAttemptsTotal   uint64

	agentCallsTotal    map[agentCallKey]uint64
	agentTokensTotal   map[agentTokenKey]uint64
	agentDurationSum   map[string]float64
	agentDurationCount map[string]uint64

	playwrightRunsTotal     map[bool]uint64
	playwrightDurationSum   float64
	playwrightDurationCount uint64
}

type agentCallKey struct {
	role   string
	model  string
	status string
}

type agentTokenKey struct {
	role  string
	model string
	kind  string // "prompt" | "completion"
}

// NewRegistry creates an initialized telemetry registry.
func NewRegistry() *Registry {
	return &Registry{
		workflowsTotal:      make(map[string]uint64),
		agentCallsTotal:     make(map[agentCallKey]uint64),
		agentTokensTotal:    make(map[agentTokenKey]uint64),
		agentDurationSum:    make(map[string]float64),
		agentDurationCount:  make(map[string]uint64),
		playwrightRunsTotal: make(map[bool]uint64),
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

// RecordAgentCall records one LLM gateway completion per agent role and model route.
func (r *Registry) RecordAgentCall(role, model string, promptTokens, completionTokens int, d time.Duration, err error) {
	if r == nil {
		return
	}
	status := "ok"
	if err != nil {
		status = "error"
	}
	if role == "" {
		role = "unknown"
	}
	if model == "" {
		model = "unknown"
	}

	r.mu.Lock()
	defer r.mu.Unlock()
	r.agentCallsTotal[agentCallKey{role: role, model: model, status: status}]++
	if promptTokens > 0 {
		r.agentTokensTotal[agentTokenKey{role: role, model: model, kind: "prompt"}] += uint64(promptTokens)
	}
	if completionTokens > 0 {
		r.agentTokensTotal[agentTokenKey{role: role, model: model, kind: "completion"}] += uint64(completionTokens)
	}
	r.agentDurationSum[role] += d.Seconds()
	r.agentDurationCount[role]++
}

// RecordPlaywrightRun records a single Playwright verification run outcome and elapsed duration.
func (r *Registry) RecordPlaywrightRun(passed bool, durationMs int64) {
	if r == nil {
		return
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.playwrightRunsTotal[passed]++
	if durationMs > 0 {
		r.playwrightDurationSum += float64(durationMs) / 1000.0
	}
	r.playwrightDurationCount++
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

	b.WriteString("# HELP ai_factory_agent_calls_total LLM completion requests by role, model, and status.\n")
	b.WriteString("# TYPE ai_factory_agent_calls_total counter\n")
	callKeys := make([]agentCallKey, 0, len(r.agentCallsTotal))
	for k := range r.agentCallsTotal {
		callKeys = append(callKeys, k)
	}
	sort.Slice(callKeys, func(i, j int) bool {
		if callKeys[i].role != callKeys[j].role {
			return callKeys[i].role < callKeys[j].role
		}
		if callKeys[i].model != callKeys[j].model {
			return callKeys[i].model < callKeys[j].model
		}
		return callKeys[i].status < callKeys[j].status
	})
	for _, k := range callKeys {
		fmt.Fprintf(&b, "ai_factory_agent_calls_total{role=%q,model=%q,status=%q} %d\n",
			k.role, k.model, k.status, r.agentCallsTotal[k])
	}

	b.WriteString("# HELP ai_factory_agent_tokens_total Tokens consumed per agent role, model, and token kind.\n")
	b.WriteString("# TYPE ai_factory_agent_tokens_total counter\n")
	tokKeys := make([]agentTokenKey, 0, len(r.agentTokensTotal))
	for k := range r.agentTokensTotal {
		tokKeys = append(tokKeys, k)
	}
	sort.Slice(tokKeys, func(i, j int) bool {
		if tokKeys[i].role != tokKeys[j].role {
			return tokKeys[i].role < tokKeys[j].role
		}
		if tokKeys[i].model != tokKeys[j].model {
			return tokKeys[i].model < tokKeys[j].model
		}
		return tokKeys[i].kind < tokKeys[j].kind
	})
	for _, k := range tokKeys {
		fmt.Fprintf(&b, "ai_factory_agent_tokens_total{role=%q,model=%q,kind=%q} %d\n",
			k.role, k.model, k.kind, r.agentTokensTotal[k])
	}

	b.WriteString("# HELP ai_factory_agent_latency_seconds Cumulative LLM inference latency per agent role.\n")
	b.WriteString("# TYPE ai_factory_agent_latency_seconds summary\n")
	roleKeys := make([]string, 0, len(r.agentDurationCount))
	for k := range r.agentDurationCount {
		roleKeys = append(roleKeys, k)
	}
	sort.Strings(roleKeys)
	for _, k := range roleKeys {
		fmt.Fprintf(&b, "ai_factory_agent_latency_seconds_sum{role=%q} %.6f\n", k, r.agentDurationSum[k])
		fmt.Fprintf(&b, "ai_factory_agent_latency_seconds_count{role=%q} %d\n", k, r.agentDurationCount[k])
	}

	b.WriteString("# HELP ai_factory_playwright_runs_total Deterministic Playwright suite runs by outcome.\n")
	b.WriteString("# TYPE ai_factory_playwright_runs_total counter\n")
	for _, passed := range []bool{false, true} {
		if cnt, ok := r.playwrightRunsTotal[passed]; ok {
			fmt.Fprintf(&b, "ai_factory_playwright_runs_total{passed=%q} %d\n", fmt.Sprintf("%t", passed), cnt)
		}
	}

	b.WriteString("# HELP ai_factory_playwright_duration_seconds Total Playwright verification duration in seconds.\n")
	b.WriteString("# TYPE ai_factory_playwright_duration_seconds summary\n")
	fmt.Fprintf(&b, "ai_factory_playwright_duration_seconds_sum %.6f\n", r.playwrightDurationSum)
	fmt.Fprintf(&b, "ai_factory_playwright_duration_seconds_count %d\n", r.playwrightDurationCount)

	return b.String()
}

func writeProbeJSON(w http.ResponseWriter, code int, status string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"status": status})
}
