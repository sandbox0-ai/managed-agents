#!/usr/bin/env bash
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SANDBOX0_DIR="${SANDBOX0_DIR:-${ROOT_DIR}/../sandbox0}"
KIND_CLUSTER_NAME="${KIND_CLUSTER_NAME:-managed-agents-e2e}"
SANDBOX0_NAMESPACE="${SANDBOX0_NAMESPACE:-sandbox0-system}"
SANDBOX0_INFRA_NAME="${SANDBOX0_INFRA_NAME:-volumes}"
MANAGED_AGENTS_NAMESPACE="${MANAGED_AGENTS_NAMESPACE:-sandbox0-cloud}"
IMAGE_REPOSITORY="${IMAGE_REPOSITORY:-sandbox0ai/managed-agents}"
GATEWAY_TAG="${GATEWAY_TAG:-gateway-testenv}"
FAKE_WRAPPER_IMAGE="${FAKE_WRAPPER_IMAGE:-managed-agents/fake-wrapper:e2e}"
E2E_ENV_FILE="${E2E_ENV_FILE:-${RUNNER_TEMP:-/tmp}/managed-agents-e2e.env}"

require_file() {
  if [[ ! -f "$1" ]]; then
    echo "missing required file: $1" >&2
    exit 1
  fi
}

require_file "${SANDBOX0_DIR}/infra-operator/chart/Chart.yaml"
require_file "${SANDBOX0_DIR}/infra-operator/chart/samples/single-cluster/volumes.yaml"

if ! kind get clusters | grep -qx "${KIND_CLUSTER_NAME}"; then
  kind create cluster --name "${KIND_CLUSTER_NAME}" --config "${ROOT_DIR}/tests/e2e/kind-config.yaml"
fi

docker pull sandbox0ai/infra:latest
docker pull postgres:16-alpine
docker pull rustfs/rustfs:1.0.0-alpha.79
docker pull registry:2.8.3
docker pull sandbox0ai/otemplates:default-v0.1.0

kind load docker-image sandbox0ai/infra:latest --name "${KIND_CLUSTER_NAME}"
kind load docker-image postgres:16-alpine --name "${KIND_CLUSTER_NAME}"
kind load docker-image rustfs/rustfs:1.0.0-alpha.79 --name "${KIND_CLUSTER_NAME}"
kind load docker-image registry:2.8.3 --name "${KIND_CLUSTER_NAME}"
kind load docker-image sandbox0ai/otemplates:default-v0.1.0 --name "${KIND_CLUSTER_NAME}"
kind load docker-image "${IMAGE_REPOSITORY}:${GATEWAY_TAG}" --name "${KIND_CLUSTER_NAME}"
kind load docker-image "${FAKE_WRAPPER_IMAGE}" --name "${KIND_CLUSTER_NAME}"

helm upgrade --install infra-operator "${SANDBOX0_DIR}/infra-operator/chart" \
  -n infra-operator \
  --create-namespace \
  --set-string image.repository=sandbox0ai/infra \
  --set-string image.tag=latest \
  --set image.pullPolicy=IfNotPresent \
  --wait \
  --timeout=5m

kubectl apply -f "${SANDBOX0_DIR}/infra-operator/chart/samples/single-cluster/volumes.yaml"
kubectl -n "${SANDBOX0_NAMESPACE}" wait --for=condition=Ready "sandbox0infra/${SANDBOX0_INFRA_NAME}" --timeout=10m
kubectl -n "${SANDBOX0_NAMESPACE}" rollout status "deploy/${SANDBOX0_INFRA_NAME}-cluster-gateway" --timeout=5m
kubectl -n "${SANDBOX0_NAMESPACE}" rollout status "deploy/${SANDBOX0_INFRA_NAME}-manager" --timeout=5m
kubectl -n "${SANDBOX0_NAMESPACE}" rollout status "deploy/${SANDBOX0_INFRA_NAME}-storage-proxy" --timeout=5m

password="$(kubectl -n "${SANDBOX0_NAMESPACE}" get secret admin-password -o jsonpath='{.data.password}' | base64 -d)"
token="$(curl --retry 20 --retry-all-errors --retry-delay 3 -fsS \
  -H 'content-type: application/json' \
  -d "{\"email\":\"admin@example.com\",\"password\":\"${password}\"}" \
  "http://127.0.0.1:30080/auth/login" | jq -r '.data.access_token // .access_token')"
