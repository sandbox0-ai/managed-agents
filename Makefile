HELM ?= helm
GO ?= env GOWORK=off go
NPM ?= npm
DOCKER ?= docker

HELM_RELEASE ?= managed-agents
KUBE_NAMESPACE ?= sandbox0-system
CHART_DIR ?= ./chart

IMAGE_REPOSITORY ?= sandbox0ai/managed-agents
GATEWAY_TAG ?= gateway-testenv
WRAPPER_TAG ?= wrapper-testenv
FAKE_WRAPPER_IMAGE ?= managed-agents/fake-wrapper:e2e
SDK_GO_DIR ?= ../sdk-go

SANDBOX0_BASE_URL ?= https://api.sandbox0.ai
RUNTIME_CALLBACK_BASE_URL ?=
INGRESS_ENABLED ?= false
INGRESS_CLASS_NAME ?= nginx
INGRESS_HOST ?= agents.sandbox0.ai
INGRESS_TLS_SECRET_NAME ?= sandbox0-ai-wildcard-tls

HELM_SET_ARGS := \
	--set-string agentGateway.image.repository=$(IMAGE_REPOSITORY) \
	--set-string agentGateway.image.tag=$(GATEWAY_TAG) \
	--set-string agentGateway.image.pullPolicy=IfNotPresent \
	--set-string agentGateway.env.sandbox0BaseURL=$(SANDBOX0_BASE_URL) \
	--set-string agentGateway.env.runtimeCallbackBaseURL=$(RUNTIME_CALLBACK_BASE_URL) \
	--set-string agentGateway.env.templateMainImage=$(IMAGE_REPOSITORY):$(WRAPPER_TAG) \
	--set agentGateway.ingress.enabled=$(INGRESS_ENABLED) \
	--set-string agentGateway.ingress.className=$(INGRESS_CLASS_NAME) \
	--set-string agentGateway.ingress.hosts[0].host=$(INGRESS_HOST) \
	--set-string agentGateway.ingress.hosts[0].paths[0].path=/ \
	--set-string agentGateway.ingress.hosts[0].paths[0].pathType=Prefix \
	--set-string agentGateway.ingress.tls[0].secretName=$(INGRESS_TLS_SECRET_NAME) \
	--set-string agentGateway.ingress.tls[0].hosts[0]=$(INGRESS_HOST)
.PHONY: verify verify-format verify-tidy generate verify-generated test-unit test-integration test-wrapper test-e2e docker-build-gateway docker-build-wrapper docker-build-fake-wrapper helm-lint helm-template helm-upgrade

verify: verify-format verify-tidy verify-generated test-unit test-wrapper helm-lint helm-template

verify-format:
	@files="$$(git ls-files '*.go')"; \
	if [ -z "$$files" ]; then exit 0; fi; \
	unformatted="$$(gofmt -l $$files)"; \
	if [ -n "$$unformatted" ]; then \
		echo "Unformatted Go files:"; \
		echo "$$unformatted"; \
		exit 1; \
	fi

verify-tidy:
	$(GO) mod tidy
	git diff --exit-code -- go.mod go.sum

generate:
	$(GO) generate ./internal/apicontract/generated

verify-generated: generate
	git diff --exit-code -- internal/apicontract/generated

test-unit:
	@packages="$$( $(GO) list ./... | grep -v '/tests/e2e$$' )"; \
	GOTOOLCHAIN=go1.25.0+auto $(GO) test -race -count=1 $$packages

test-integration:
	@test -n "$$TEST_DATABASE_URL" || { echo "TEST_DATABASE_URL is required"; exit 1; }
	GOTOOLCHAIN=go1.25.0+auto $(GO) test -race -count=1 ./internal/managedagents/... ./cmd/...

test-wrapper:
	cd agent-wrapper && $(NPM) ci && $(NPM) test -- --test-reporter=spec

test-e2e:
	GOTOOLCHAIN=go1.25.0+auto $(GO) test -count=1 -v ./tests/e2e/... -timeout=20m

docker-build-gateway:
	@test -f "$(SDK_GO_DIR)/go.mod" || { echo "SDK_GO_DIR must point to sdk-go checkout"; exit 1; }
	DOCKER_BUILDKIT=1 $(DOCKER) buildx build --load \
		-t $(IMAGE_REPOSITORY):$(GATEWAY_TAG) \
		-f Dockerfile \
		--build-context sdk-go=$(SDK_GO_DIR) \
		.

docker-build-wrapper:
	DOCKER_BUILDKIT=1 $(DOCKER) buildx build --load \
		-t $(IMAGE_REPOSITORY):$(WRAPPER_TAG) \
		-f agent-wrapper/Dockerfile.wrapper \
		agent-wrapper

docker-build-fake-wrapper:
	$(DOCKER) build -t $(FAKE_WRAPPER_IMAGE) tests/e2e/fake-wrapper

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
