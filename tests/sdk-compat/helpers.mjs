import Anthropic, { toFile } from '@anthropic-ai/sdk';
import assert from 'node:assert/strict';

export const baseURL = process.env.MANAGED_AGENTS_E2E_BASE_URL?.trim();
export const apiKey = (process.env.MANAGED_AGENTS_E2E_TOKEN || process.env.SANDBOX0_API_KEY)?.trim();
export const skipReason = !baseURL || !apiKey
  ? 'set MANAGED_AGENTS_E2E_BASE_URL and MANAGED_AGENTS_E2E_TOKEN to run SDK compatibility tests'
  : false;

export function sdkClient() {
  return new Anthropic({
    baseURL,
    apiKey,
    timeout: 90_000,
    maxRetries: 0,
  });
}

export function suffix() {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
}

export function environmentBody(runID, overrides = {}) {
  return {
    name: `sdk-compat-env-${runID}`,
    config: {
      type: 'cloud',
      networking: { type: 'unrestricted' },
      packages: { type: 'packages' },
    },
    metadata: { e2e: 'sdk-compat', run: runID },
    ...overrides,
  };
}

export function agentBody(runID, overrides = {}) {
  return {
    name: `sdk-compat-agent-${runID}`,
    model: { id: 'claude-sonnet-4-20250514' },
    system: 'Reply with one short sentence.',
    tools: [{
      type: 'agent_toolset_20260401',
      default_config: {
        enabled: true,
        permission_policy: { type: 'always_allow' },
      },
    }],
    metadata: { e2e: 'sdk-compat', run: runID },
    ...overrides,
  };
}

export function llmVaultBody(runID, engine = 'claude') {
  const llmBaseURL = engine === 'codex' ? 'https://api.openai.com/v1' : 'https://api.anthropic.com';
  return {
    display_name: `sdk-compat-${engine}-llm-${runID}`,
    metadata: {
      'sandbox0.managed_agents.role': 'llm',
      'sandbox0.managed_agents.engine': engine,
      'sandbox0.managed_agents.llm_base_url': llmBaseURL,
      e2e: 'sdk-compat',
      run: runID,
    },
  };
}

export async function collectAsyncIterable(iterable, limit = 20) {
  const out = [];
  for await (const item of iterable) {
    out.push(item);
    if (out.length >= limit) {
      break;
    }
  }
  return out;
}

export async function withTimeout(promise, ms, label) {
  let timer;
  try {
    return await Promise.race([
      promise,
      new Promise((_, reject) => {
        timer = setTimeout(() => reject(new Error(`${label} timed out after ${ms}ms`)), ms);
      }),
    ]);
  } finally {
    clearTimeout(timer);
  }
}

export async function waitForEvents(client, sessionID, predicate, { timeoutMs = 45_000, limit = 50 } = {}) {
  return withTimeout((async () => {
    for (;;) {
      const events = await collectAsyncIterable(client.beta.sessions.events.list(sessionID, { limit: 20, order: 'asc' }), limit);
      if (predicate(events)) {
        return events;
      }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }
  })(), timeoutMs, `session ${sessionID} events`);
}

export function textFromAgentMessages(events) {
  return events
    .filter((event) => event?.type === 'agent.message')
    .flatMap((event) => Array.isArray(event.content) ? event.content : [])
    .filter((block) => block?.type === 'text' && typeof block.text === 'string')
    .map((block) => block.text)
    .join('\n');
}

export function createCleanup() {
  const cleanup = [];
  return {
    add(fn) {
      cleanup.push(fn);
    },
    async run() {
      for (const fn of cleanup.reverse()) {
        try {
          await fn();
        } catch {
          // Best-effort cleanup must not hide the test failure.
        }
      }
    },
  };
}

export async function createBasicFixture(client, runID, cleanup, { engine = 'claude', agent = {} } = {}) {
  const environment = await client.beta.environments.create(environmentBody(runID));
  cleanup.add(() => client.beta.environments.delete(environment.id));

  const createdAgent = await client.beta.agents.create(agentBody(runID, agent));
  cleanup.add(() => client.beta.agents.archive(createdAgent.id));

  const vault = await client.beta.vaults.create(llmVaultBody(runID, engine));
  cleanup.add(() => client.beta.vaults.delete(vault.id));

  const credential = await client.beta.vaults.credentials.create(vault.id, {
    display_name: `sdk-compat-token-${runID}`,
    auth: {
      type: 'static_bearer',
      token: 'fake-model-token',
    },
    metadata: { e2e: 'sdk-compat', run: runID },
  });

  return { environment, agent: createdAgent, vault, credential };
}

export async function createSessionFixture(client, t, { engine = 'claude', agent = {} } = {}) {
  const runID = suffix();
  const cleanup = createCleanup();
  const fixture = await createBasicFixture(client, runID, cleanup, { engine, agent });
  const session = await client.beta.sessions.create({
    agent: fixture.agent.id,
    environment_id: fixture.environment.id,
    title: `sdk-compat-session-${runID}`,
    vault_ids: [fixture.vault.id],
    metadata: { e2e: 'sdk-compat', run: runID },
  });
  cleanup.add(() => client.beta.sessions.delete(session.id));
  t.after(() => cleanup.run());
  return { runID, cleanup, session, ...fixture };
}

export async function uploadTextFile(client, filename, text, mimeType = 'text/plain') {
  const file = await toFile(Buffer.from(text, 'utf8'), filename, { type: mimeType });
  return client.beta.files.upload({ file });
}

export function assertFound(items, predicate, label) {
  assert(items.some(predicate), `${label} not found in ${JSON.stringify(items)}`);
}
