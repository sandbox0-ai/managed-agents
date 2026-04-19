#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-managed-agents-e2e}"
KUBE_CONTEXT="${KUBE_CONTEXT:-kind-${KIND_CLUSTER_NAME}}"
MANAGED_AGENTS_NAMESPACE="${MANAGED_AGENTS_NAMESPACE:-sandbox0-cloud}"
E2E_ENV_FILE="${E2E_ENV_FILE:-${RUNNER_TEMP:-/tmp}/managed-agents-e2e.env}"

if [[ -f "${E2E_ENV_FILE}" ]]; then
  set -a
  # shellcheck disable=SC1090
  source "${E2E_ENV_FILE}"
  set +a
fi

: "${MANAGED_AGENT_LIVE_CLAUDE_BASE_URL:?MANAGED_AGENT_LIVE_CLAUDE_BASE_URL is required}"
: "${MANAGED_AGENT_LIVE_CLAUDE_MODEL:?MANAGED_AGENT_LIVE_CLAUDE_MODEL is required}"
: "${MANAGED_AGENT_LIVE_CLAUDE_TOKEN:?MANAGED_AGENT_LIVE_CLAUDE_TOKEN is required}"
: "${MANAGED_AGENT_LIVE_CODEX_BASE_URL:?MANAGED_AGENT_LIVE_CODEX_BASE_URL is required}"
: "${MANAGED_AGENT_LIVE_CODEX_MODEL:?MANAGED_AGENT_LIVE_CODEX_MODEL is required}"
: "${MANAGED_AGENT_LIVE_CODEX_TOKEN:?MANAGED_AGENT_LIVE_CODEX_TOKEN is required}"

kubectl --context "${KUBE_CONTEXT}" -n "${MANAGED_AGENTS_NAMESPACE}" port-forward svc/managed-agents-agent-gateway 18088:80 >"${RUNNER_TEMP:-/tmp}/managed-agents-port-forward.log" 2>&1 &
pf_pid=$!
trap 'kill ${pf_pid} >/dev/null 2>&1 || true' EXIT

for _ in {1..60}; do
  if curl -fsS "${MANAGED_AGENTS_E2E_BASE_URL}/healthz" >/dev/null 2>&1; then
    break
  fi
  sleep 1
done

make -C "${ROOT_DIR}" test-live-engines
