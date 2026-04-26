#!/usr/bin/env node

const baseURL = trimTrailingSlash(process.env.MANAGED_AGENTS_E2E_BASE_URL || 'https://agents.sandbox0.ai');
const apiKey = (process.env.MANAGED_AGENTS_E2E_TOKEN || process.env.SANDBOX0_API_KEY || '').trim();
const beta = (process.env.MANAGED_AGENTS_E2E_BETA || 'managed-agents-2026-04-01').trim();
const keepResources = process.env.KEEP_DEBUG_RESOURCES === '1';
const packageName = (process.env.DEBUG_NPM_PACKAGE || 'tsx').trim();
const suffix = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;
const cleanup = [];
const results = [];

if (!apiKey) {
  throw new Error('set MANAGED_AGENTS_E2E_TOKEN or SANDBOX0_API_KEY');
}

try {
  const environment = await step('create environment with npm package', () => request('POST', '/v1/environments', {
    name: `debug-tsx-env-${suffix}`,
    config: {
      type: 'cloud',
      networking: { type: 'unrestricted' },
      packages: {
        type: 'packages',
        npm: [packageName],
      },
    },
    metadata: { debug: 'tsx-environment', run: suffix },
  }));
  cleanup.push(() => request('DELETE', `/v1/environments/${environment.id}`));

  const agent = await step('create agent', () => request('POST', '/v1/agents', {
    name: `debug-tsx-agent-${suffix}`,
    model: { id: 'claude-sonnet-4-20250514' },
    system: 'Reply with one short sentence.',
    tools: [{
      type: 'agent_toolset_20260401',
      default_config: {
        enabled: true,
        permission_policy: { type: 'always_allow' },
      },
    }],
    metadata: { debug: 'tsx-environment', run: suffix },
  }));
  cleanup.push(() => request('POST', `/v1/agents/${agent.id}/archive`, {}));

  const vault = await step('create llm vault', () => request('POST', '/v1/vaults', {
    display_name: `debug-tsx-llm-${suffix}`,
    metadata: {
      'sandbox0.managed_agents.role': 'llm',
      'sandbox0.managed_agents.engine': 'claude',
      'sandbox0.managed_agents.llm_base_url': 'https://api.anthropic.com',
      debug: 'tsx-environment',
      run: suffix,
    },
  }));
  cleanup.push(() => request('DELETE', `/v1/vaults/${vault.id}`));

  await step('create fake llm credential', () => request('POST', `/v1/vaults/${vault.id}/credentials`, {
    display_name: `debug-tsx-token-${suffix}`,
    auth: {
      type: 'static_bearer',
      token: 'fake-model-token',
    },
    metadata: { debug: 'tsx-environment', run: suffix },
  }));

  const session = await step('create session', () => request('POST', '/v1/sessions', {
    agent: agent.id,
    environment_id: environment.id,
    title: `debug-tsx-session-${suffix}`,
    vault_ids: [vault.id],
    metadata: { debug: 'tsx-environment', run: suffix },
  }), { timeoutMs: 180_000 });
  cleanup.push(() => request('DELETE', `/v1/sessions/${session.id}`));

  await step('retrieve session', () => request('GET', `/v1/sessions/${session.id}`));

  console.log(JSON.stringify({
    ok: true,
    run: suffix,
    package: packageName,
    environment_id: environment.id,
    agent_id: agent.id,
    vault_id: vault.id,
    session_id: session.id,
    session_status: session.status,
    results,
    kept_resources: keepResources,
  }, null, 2));
} catch (error) {
  console.error(JSON.stringify({
    ok: false,
    run: suffix,
    package: packageName,
    error: serializeError(error),
    results,
    kept_resources: keepResources,
  }, null, 2));
  process.exitCode = 1;
} finally {
  if (!keepResources) {
    for (const fn of cleanup.reverse()) {
      try {
        await fn();
      } catch {
        // Best-effort cleanup should not hide the original result.
      }
    }
  }
}

async function step(name, fn, options = {}) {
  const started = Date.now();
  try {
    const value = await fn();
    results.push({ name, ok: true, duration_ms: Date.now() - started });
    return value;
  } catch (error) {
    results.push({ name, ok: false, duration_ms: Date.now() - started, error: serializeError(error) });
    throw error;
  }
}

async function request(method, path, body, options = {}) {
  const controller = new AbortController();
  const timeout = setTimeout(() => controller.abort(), options.timeoutMs || 120_000);
  try {
    const response = await fetch(`${baseURL}${path}`, {
      method,
      signal: controller.signal,
      headers: {
        'Authorization': `Bearer ${apiKey}`,
        'X-Api-Key': apiKey,
        'Anthropic-Beta': beta,
        'Content-Type': 'application/json',
      },
      body: body === undefined ? undefined : JSON.stringify(body),
    });
    const text = await response.text();
    let payload = {};
    if (text.trim()) {
      try {
        payload = JSON.parse(text);
      } catch {
        payload = { raw: text };
      }
    }
    if (!response.ok) {
      const err = new Error(`${method} ${path} failed with ${response.status}`);
      err.status = response.status;
      err.requestID = response.headers.get('request-id') || response.headers.get('x-request-id') || undefined;
      err.response = payload;
      throw err;
    }
    return payload;
  } finally {
    clearTimeout(timeout);
  }
}

function serializeError(error) {
  if (!error || typeof error !== 'object') {
    return { message: String(error) };
  }
  return {
    message: error.message,
    name: error.name,
    status: error.status,
    request_id: error.requestID,
    response: error.response,
  };
}

function trimTrailingSlash(value) {
  return value.trim().replace(/\/+$/, '');
}
