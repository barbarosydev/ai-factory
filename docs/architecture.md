# ai-factory Architecture

`ai-factory` combines a Go control plane and a PydanticAI Python activity worker for the local AI Software Factory. It runs natively on macOS and connects [`ai-factory-infra`](https://github.com/barbarosydev/ai-factory-infra) (`temporal.localhost:7233`, `http://litellm.localhost:8080/v1`) with [`ai-factory-websites`](https://github.com/barbarosydev/ai-factory-websites).

## Three-Repository System View

```mermaid
flowchart LR
  subgraph INFRA["ai-factory-infra (k3d + Mac GPU)"]
    CNPG["CloudNativePG (Postgres 18)"]
    TEMP["Temporal Server (:7233 / :8080)"]
    LITE["LiteLLM Gateway (:8080/v1)"]
    PHX["Arize Phoenix OTLP & UI (phoenix.localhost:8080)"]
    OLLAMA["Ollama Native Metal GPU (gemma4:12b, gemma4:e4b)"]
    CNPG <--> TEMP
    CNPG <--> LITE
    CNPG <--> PHX
    LITE --> OLLAMA
  end

  subgraph FACTORY["ai-factory (Go Control Plane + Python Agents)"]
    POLLER["FIFO Issue & Comment Poller (15s + Actor Allowlist)"]
    WORKER["Go Control Worker (ai-factory-v1: Workflow, Git, GitHub)"]
    PYWORKER["PydanticAI Worker (ai-factory-agents: pm, dev, test, sec)"]
    POLLER --> TEMP
    TEMP <--> WORKER
    TEMP <-->|agent activity tasks + _tracer-data| PYWORKER
    PYWORKER -->|Per-agent virtual keys + x-litellm-tags| LITE
    WORKER -.->|OTLP /v1/traces| PHX
    PYWORKER -.->|OTLP + OpenInference /v1/traces| PHX
  end

  subgraph TARGET["ai-factory-websites (Target Monorepo)"]
    ISSUES["GitHub Issues & factory:* Labels"]
    WT[".worktrees/issue-N/sites/<slug>/ (SiteSandbox)"]
    PW["Root @playwright/test Suite (8 gates)"]
    PRS["PRs + ?raw=true Screenshots + CI"]
    POLLER <-->|GitHub App JWT + Token| ISSUES
    PYWORKER --> WT
    PYWORKER --> PW
    WORKER -->|GIT_ASKPASS + go-github| PRS
  end
```

## Issue Lifecycle State Machine

```mermaid
stateDiagram-v2
  [*] --> factory_ready: Issue form created or re-labeled
  factory_ready --> factory_running: Oldest ready issue dequeued (1 active)
  factory_running --> factory_rejected: PM classifies out of v1 scope
  factory_running --> factory_waiting: PM asks 1-3 clarification questions
  factory_waiting --> factory_running: Human comment or factory:ready -> SignalHumanReply
  factory_waiting --> factory_failed: 7-day timeout or 2 clarification rounds exceeded
  factory_running --> factory_review: Built, verified, PR opened (review mode)
  factory_running --> factory_done: Checks passed, squash-merged & remote branch deleted (auto mode)
  factory_running --> factory_failed: Exhausted 3 repair attempts (snapshot archived)
  factory_rejected --> [*]
  factory_review --> [*]
  factory_done --> [*]
```

## Package Architecture

| Package | Responsibility | Key Files |
| --- | --- | --- |
| **`cmd/factory`** | CLI (`doctor`, `run`, `once`, `status`, `clean-failed`) for the control plane | [main.go](../cmd/factory/main.go) |
| **`internal/controlplane`** | Temporal worker startup (`MaxConcurrentActivityExecutionSize: 1`), single-issue FIFO scheduler, signal bridge, shared GitHub/doctor/status probes | [runner.go](../internal/controlplane/runner.go), [doctor.go](../internal/controlplane/doctor.go), [status.go](../internal/controlplane/status.go), [github.go](../internal/controlplane/github.go) |
| **`internal/githubapp`** | Stdlib RS256 App JWT signer, 1-hour token cache, Issue Form markdown parser, `IssueController` (PR creation + Checks API wait + squash merge + branch cleanup) | [auth.go](../internal/githubapp/auth.go), [parser.go](../internal/githubapp/parser.go), [client.go](../internal/githubapp/client.go) |
| **`internal/workflow` & `internal/activities`** | Deterministic `IssueFactoryWorkflow` (`workflowcheck` verified), named cross-language agent activities, GitHub/worktree/PR control activities | [issue_workflow.go](../internal/workflow/issue_workflow.go), [activities.go](../internal/activities/activities.go) |
| **`internal/telemetry`** | OpenTelemetry OTLP HTTP setup, per-issue trace helpers, `/healthz`, `/readyz`, `/metrics`, and `slog` loggers | [tracing.go](../internal/telemetry/tracing.go), [metrics.go](../internal/telemetry/metrics.go) |
| **`agents_py/`** | PydanticAI Temporal Activity Worker on `ai-factory-agents`, OpenInference tracing, dual-mode `SandboxRunner`, direct PydanticAI tools, `SiteSandbox`, secret-scrubbed Playwright, fail-closed QA | [agents.py](../agents_py/src/agents_py/agents.py), [sandbox_runner.py](../agents_py/src/agents_py/sandbox_runner.py), [telemetry.py](../agents_py/src/agents_py/telemetry.py), [workspace.py](../agents_py/src/agents_py/workspace.py), [security.py](../agents_py/src/agents_py/security.py), [worker.py](../agents_py/src/agents_py/worker.py) |
| **`internal/gitworktree`** | Isolated `factory/issue-<N>` worktrees, diff isolation, ephemeral `GIT_ASKPASS` pushes, and failed snapshot archival (max 5 kept) | [manager.go](../internal/gitworktree/manager.go) |

## Design, Observability & Kubernetes Readiness

- **[Observability, Configurability & Agent Evals](observability-and-evals.md)** — Arize Phoenix OpenInference tracing, LiteLLM `/ui/usage/` correlation, Prometheus `/metrics`, `/healthz`, `/readyz`, structured `slog` JSON logs, and tunable `FACTORY_*` variables.
- **Tracing** — W3C `traceparent` propagates from Go through Temporal `_tracer-data` headers into Python PydanticAI + Playwright spans (`FACTORY_OTEL_CAPTURE_CONTENT=false` metadata-only by default), with `Trace ID` recorded on generated PRs ([ADR 0010](adr/0010-opentelemetry-and-phoenix-observability.md)).
- **[Running in Kubernetes / Migrating to AKS](deployment-k8s-aks.md)** — How the stateless Go worker, `/readyz` probes, and `FACTORY_MAX_CONCURRENT_ACTIVITIES` transition cleanly from local macOS host execution to AKS Pods.
- **[Architecture Decision Records](adr/README.md)** — ADRs `0001`–`0010`.
