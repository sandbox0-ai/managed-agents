HELM ?= helm

HELM_RELEASE ?= managed-agents
KUBE_NAMESPACE ?= sandbox0-system
CHART_DIR ?= ./chart

IMAGE_REPOSITORY ?= sandbox0ai/managed-agents
GATEWAY_TAG ?= gateway-testenv
WRAPPER_TAG ?= wrapper-testenv

SANDBOX0_BASE_URL ?= http://fullmode-cluster-gateway.sandbox0-system.svc.cluster.local:30080
RUNTIME_REGION_ID ?= default

HELM_SET_ARGS := \
	--set-string agentGateway.image.repository=$(IMAGE_REPOSITORY) \
	--set-string agentGateway.image.tag=$(GATEWAY_TAG) \
	--set-string agentGateway.image.pullPolicy=IfNotPresent \
	--set-string agentGateway.env.sandbox0BaseURL=$(SANDBOX0_BASE_URL) \
	--set-string agentGateway.env.runtimeRegionID=$(RUNTIME_REGION_ID) \
	--set-string agentGateway.env.templateMainImage=$(IMAGE_REPOSITORY):$(WRAPPER_TAG) \
	--set-string agentGateway.env.templateSidecarImage=$(IMAGE_REPOSITORY):$(WRAPPER_TAG)

.PHONY: helm-lint helm-template helm-upgrade

helm-lint:
	$(HELM) lint $(CHART_DIR)

helm-template:
	$(HELM) template $(HELM_RELEASE) $(CHART_DIR) \
		-n $(KUBE_NAMESPACE) \
		$(HELM_SET_ARGS)

helm-upgrade:
	$(HELM) upgrade --install $(HELM_RELEASE) $(CHART_DIR) \
		-n $(KUBE_NAMESPACE) \
		--create-namespace \
		$(HELM_SET_ARGS) \
		$(HELM_EXTRA_ARGS)
