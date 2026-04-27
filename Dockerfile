# syntax=docker/dockerfile:1.7

FROM golang:1.25-bookworm AS builder

ARG TARGETOS
ARG TARGETARCH

WORKDIR /workspace/managed-agent

COPY . .
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} \
	go build -trimpath -ldflags="-s -w" -o /out/agent-gateway ./cmd/agent-gateway

FROM debian:bookworm-slim

RUN apt-get update \
	&& apt-get install -y --no-install-recommends ca-certificates tzdata \
	&& rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY --from=builder /out/agent-gateway /usr/local/bin/agent-gateway

USER 65532:65532
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/agent-gateway"]
