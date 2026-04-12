HELM ?= helm

HELM_RELEASE ?= managed-agents
KUBE_NAMESPACE ?= sandbox0-system
CHART_DIR ?= ./chart

IMAGE_REPOSITORY ?= sandbox0ai/managed-agents
GATEWAY_TAG ?= gateway-testenv
WRAPPER_TAG ?= wrapper-testenv

SANDBOX0_BASE_URL ?= https://api.sandbox0.ai
SANDBOX0_TLS_INSECURE_SKIP_VERIFY ?= false
RUNTIME_PROXY_BASE_URL ?=
SANDBOX0_HOST_ALIAS_IP ?=
SANDBOX0_HOST_ALIAS_HOST ?=
INGRESS_ENABLED ?= false
INGRESS_CLASS_NAME ?= nginx
INGRESS_HOST ?= agents.sandbox0.ai
INGRESS_TLS_SECRET_NAME ?= sandbox0-ai-wildcard-tls

HELM_SET_ARGS := \
	--set-string agentGateway.image.repository=$(IMAGE_REPOSITORY) \
	--set-string agentGateway.image.tag=$(GATEWAY_TAG) \
	--set-string agentGateway.image.pullPolicy=IfNotPresent \
	--set-string agentGateway.env.sandbox0BaseURL=$(SANDBOX0_BASE_URL) \
	--set-string agentGateway.env.sandbox0TLSInsecureSkipVerify=$(SANDBOX0_TLS_INSECURE_SKIP_VERIFY) \
	--set-string agentGateway.env.runtimeProxyBaseURL=$(RUNTIME_PROXY_BASE_URL) \
	--set-string agentGateway.env.templateMainImage=$(IMAGE_REPOSITORY):$(WRAPPER_TAG) \
	--set agentGateway.ingress.enabled=$(INGRESS_ENABLED) \
	--set-string agentGateway.ingress.className=$(INGRESS_CLASS_NAME) \
	--set-string agentGateway.ingress.hosts[0].host=$(INGRESS_HOST) \
	--set-string agentGateway.ingress.hosts[0].paths[0].path=/ \
	--set-string agentGateway.ingress.hosts[0].paths[0].pathType=Prefix \
	--set-string agentGateway.ingress.tls[0].secretName=$(INGRESS_TLS_SECRET_NAME) \
	--set-string agentGateway.ingress.tls[0].hosts[0]=$(INGRESS_HOST)

HELM_OPTIONAL_SET_ARGS :=
ifneq ($(strip $(SANDBOX0_HOST_ALIAS_IP)),)
HELM_OPTIONAL_SET_ARGS += --set-string agentGateway.hostAliases[0].ip=$(SANDBOX0_HOST_ALIAS_IP)
endif
ifneq ($(strip $(SANDBOX0_HOST_ALIAS_HOST)),)
HELM_OPTIONAL_SET_ARGS += --set-string agentGateway.hostAliases[0].hostnames[0]=$(SANDBOX0_HOST_ALIAS_HOST)
endif

.PHONY: helm-lint helm-template helm-upgrade

helm-lint:
	$(HELM) lint $(CHART_DIR)

helm-template:
	$(HELM) template $(HELM_RELEASE) $(CHART_DIR) \
		-n $(KUBE_NAMESPACE) \
		$(HELM_SET_ARGS) \
		$(HELM_OPTIONAL_SET_ARGS)

helm-upgrade:
	$(HELM) upgrade --install $(HELM_RELEASE) $(CHART_DIR) \
		-n $(KUBE_NAMESPACE) \
		--create-namespace \
		$(HELM_SET_ARGS) \
		$(HELM_OPTIONAL_SET_ARGS) \
		$(HELM_EXTRA_ARGS)
