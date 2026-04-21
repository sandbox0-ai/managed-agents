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

kubectl --context "${KUBE_CONTEXT}" -n "${MANAGED_AGENTS_NAMESPACE}" port-forward svc/managed-agents-agent-gateway 18088:80 >"${RUNNER_TEMP:-/tmp}/managed-agents-port-forward.log" 2>&1 &
pf_pid=$!
trap 'kill ${pf_pid} >/dev/null 2>&1 || true' EXIT

ready=0
for _ in {1..60}; do
  if curl -fsS "${MANAGED_AGENTS_E2E_BASE_URL}/readyz" >/dev/null 2>&1; then
    ready=1
    break
  fi
  sleep 1
done

if [[ "${ready}" != "1" ]]; then
  echo "managed-agents agent-gateway never became ready" >&2
  exit 1
fi

make -C "${ROOT_DIR}" test-e2e
make -C "${ROOT_DIR}" test-sdk-compat
