# Multi-stage container image for the ai-factory Go control plane + PydanticAI worker.
# Targets Kubernetes / AKS deployment (docs/deployment-k8s-aks.md).

FROM golang:1.27-bookworm AS go-builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd/ ./cmd/
COPY internal/ ./internal/
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/factory ./cmd/factory

FROM python:3.13-slim-bookworm
RUN apt-get update \
  && apt-get install -y --no-install-recommends git ca-certificates curl \
  && rm -rf /var/lib/apt/lists/* \
  && useradd --create-home --uid 10001 factory

COPY --from=ghcr.io/astral-sh/uv:0.13.9 /uv /usr/local/bin/uv
COPY --from=go-builder /out/factory /usr/local/bin/factory

WORKDIR /app
COPY agents_py/pyproject.toml agents_py/uv.lock agents_py/README.md ./agents_py/
COPY agents_py/src/ ./agents_py/src/
COPY prompts/ ./prompts/
RUN uv sync --directory /app/agents_py --locked --no-dev --no-editable

ENV FACTORY_METRICS_ADDR=:9091 \
    FACTORY_LOG_FORMAT=json \
    PYTHONDONTWRITEBYTECODE=1 \
    PYTHONUNBUFFERED=1

USER 10001
EXPOSE 9091
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD curl -fsS http://127.0.0.1:9091/healthz || exit 1

ENTRYPOINT ["/usr/local/bin/factory"]
CMD ["run", "--mode", "review"]
