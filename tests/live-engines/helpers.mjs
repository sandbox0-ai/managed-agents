import Anthropic from '@anthropic-ai/sdk';
import assert from 'node:assert/strict';

export const gatewayBaseURL = process.env.MANAGED_AGENTS_E2E_BASE_URL?.trim();
export const gatewayToken = (process.env.MANAGED_AGENTS_E2E_TOKEN || process.env.SANDBOX0_API_KEY)?.trim();

export const liveClaude = {
  baseURL: process.env.MANAGED_AGENT_LIVE_CLAUDE_BASE_URL?.trim(),
  model: process.env.MANAGED_AGENT_LIVE_CLAUDE_MODEL?.trim(),
  token: process.env.MANAGED_AGENT_LIVE_CLAUDE_TOKEN?.trim(),
};

export const liveCodex = {
  baseURL: process.env.MANAGED_AGENT_LIVE_CODEX_BASE_URL?.trim(),
  model: process.env.MANAGED_AGENT_LIVE_CODEX_MODEL?.trim(),
  token: process.env.MANAGED_AGENT_LIVE_CODEX_TOKEN?.trim(),
};

export function liveSkipReason(config) {
  if (!gatewayBaseURL || !gatewayToken) {
    return 'set MANAGED_AGENTS_E2E_BASE_URL and MANAGED_AGENTS_E2E_TOKEN to run live engine tests';
  }
  if (!config.baseURL || !config.model || !config.token) {
    return `set live engine env for ${config.engine}`;
  }
  return false;
}

export function sdkClient() {
  return new Anthropic({
    baseURL: gatewayBaseURL,
    apiKey: gatewayToken,
    timeout: 90_000,
    maxRetries: 0,
  });
}

export function suffix() {
  return `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
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

export async function createLiveSessionFixture(client, t, config) {
  const runID = suffix();
  const cleanup = createCleanup();

  const environment = await client.beta.environments.create({
    name: `live-engines-env-${runID}`,
    config: {
      type: 'cloud',
      networking: { type: 'unrestricted' },
      packages: { type: 'packages' },
    },
    metadata: { e2e: 'live-engines', run: runID, engine: config.engine },
  });
  cleanup.add(() => client.beta.environments.delete(environment.id));

  const agent = await client.beta.agents.create({
    name: `live-engines-agent-${config.engine}-${runID}`,
    model: { id: config.model },
    system: 'Reply with exactly the requested token and nothing else.',
    tools: [{
      type: 'agent_toolset_20260401',
      default_config: {
        enabled: true,
        permission_policy: { type: 'always_allow' },
      },
    }],
    metadata: { e2e: 'live-engines', run: runID, engine: config.engine },
  });
  cleanup.add(() => client.beta.agents.archive(agent.id));

  const vault = await client.beta.vaults.create({
    display_name: `live-engines-${config.engine}-llm-${runID}`,
    metadata: {
      'sandbox0.managed_agents.role': 'llm',
      'sandbox0.managed_agents.engine': config.engine,
      'sandbox0.managed_agents.llm_base_url': config.baseURL,
      e2e: 'live-engines',
      run: runID,
      engine: config.engine,
    },
  });
  cleanup.add(() => client.beta.vaults.delete(vault.id));

  await client.beta.vaults.credentials.create(vault.id, {
    display_name: `live-engines-token-${config.engine}-${runID}`,
    auth: {
      type: 'static_bearer',
      token: config.token,
    },
    metadata: { e2e: 'live-engines', run: runID, engine: config.engine },
  });

  const session = await client.beta.sessions.create({
    agent: agent.id,
    environment_id: environment.id,
    title: `live-engines-session-${config.engine}-${runID}`,
    vault_ids: [vault.id],
    metadata: { e2e: 'live-engines', run: runID, engine: config.engine },
  });
  cleanup.add(() => client.beta.sessions.delete(session.id));

  t.after(() => cleanup.run());
  return { runID, cleanup, environment, agent, vault, session };
}

export async function collectAsyncIterable(iterable, limit = 50) {
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

export async function waitForCompletedTurn(client, sessionID, timeoutMs = 300_000) {
  return withTimeout((async () => {
    for (;;) {
      const events = await collectAsyncIterable(client.beta.sessions.events.list(sessionID, { limit: 100, order: 'asc' }), 100);
      if (events.some((event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'end_turn')) {
        return events;
      }
      const terminal = events.find((event) => event.type === 'session.error' || event.type === 'session.status_terminated');
      if (terminal) {
        throw new Error(`session ${sessionID} ended without a successful turn: ${JSON.stringify(terminal)}`);
      }
      await new Promise((resolve) => setTimeout(resolve, 1000));
    }
  })(), timeoutMs, `session ${sessionID} turn`);
}

export function textFromAgentMessages(events) {
  return events
    .filter((event) => event?.type === 'agent.message')
    .flatMap((event) => Array.isArray(event.content) ? event.content : [])
    .filter((block) => block?.type === 'text' && typeof block.text === 'string')
    .map((block) => block.text)
    .join('\n');
}

export async function runLiveTurn(client, sessionID, expectedText, { timeoutMs } = {}) {
  const sent = await client.beta.sessions.events.send(sessionID, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: `Reply with exactly ${expectedText} and nothing else.` }],
    }],
  });
  assert.equal(sent.data?.[0]?.type, 'user.message');

  const events = await waitForCompletedTurn(client, sessionID, timeoutMs);
  const responseText = textFromAgentMessages(events);
  assert.match(responseText, new RegExp(`\\b${expectedText}\\b`));
  assert.doesNotMatch(responseText, /fake-wrapper response:/);
  assert(events.some((event) => event.type === 'span.model_request_start'));

  const modelRequestEnd = events.find((event) => event.type === 'span.model_request_end');
  assert(modelRequestEnd, `missing span.model_request_end in ${JSON.stringify(events)}`);
  const usage = modelRequestEnd.model_usage ?? {};
  assert(
    Number(usage.input_tokens ?? 0) > 0 || Number(usage.output_tokens ?? 0) > 0,
    `expected token usage in ${JSON.stringify(modelRequestEnd)}`,
  );

  const session = await client.beta.sessions.retrieve(sessionID);
  assert(
    Number(session.usage?.input_tokens ?? 0) > 0 || Number(session.usage?.output_tokens ?? 0) > 0,
    `expected persisted usage in ${JSON.stringify(session.usage)}`,
  );

  return { events, session, responseText };
}