if [[ -z "${token}" || "${token}" == "null" ]]; then
  echo "failed to resolve sandbox0 login token" >&2
  exit 1
fi

kubectl create namespace "${MANAGED_AGENTS_NAMESPACE}" --dry-run=client -o yaml | kubectl apply -f -
kubectl -n "${MANAGED_AGENTS_NAMESPACE}" apply -f - <<'YAML'
apiVersion: apps/v1
kind: Deployment
metadata:
  name: managed-agents-postgres
spec:
  replicas: 1
  selector:
    matchLabels:
      app.kubernetes.io/name: managed-agents-postgres
  template:
    metadata:
      labels:
        app.kubernetes.io/name: managed-agents-postgres
    spec:
      containers:
        - name: postgres
          image: postgres:16-alpine
          imagePullPolicy: IfNotPresent
          ports:
            - containerPort: 5432
          env:
            - name: POSTGRES_DB
              value: managed_agents
            - name: POSTGRES_USER
              value: managed_agents
            - name: POSTGRES_PASSWORD
              value: managed_agents
          readinessProbe:
            exec:
              command: ["pg_isready", "-U", "managed_agents", "-d", "managed_agents"]
            periodSeconds: 2
            timeoutSeconds: 2
            failureThreshold: 30
---
apiVersion: v1
kind: Service
metadata:
  name: managed-agents-postgres
spec:
  selector:
    app.kubernetes.io/name: managed-agents-postgres
  ports:
    - name: postgres
      port: 5432
      targetPort: 5432
YAML
kubectl -n "${MANAGED_AGENTS_NAMESPACE}" rollout status deploy/managed-agents-postgres --timeout=3m

database_url="postgres://managed_agents:managed_agents@managed-agents-postgres.${MANAGED_AGENTS_NAMESPACE}.svc.cluster.local:5432/managed_agents?sslmode=disable"
kubectl -n "${MANAGED_AGENTS_NAMESPACE}" create secret generic managed-agents-runtime \
  --from-literal=MANAGED_AGENT_DATABASE_URL="${database_url}" \
  --from-literal=MANAGED_AGENT_SANDBOX0_ADMIN_API_KEY="${token}" \
  --dry-run=client -o yaml | kubectl apply -f -

make -C "${ROOT_DIR}" helm-upgrade \
  HELM_RELEASE=managed-agents \
  KUBE_NAMESPACE="${MANAGED_AGENTS_NAMESPACE}" \
  IMAGE_REPOSITORY="${IMAGE_REPOSITORY}" \
  GATEWAY_TAG="${GATEWAY_TAG}" \
  WRAPPER_TAG="${FAKE_WRAPPER_IMAGE#*:}" \
  SANDBOX0_BASE_URL="http://${SANDBOX0_INFRA_NAME}-cluster-gateway.${SANDBOX0_NAMESPACE}.svc.cluster.local:30080" \
  RUNTIME_CALLBACK_BASE_URL="http://managed-agents-agent-gateway.${MANAGED_AGENTS_NAMESPACE}.svc.cluster.local" \
  HELM_EXTRA_ARGS="--set-string agentGateway.env.templateID=managed-agents-ci --set-string agentGateway.env.templateMainImage=${FAKE_WRAPPER_IMAGE} --set-string agentGateway.env.sandboxTTLSeconds=120 --set-string agentGateway.secretKeys.sandbox0AdminAPIKey.secretName=managed-agents-runtime --set-string agentGateway.secretKeys.sandbox0AdminAPIKey.key=MANAGED_AGENT_SANDBOX0_ADMIN_API_KEY"

kubectl -n "${MANAGED_AGENTS_NAMESPACE}" rollout status deploy/managed-agents-agent-gateway --timeout=5m

{
  printf 'MANAGED_AGENTS_E2E_BASE_URL=http://127.0.0.1:18088\n'
  printf 'MANAGED_AGENTS_E2E_TOKEN=%s\n' "${token}"
} > "${E2E_ENV_FILE}"
echo "wrote ${E2E_ENV_FILE}"

