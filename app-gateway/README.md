# Managed Agents App Gateway

`app-gateway` lets external app protocols connect to user-created Sandbox0 Managed Agents.

The first adapter targets Remodex. Remodex compatibility is implemented by vendoring the public Remodex bridge and relay source as a protocol SDK under `vendor/remodex`, then replacing the local `codex app-server` side with a Managed Agents session adapter.

## Runtime Boundary

- Managed Agents owns agents, sessions, events, runtime lifecycle, and sandbox ownership.
- App Gateway owns app protocol pairing, transport, and message translation.
- Sandbox fallback operations must be session-scoped. App Gateway must not create independent sandboxes for app requests.

## Remodex Flow

1. Create an app binding for an existing Managed Agents `agent_id` and `environment_id`.
2. Start a Remodex bridge session for that binding.
3. Scan the returned Remodex pairing payload in the app.
4. Remodex connects through the built-in relay and secure transport.
5. Codex app-server-like JSON-RPC is translated to Managed Agents sessions/events.

## Remodex Compatibility

Supported in this service:

- Secure Remodex pairing, trusted-device state, reconnect lookup, and encrypted relay forwarding.
- Thread list/start/resume/read, turn start/interrupt, streaming agent messages, tool calls, tool results, status changes, and tool approval confirmations.
- Account, model, context-window, push-registration, and desktop-preference RPCs that can be answered without a local desktop.

Not yet supported:

- `git/*` and `workspace/*` RPCs that require direct filesystem access. The current Managed Agents SDK does not expose a session-scoped filesystem API, so these return `sandbox_fallback_unavailable` instead of creating a second sandbox or mutating unrelated state.
- `voice/transcribe` and local Mac handoff RPCs, because they depend on local Codex desktop credentials or macOS APIs.

## Environment

- `APP_GATEWAY_HTTP_ADDR`: listen address, default `127.0.0.1:8787`
- `APP_GATEWAY_STATE_DIR`: JSON state directory, default `.app-gateway-state`
- `APP_GATEWAY_PUBLIC_RELAY_URL`: public relay base, for example `wss://agents.example.com/relay`
- `APP_GATEWAY_LOCAL_RELAY_URL`: bridge-side relay base, default derived from listen port
- `APP_GATEWAY_AUTH_TOKEN`: optional bearer token for App Gateway API
- `MANAGED_AGENT_BASE_URL`: Managed Agents API URL
- `MANAGED_AGENT_API_KEY` or `MANAGED_AGENT_AUTH_TOKEN`: Managed Agents credential

## API

```bash
curl -X POST http://127.0.0.1:8787/v1/app-bindings \
  -H 'content-type: application/json' \
  -d '{
    "app": "remodex",
    "external_id": "demo-device",
    "agent_id": "agent_...",
    "environment_id": "env_...",
    "vault_ids": ["vault_..."],
    "session_scope": "thread"
  }'

curl -X POST http://127.0.0.1:8787/v1/app-bindings/appb_.../remodex/start
```

The start response contains `pairing_payload` and `pairing_code`.
