# Sandbox0 Managed Agents

Sandbox0 Managed Agents is built on [sandbox0](https://github.com/sandbox0-ai/sandbox0), the AI agent sandbox runtime that provides persistent storage, session state, and fast sandbox startup for managed agent workloads.

Use the official Anthropic SDK and point it at the Sandbox0 Managed Agents API:

- Managed Agents API: `https://agents.sandbox0.ai`
- Agent engine: configured by vault metadata
- LLM host and token: configured by an LLM vault and its `static_bearer` credential

For Claude agents, Sandbox0 expects an Anthropic-compatible LLM API. The LLM vault is identified by these reserved metadata keys:

```json
{
  "sandbox0.managed_agents.role": "llm",
  "sandbox0.managed_agents.engine": "claude",
  "sandbox0.managed_agents.llm_base_url": "https://api.anthropic.com"
}
```

The credential in that vault should be an unbound `static_bearer` credential. Do not set `mcp_server_url` for the LLM credential.

## Copy-paste demo

```bash
mkdir sandbox0-managed-agents-demo
cd sandbox0-managed-agents-demo
npm init -y
npm install @anthropic-ai/sdk

cat > demo.mjs <<'EOF'
import Anthropic from '@anthropic-ai/sdk';

function requireEnv(name) {
  const value = process.env[name]?.trim();
  if (!value) throw new Error(`Set ${name}`);
  return value;
}

function env(name, fallback) {
  return process.env[name]?.trim() || fallback;
}

async function listSessionEvents(client, sessionID) {
  const events = [];
  for await (const event of client.beta.sessions.events.list(sessionID, { limit: 100 })) {
    events.push(event);
  }
  return events;
}

function agentText(events) {
  return events
    .filter((event) => event?.type === 'agent.message')
    .flatMap((event) => Array.isArray(event.content) ? event.content : [])
    .filter((block) => block?.type === 'text' && typeof block.text === 'string')
    .map((block) => block.text.trim())
    .filter(Boolean)
    .join('\n');
}

const client = new Anthropic({
  baseURL: 'https://agents.sandbox0.ai',
  apiKey: requireEnv('SANDBOX0_API_KEY'),
});

const llmBaseURL = env('ANTHROPIC_BASE_URL', 'https://api.anthropic.com');
const model = env('ANTHROPIC_MODEL', 'claude-sonnet-4-20250514');
const suffix = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

const environment = await client.beta.environments.create({
  name: `sandbox0-demo-env-${suffix}`,
  config: {
    type: 'cloud',
    networking: { type: 'unrestricted' },
    packages: { type: 'packages' },
  },
});

const agent = await client.beta.agents.create({
  name: `Sandbox0 Demo Agent ${suffix}`,
  model: { id: model },
  system: 'Reply in one short sentence.',
  tools: [{ type: 'agent_toolset_20260401' }],
});

const llmVault = await client.beta.vaults.create({
  display_name: `Claude LLM ${suffix}`,
  metadata: {
    'sandbox0.managed_agents.role': 'llm',
    // Only the Claude engine is supported today. Codex is reserved for future OpenAI-compatible support.
    'sandbox0.managed_agents.engine': 'claude',
    'sandbox0.managed_agents.llm_base_url': llmBaseURL,
  },
});

await client.beta.vaults.credentials.create(llmVault.id, {
  display_name: 'Anthropic-compatible API key',
  auth: {
    type: 'static_bearer',
    token: requireEnv('ANTHROPIC_API_KEY'),
  },
});

const session = await client.beta.sessions.create({
  agent: agent.id,
  environment_id: environment.id,
  title: `Sandbox0 SDK demo ${suffix}`,
  vault_ids: [llmVault.id],
});

await client.beta.sessions.events.send(session.id, {
  events: [{
    type: 'user.message',
    content: [{ type: 'text', text: 'Say: hello from Sandbox0 managed agents' }],
  }],
});

const deadline = Date.now() + 120_000;
while (Date.now() < deadline) {
  const events = await listSessionEvents(client, session.id);
  const text = agentText(events);
  if (text) {
    console.log(text);
    process.exit(0);
  }
  await new Promise((resolve) => setTimeout(resolve, 2_000));
}

throw new Error(`Timed out waiting for agent.message in session ${session.id}`);
EOF

SANDBOX0_API_KEY='s0_...' \
ANTHROPIC_API_KEY='sk-ant-...' \
node demo.mjs
```

To use an Anthropic-compatible proxy, set the LLM host separately from the Sandbox0 API host:

```bash
SANDBOX0_API_KEY='s0_...' \
ANTHROPIC_API_KEY='proxy-token' \
ANTHROPIC_BASE_URL='https://anthropic-proxy.example.com' \
node demo.mjs
```

For Z.ai or another Anthropic-compatible LLM provider, keep `SANDBOX0_API_KEY` pointed at Sandbox0 and replace only the LLM provider token, base URL, and model. For example, Z.ai documents the Claude Code/Goose Anthropic-compatible endpoint as `https://api.z.ai/api/anthropic`:

```bash
SANDBOX0_API_KEY='s0_...' \
ANTHROPIC_API_KEY='zai-token' \
ANTHROPIC_BASE_URL='https://api.z.ai/api/anthropic' \
ANTHROPIC_MODEL='glm-4.7' \
node demo.mjs
```

The provider must implement the Anthropic API shape. OpenAI-compatible providers are not supported through the Claude engine; Codex/OpenAI-compatible engine support will be added separately.

In TypeScript, the official SDK types may still require `mcp_server_url` for `static_bearer`. The runtime request should still omit it for the LLM vault credential; cast that `auth` object if needed.
