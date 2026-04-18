import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import {
  allowToolUseDecision,
  buildToolPlan,
  ClaudeRuntime,
  claudeAgentContextOptions,
  claudeExtraArgsForSession,
  claudeSettingSourcesForSession,
  claudeStatePathsForSession,
  claudeToolsForSession,
  finalStatusEventForSessionError,
  mcpServersFromAgent,
  providerErrorEventForText,
  querySkillNames,
  resolveToolPolicy,
  runtimeEnvForClaudeEngine,
  runtimeEnvForEngine,
  runtimeModelForSession,
  sessionErrorEventForClaudeSDKSystemMessage,
  sessionErrorEventForError,
} from '../src/adapters/claude.js';

async function captureStructuredLogs(fn) {
  const originalLog = console.log;
  const logs = [];
  console.log = (line) => {
    logs.push(JSON.parse(String(line)));
  };
  try {
    await fn();
  } finally {
    console.log = originalLog;
  }
  return logs;
}

test('ClaudeRuntime resolves custom tool results as pending actions', () => {
  const runtime = new ClaudeRuntime();
  const resolved = [];
  runtime.pendingActions.set('sesn_123', new Map([
    ['ctu_1', {
      id: 'ctu_1',
      kind: 'custom_tool_result',
      resolve: (value) => resolved.push(value),
      reject: () => {},
    }],
  ]));

  const result = runtime.resolveActions(
    'sesn_123',
    [{
      type: 'user.custom_tool_result',
      custom_tool_use_id: 'ctu_1',
      content: [{ type: 'text', text: '{"ok":true}' }],
      is_error: false,
    }],
    {
      persistSession: (updater) => updater({ session_id: 'sesn_123', pending_actions: [] }),
    },
  );

  assert.deepEqual(result, { resolved_count: 1, remaining_action_ids: [], resume_required: false });
  assert.deepEqual(resolved, [{
    content: [{ type: 'text', text: '{"ok":true}' }],
    isError: false,
  }]);
});

test('ClaudeRuntime resolves builtin tool confirmations into deferred resume state', () => {
  const runtime = new ClaudeRuntime();
  let persisted = null;

  const result = runtime.resolveActions(
    'sesn_456',
    [{
      type: 'user.tool_confirmation',
      tool_use_id: 'tool_1',
      result: 'allow',
    }],
    {
      getSession: () => ({
        session_id: 'sesn_456',
        pending_actions: [{ id: 'tool_1', kind: 'tool_confirmation', tool_use_id: 'tool_1' }],
        tool_confirmation_resolutions: {},
      }),
      persistSession: (updater) => {
        persisted = updater({
          session_id: 'sesn_456',
          pending_actions: [{ id: 'tool_1', kind: 'tool_confirmation', tool_use_id: 'tool_1' }],
          tool_confirmation_resolutions: {},
        });
        return persisted;
      },
    },
  );

  assert.deepEqual(result, { resolved_count: 1, remaining_action_ids: [], resume_required: true });
  assert.deepEqual(persisted.pending_actions, []);
  assert.equal(persisted.tool_confirmation_resolutions.tool_1.result.behavior, 'allow');
});

test('ClaudeRuntime resolves live builtin tool confirmation promises', async () => {
  const runtime = new ClaudeRuntime();
  let resolveDecision;
  const decisionPromise = new Promise((resolve) => {
    resolveDecision = resolve;
  });
  runtime.pendingActions.set('sesn_live', new Map([
    ['tool_3', {
      id: 'tool_3',
      kind: 'tool_confirmation',
      tool_use_id: 'tool_3',
      input: { command: 'printf ok' },
      resolve: resolveDecision,
      reject: () => {},
    }],
  ]));
  let persisted = null;

  const result = runtime.resolveActions(
    'sesn_live',
    [{ type: 'user.tool_confirmation', tool_use_id: 'tool_3', result: 'allow' }],
    {
      getSession: () => ({
        session_id: 'sesn_live',
        pending_actions: [{ id: 'tool_3', kind: 'tool_confirmation', tool_use_id: 'tool_3', input: { command: 'printf ok' } }],
        tool_confirmation_resolutions: {},
      }),
      persistSession: (updater) => {
        persisted = updater({
          session_id: 'sesn_live',
          pending_actions: [{ id: 'tool_3', kind: 'tool_confirmation', tool_use_id: 'tool_3', input: { command: 'printf ok' } }],
          tool_confirmation_resolutions: {},
        });
        return persisted;
      },
    },
  );

  assert.deepEqual(result, { resolved_count: 1, remaining_action_ids: [], resume_required: false });
  assert.deepEqual(await decisionPromise, {
    behavior: 'allow',
    updatedInput: { command: 'printf ok' },
    toolUseID: 'tool_3',
  });
  assert.deepEqual(persisted.pending_actions, []);
});

test('ClaudeRuntime can resume builtin tool confirmations from vendor session state alone', () => {
  const runtime = new ClaudeRuntime();
  let persisted = null;

  const result = runtime.resolveActions(
    'sesn_789',
    [{
      type: 'user.tool_confirmation',
      tool_use_id: 'tool_2',
      result: 'allow',
    }],
    {
      getSession: () => ({
        session_id: 'sesn_789',
        vendor_session_id: 'vendor_sesn_789',
        pending_actions: [],
        tool_confirmation_resolutions: {},
      }),
      persistSession: (updater) => {
        persisted = updater({
          session_id: 'sesn_789',
          vendor_session_id: 'vendor_sesn_789',
          pending_actions: [],
          tool_confirmation_resolutions: {},
        });
        return persisted;
      },
    },
  );

  assert.deepEqual(result, { resolved_count: 1, remaining_action_ids: [], resume_required: true });
  assert.equal(persisted.tool_confirmation_resolutions.tool_2.result.behavior, 'allow');
});

test('ClaudeRuntime starts SDK runs with documented skill context options', async () => {
  let sdkCall = null;
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      sdkCall = { prompt, options };
      return sdkResultStream();
    },
  });
  let currentSession = {
    session_id: 'sesn_sdk_options',
    working_directory: '/workspace/project',
    skill_names: ['workspace-map', 'regression-check'],
    agent: {
      system: 'Use attached skills.',
      model: { id: 'glm-5.1' },
      tools: [{ type: 'agent_toolset_20260401' }],
    },
    engine: {
      extra_args: { bare: null, 'debug-file': '/tmp/claude-debug.log' },
      setting_sources: ['local'],
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };
  const callbackPayloads = [];

  await runtime.startRun(currentSession, {
    run_id: 'run_sdk_options',
    input_events: [{ type: 'user.message', content: 'hi' }],
  }, {
    send: async (_session, payload) => callbackPayloads.push(payload),
  }, {
    getSession: () => currentSession,
    persistSession: (updater) => {
      currentSession = updater(currentSession);
      return currentSession;
    },
  });

  assert.equal(typeof sdkCall.prompt?.[Symbol.asyncIterator], 'function');
  assert.equal(sdkCall.options.agent, 'managed-agent');
  assert.deepEqual(sdkCall.options.agents['managed-agent'].skills, ['workspace-map', 'regression-check']);
  assert.deepEqual(sdkCall.options.extraArgs, { 'debug-file': '/tmp/claude-debug.log' });
  assert.deepEqual(sdkCall.options.settingSources, ['local', 'project']);
  assert.equal(sdkCall.options.tools.includes('Skill'), true);
  assert.equal(currentSession.vendor_session_id, 'vendor_sesn_sdk_options');
  assert.equal(callbackPayloads.at(-1).events.at(-1).type, 'session.status_idle');
});

test('ClaudeRuntime can prestart a resident SDK query before the first run', async () => {
  const sdkCalls = [];
  const prompts = [];
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      sdkCalls.push({ prompt, options });
      return sdkResidentResultStream(prompt, prompts, 'vendor_sesn_prestart');
    },
  });
  let currentSession = {
    session_id: 'sesn_prestart',
    vendor_session_id: null,
    working_directory: '/workspace',
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };
  const sessionStore = {
    getSession: () => currentSession,
    persistSession: (updater) => {
      currentSession = updater(currentSession);
      return currentSession;
    },
  };

  await runtime.prestartSession(currentSession, sessionStore);
  assert.equal(sdkCalls.length, 1);
  assert.equal(sdkCalls[0].options.resume, undefined);

  await runtime.startRun(currentSession, {
    run_id: 'run_prestart',
    input_events: [{ type: 'user.message', content: 'first after prestart' }],
  }, {
    send: async () => {},
  }, sessionStore);

  assert.equal(sdkCalls.length, 1);
  assert.deepEqual(prompts.map((message) => message.message.content[0].text), ['first after prestart']);
  assert.equal(currentSession.vendor_session_id, 'vendor_sesn_prestart');
  await runtime.deleteSession(currentSession.session_id, currentSession);
});

test('ClaudeRuntime deduplicates concurrent prestart requests', async () => {
  const sdkCalls = [];
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      sdkCalls.push({ prompt, options });
      return sdkResidentResultStream(prompt, [], 'vendor_sesn_prestart_dedupe');
    },
  });
  const currentSession = {
    session_id: 'sesn_prestart_dedupe',
    vendor_session_id: null,
    working_directory: '/workspace',
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };
  const sessionStore = {
    getSession: () => currentSession,
    persistSession: (updater) => updater(currentSession),
  };

  await Promise.all([
    runtime.prestartSession(currentSession, sessionStore),
    runtime.prestartSession(currentSession, sessionStore),
  ]);

  assert.equal(sdkCalls.length, 1);
  await runtime.deleteSession(currentSession.session_id, currentSession);
});

test('ClaudeRuntime reuses a resident SDK query across runs in a session', async () => {
  const sdkCalls = [];
  const prompts = [];
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      sdkCalls.push({ prompt, options });
      return sdkResidentResultStream(prompt, prompts, 'vendor_sesn_resident');
    },
  });
  let currentSession = {
    session_id: 'sesn_resident',
    vendor_session_id: null,
    working_directory: '/workspace',
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };
  const sessionStore = {
    getSession: () => currentSession,
    persistSession: (updater) => {
      currentSession = updater(currentSession);
      return currentSession;
    },
  };
  const callbackPayloads = [];
  const callbackClient = {
    send: async (_session, payload) => callbackPayloads.push(payload),
  };

  await runtime.startRun(currentSession, {
    run_id: 'run_resident_1',
    input_events: [{ type: 'user.message', content: 'first' }],
  }, callbackClient, sessionStore);
  await runtime.startRun(currentSession, {
    run_id: 'run_resident_2',
    input_events: [{ type: 'user.message', content: 'second' }],
  }, callbackClient, sessionStore);

  assert.equal(sdkCalls.length, 1);
  assert.deepEqual(prompts.map((message) => message.message.content[0].text), ['first', 'second']);
  assert.equal(currentSession.vendor_session_id, 'vendor_sesn_resident');
  assert.equal(callbackPayloads.filter((payload) => payload.events.some((event) => event.type === 'session.status_idle')).length, 2);
  await runtime.deleteSession(currentSession.session_id, currentSession);
});

test('ClaudeRuntime emits timing logs around resident query startup and first stream message', async () => {
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      void options;
      return sdkResidentResultStream(prompt, [], 'vendor_sesn_timing');
    },
  });
  let currentSession = {
    session_id: 'sesn_timing',
    vendor_session_id: null,
    working_directory: '/workspace',
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };
  const sessionStore = {
    getSession: () => currentSession,
    persistSession: (updater) => {
      currentSession = updater(currentSession);
      return currentSession;
    },
  };
  const callbackClient = {
    send: async () => {},
  };

  const logs = await captureStructuredLogs(async () => {
    await runtime.startRun(currentSession, {
      run_id: 'run_timing',
      input_events: [{ type: 'user.message', content: 'hello timing' }],
    }, callbackClient, sessionStore);
  });

  const messages = logs.map((entry) => entry.msg);
  assert.ok(messages.includes('claude resident run starting'));
  assert.ok(messages.includes('claude query starting'));
  assert.ok(messages.includes('claude query stream created'));
  assert.ok(messages.includes('claude runtime input enqueued'));
  assert.ok(messages.includes('claude first stream message'));
  assert.ok(messages.includes('claude result received'));

  const firstMessageLog = logs.find((entry) => entry.msg === 'claude first stream message');
  assert.equal(firstMessageLog.session_id, 'sesn_timing');
  assert.equal(firstMessageLog.run_id, 'run_timing');
  assert.ok(['assistant', 'system'].includes(firstMessageLog.message_type));
  assert.equal(firstMessageLog.vendor_session_id, 'vendor_sesn_timing');
});

test('ClaudeRuntime restarts a resident SDK query when session resources change', async () => {
  const sdkCalls = [];
  const prompts = [];
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      const callIndex = sdkCalls.length + 1;
      sdkCalls.push({ prompt, options });
      return sdkResidentResultStream(prompt, prompts, `vendor_sesn_resources_${callIndex}`);
    },
  });
  let currentSession = {
    session_id: 'sesn_resources',
    vendor_session_id: null,
    working_directory: '/workspace',
    resources: [{ type: 'volume', id: 'vol_a', mount_path: '/mnt/data' }],
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };
  const sessionStore = {
    getSession: () => currentSession,
    persistSession: (updater) => {
      currentSession = updater(currentSession);
      return currentSession;
    },
  };
  const callbackClient = {
    send: async () => {},
  };

  await runtime.startRun(currentSession, {
    run_id: 'run_resources_1',
    input_events: [{ type: 'user.message', content: 'before remount' }],
  }, callbackClient, sessionStore);

  currentSession = {
    ...currentSession,
    resources: [{ type: 'volume', id: 'vol_b', mount_path: '/mnt/data' }],
  };
  await runtime.startRun(currentSession, {
    run_id: 'run_resources_2',
    input_events: [{ type: 'user.message', content: 'after remount' }],
  }, callbackClient, sessionStore);
  await new Promise((resolve) => setImmediate(resolve));
  await runtime.startRun(currentSession, {
    run_id: 'run_resources_3',
    input_events: [{ type: 'user.message', content: 'same mount' }],
  }, callbackClient, sessionStore);

  assert.equal(sdkCalls.length, 2);
  assert.equal(sdkCalls[1].options.resume, 'vendor_sesn_resources_1');
  assert.deepEqual(prompts.map((message) => message.message.content[0].text), ['before remount', 'after remount', 'same mount']);
  await runtime.deleteSession(currentSession.session_id, currentSession);
});

test('ClaudeRuntime strips bare mode even without attached skills', async () => {
  let sdkCall = null;
  const runtime = new ClaudeRuntime({
    queryFn: ({ prompt, options }) => {
      sdkCall = { prompt, options };
      return sdkResultStream();
    },
  });
  let currentSession = {
    session_id: 'sesn_no_skills',
    working_directory: '/workspace',
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      extra_args: { bare: null, 'debug-file': '/tmp/claude-debug.log' },
      env: {
        CLAUDE_CONFIG_DIR: fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-')),
      },
    },
  };

  await runtime.startRun(currentSession, {
    run_id: 'run_no_skills',
    input_events: [{ type: 'user.message', content: 'hi' }],
  }, {
    send: async () => {},
  }, {
    getSession: () => currentSession,
    persistSession: (updater) => {
      currentSession = updater(currentSession);
      return currentSession;
    },
  });

  assert.equal(typeof sdkCall.prompt?.[Symbol.asyncIterator], 'function');
  assert.deepEqual(sdkCall.options.extraArgs, { 'debug-file': '/tmp/claude-debug.log' });
  assert.equal(sdkCall.options.agent, undefined);
  assert.deepEqual(sdkCall.options.tools, []);
  assert.equal(sdkCall.options.settingSources, undefined);
});

test('ClaudeRuntime hydrates and flushes mirrored Claude state around SDK runs', async () => {
  const mirrorCalls = [];
  const stateMirror = {
    hydrate: async (paths) => {
      mirrorCalls.push({ type: 'hydrate', paths });
      return { restored: true };
    },
    flush: async (paths) => {
      mirrorCalls.push({ type: 'flush', paths });
      return { flushed: true };
    },
  };
  const runtime = new ClaudeRuntime({
    stateMirror,
    queryFn: () => sdkResultStream(),
  });
  const localDir = fs.mkdtempSync(path.join(os.tmpdir(), 'claude-local-'));
  const mirrorDir = fs.mkdtempSync(path.join(os.tmpdir(), 'claude-mirror-'));
  let currentSession = {
    session_id: 'sesn_mirror',
    working_directory: '/workspace',
    skill_names: [],
    agent: {
      system: 'Base prompt.',
      tools: [],
    },
    engine: {
      env: {
        CLAUDE_CONFIG_DIR: localDir,
        AGENT_WRAPPER_CLAUDE_MIRROR_DIR: mirrorDir,
      },
    },
  };

  try {
    await runtime.startRun(currentSession, {
      run_id: 'run_mirror',
      input_events: [{ type: 'user.message', content: 'hi' }],
    }, {
      send: async () => {},
    }, {
      getSession: () => currentSession,
      persistSession: (updater) => {
        currentSession = updater(currentSession);
        return currentSession;
      },
    });

    const expectedPaths = claudeStatePathsForSession(currentSession);
    assert.deepEqual(mirrorCalls, [
      { type: 'hydrate', paths: expectedPaths },
      { type: 'flush', paths: expectedPaths },
    ]);
  } finally {
    fs.rmSync(localDir, { recursive: true, force: true });
    fs.rmSync(mirrorDir, { recursive: true, force: true });
  }
});

test('runtimeEnvForEngine preserves base process environment', () => {
  const merged = runtimeEnvForEngine({
    env: {
      PATH: '/custom/bin',
      ANTHROPIC_BASE_URL: 'https://api.z.ai/api/anthropic',
    },
  });

  assert.equal(merged.PATH, '/custom/bin');
  assert.equal(merged.ANTHROPIC_BASE_URL, 'https://api.z.ai/api/anthropic');
  assert.equal(merged.HOME, process.env.HOME);
});

test('runtimeEnvForClaudeEngine stores Claude config under local tmp state by default', () => {
  const previousLocalStateDir = process.env.AGENT_WRAPPER_LOCAL_STATE_DIR;
  const localStateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-wrapper-local-state-'));
  try {
    process.env.AGENT_WRAPPER_LOCAL_STATE_DIR = localStateDir;
    const env = runtimeEnvForClaudeEngine({
      env: {
        ANTHROPIC_BASE_URL: 'https://api.z.ai/api/anthropic',
      },
    });

    assert.equal(env.CLAUDE_CONFIG_DIR, path.join(localStateDir, 'claude'));
    assert.equal(fs.existsSync(env.CLAUDE_CONFIG_DIR), true);
    assert.equal(env.ANTHROPIC_BASE_URL, 'https://api.z.ai/api/anthropic');
  } finally {
    if (previousLocalStateDir === undefined) {
      delete process.env.AGENT_WRAPPER_LOCAL_STATE_DIR;
    } else {
      process.env.AGENT_WRAPPER_LOCAL_STATE_DIR = previousLocalStateDir;
    }
    fs.rmSync(localStateDir, { recursive: true, force: true });
  }
});

test('runtimeEnvForClaudeEngine preserves explicit Claude config dir', () => {
  const configDir = fs.mkdtempSync(path.join(os.tmpdir(), 'claude-config-'));
  fs.rmSync(configDir, { recursive: true, force: true });
  try {
    const env = runtimeEnvForClaudeEngine({
      env: {
        CLAUDE_CONFIG_DIR: configDir,
      },
    });

    assert.equal(env.CLAUDE_CONFIG_DIR, configDir);
    assert.equal(fs.existsSync(configDir), true);
  } finally {
    fs.rmSync(configDir, { recursive: true, force: true });
  }
});

test('runtimeModelForSession falls back to agent model when engine model is unset', () => {
  assert.equal(runtimeModelForSession({
    engine: {},
    agent: { model: { id: 'GLM-5.1' } },
  }), 'GLM-5.1');
});

test('runtimeModelForSession prefers explicit engine model', () => {
  assert.equal(runtimeModelForSession({
    engine: { model: 'claude-sonnet-4-6' },
    agent: { model: { id: 'GLM-5.1' } },
  }), 'claude-sonnet-4-6');
});

async function* sdkResultStream() {
  yield {
    type: 'system',
    subtype: 'init',
    session_id: 'vendor_sesn_sdk_options',
  };
  yield {
    type: 'result',
    subtype: 'success',
    session_id: 'vendor_sesn_sdk_options',
    stop_reason: 'end_turn',
    usage: {},
  };
}

async function* sdkResidentResultStream(prompt, prompts, sessionID) {
  let initialized = false;
  for await (const message of prompt) {
    prompts.push(message);
    if (!initialized) {
      initialized = true;
      yield {
        type: 'system',
        subtype: 'init',
        session_id: sessionID,
      };
    }
    yield {
      type: 'result',
      subtype: 'success',
      session_id: sessionID,
      stop_reason: 'end_turn',
      usage: {},
    };
  }
}

test('allowToolUseDecision returns SDK-compatible allow result', () => {
  assert.deepEqual(allowToolUseDecision({ command: 'printf ok' }, 'toolu_123'), {
    behavior: 'allow',
    updatedInput: { command: 'printf ok' },
    toolUseID: 'toolu_123',
  });
  assert.deepEqual(allowToolUseDecision(undefined, ''), {
    behavior: 'allow',
    updatedInput: {},
  });
});

test('mcpServersFromAgent converts url MCP definitions into SDK config', () => {
  const servers = mcpServersFromAgent([
    { type: 'url', name: 'issues', url: 'https://example.com/sse' },
    { type: 'url', name: 'search', url: 'https://api.example.com/mcp' },
  ]);

  assert.deepEqual(servers, {
    issues: { type: 'sse', url: 'https://example.com/sse' },
    search: { type: 'http', url: 'https://api.example.com/mcp' },
  });
});

test('buildToolPlan exposes built-in SDK tools from agent toolset defaults', () => {
  const plan = buildToolPlan([
    { type: 'agent_toolset_20260401' },
  ]);

  assert.deepEqual(plan.builtinSDKTools, ['Bash', 'Edit', 'Read', 'Write', 'Glob', 'Grep', 'WebFetch', 'WebSearch']);
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'Bash' }), {
    kind: 'builtin',
    name: 'bash',
    enabled: true,
    policy: 'always_allow',
  });
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'WebFetch' }), {
    kind: 'builtin',
    name: 'web_fetch',
    enabled: true,
    policy: 'always_allow',
  });
});

test('buildToolPlan disables built-ins when agent toolset is omitted', () => {
  const plan = buildToolPlan([
    { type: 'custom', name: 'lookup', description: 'Lookup', input_schema: { type: 'object' } },
  ]);

  assert.deepEqual(plan.builtinSDKTools, []);
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'Read' }), {
    kind: 'builtin',
    name: 'read',
    enabled: false,
    policy: 'always_ask',
  });
});

test('buildToolPlan applies built-in enabled and permission overrides', () => {
  const plan = buildToolPlan([
    {
      type: 'agent_toolset_20260401',
      default_config: { permission_policy: { type: 'always_ask' } },
      configs: [
        { name: 'bash', enabled: false },
        { name: 'read', permission_policy: { type: 'always_allow' } },
      ],
    },
  ]);

  assert.equal(plan.builtinSDKTools.includes('Bash'), false);
  assert.equal(plan.builtinSDKTools.includes('Read'), true);
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'Bash' }), {
    kind: 'builtin',
    name: 'bash',
    enabled: false,
    policy: 'always_ask',
  });
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'Read' }), {
    kind: 'builtin',
    name: 'read',
    enabled: true,
    policy: 'always_allow',
  });
});

test('buildToolPlan applies MCP defaults, overrides, and disabled tool visibility', () => {
  const plan = buildToolPlan([
    {
      type: 'mcp_toolset',
      mcp_server_name: 'docs',
      configs: [
        { name: 'fetch', permission_policy: { type: 'always_allow' } },
        { name: 'search', enabled: false },
      ],
    },
  ]);

  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'mcp__docs__list' }), {
    kind: 'mcp',
    name: 'list',
    serverName: 'docs',
    enabled: true,
    policy: 'always_ask',
  });
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'mcp__docs__fetch' }), {
    kind: 'mcp',
    name: 'fetch',
    serverName: 'docs',
    enabled: true,
    policy: 'always_allow',
  });
  assert.deepEqual(resolveToolPolicy(plan, { tool_name: 'mcp__docs__search' }), {
    kind: 'mcp',
    name: 'search',
    serverName: 'docs',
    enabled: false,
    policy: 'always_ask',
  });
  assert.deepEqual(plan.disallowedTools, ['mcp__docs__search']);
});

test('sessionErrorEventForError classifies MCP auth and connection failures', () => {
  assert.deepEqual(sessionErrorEventForError('MCP server "docs" authentication failed: 401 unauthorized'), {
    type: 'session.error',
    error: {
      type: 'mcp_authentication_failed_error',
      message: 'MCP server "docs" authentication failed: 401 unauthorized',
      retry_status: { type: 'terminal' },
      mcp_server_name: 'docs',
    },
  });
  assert.deepEqual(sessionErrorEventForError('MCP server search connection failed: ECONNREFUSED'), {
    type: 'session.error',
    error: {
      type: 'mcp_connection_failed_error',
      message: 'MCP server search connection failed: ECONNREFUSED',
      retry_status: { type: 'terminal' },
      mcp_server_name: 'search',
    },
  });
});

test('sessionErrorEventForError keeps fallback errors contract-shaped', () => {
  assert.deepEqual(sessionErrorEventForError('something unexpected'), {
    type: 'session.error',
    error: {
      type: 'unknown_error',
      message: 'something unexpected',
      retry_status: { type: 'terminal' },
    },
  });
});

test('sessionErrorEventForError classifies provider model failures', () => {
  assert.deepEqual(sessionErrorEventForError('API Error: 429 {"error":{"code":"1302","message":"Rate limit reached for requests"}}'), {
    type: 'session.error',
    error: {
      type: 'model_rate_limited_error',
      message: 'API Error: 429 {"error":{"code":"1302","message":"Rate limit reached for requests"}}',
      retry_status: { type: 'exhausted' },
    },
  });
  assert.deepEqual(sessionErrorEventForError('API Error: 529 overloaded'), {
    type: 'session.error',
    error: {
      type: 'model_overloaded_error',
      message: 'API Error: 529 overloaded',
      retry_status: { type: 'exhausted' },
    },
  });
  assert.deepEqual(sessionErrorEventForError('API Error: 402 out of credits'), {
    type: 'session.error',
    error: {
      type: 'billing_error',
      message: 'API Error: 402 out of credits',
      retry_status: { type: 'terminal' },
    },
  });
});

test('providerErrorEventForText detects SDK assistant-text API errors', () => {
  const event = providerErrorEventForText('API Error: 429 {"error":{"code":"1302","message":"Rate limit reached for requests"}}');

  assert.equal(event.type, 'session.error');
  assert.equal(event.error.type, 'model_rate_limited_error');
  assert.deepEqual(finalStatusEventForSessionError(event), {
    type: 'session.status_idle',
    stop_reason: { type: 'retries_exhausted' },
  });
  assert.equal(providerErrorEventForText('I can explain what a rate limit is.'), null);
});

test('sessionErrorEventForClaudeSDKSystemMessage maps API retry to retrying session error', () => {
  const event = sessionErrorEventForClaudeSDKSystemMessage({
    type: 'system',
    subtype: 'api_retry',
    error_status: 401,
    error: 'authentication_failed',
    message: 'invalid API key',
    attempt: 1,
    max_attempts: 3,
  });

  assert.equal(event.type, 'session.error');
  assert.equal(event.error.type, 'model_request_failed_error');
  assert.deepEqual(event.error.retry_status, { type: 'retrying' });
  assert.match(event.error.message, /status=401/);
  assert.match(event.error.message, /authentication_failed/);
  assert.match(event.error.message, /attempt=1/);
});

test('sessionErrorEventForClaudeSDKSystemMessage keeps retry error classification contract-shaped', () => {
  const rateLimit = sessionErrorEventForClaudeSDKSystemMessage({
    type: 'system',
    subtype: 'api_retry',
    status: 429,
    error: { type: 'rate_limit_error', message: 'too many requests' },
  });
  const overloaded = sessionErrorEventForClaudeSDKSystemMessage({
    type: 'system',
    subtype: 'api_retry',
    status: 529,
    error: 'overloaded',
  });

  assert.equal(rateLimit.error.type, 'model_rate_limited_error');
  assert.deepEqual(rateLimit.error.retry_status, { type: 'retrying' });
  assert.equal(overloaded.error.type, 'model_overloaded_error');
  assert.deepEqual(overloaded.error.retry_status, { type: 'retrying' });
});

test('sessionErrorEventForClaudeSDKSystemMessage maps SDK final errors to session errors', () => {
  const event = sessionErrorEventForClaudeSDKSystemMessage({
    type: 'system',
    subtype: 'api_error',
    status: 500,
    error: 'upstream failed',
  });

  assert.equal(event.type, 'session.error');
  assert.equal(event.error.type, 'model_request_failed_error');
  assert.deepEqual(event.error.retry_status, { type: 'exhausted' });
  assert.match(event.error.message, /subtype=api_error/);
});

test('sessionErrorEventForClaudeSDKSystemMessage ignores non-error system events', () => {
  assert.equal(sessionErrorEventForClaudeSDKSystemMessage({ type: 'system', subtype: 'init' }), null);
  assert.equal(sessionErrorEventForClaudeSDKSystemMessage({ type: 'assistant' }), null);
});

test('querySkillNames trims and filters preload skill names', () => {
  assert.deepEqual(querySkillNames({ skill_names: [' demo-skill ', '', null, 'search'] }), ['demo-skill', 'search']);
  assert.equal(querySkillNames({ skill_names: [] }), undefined);
});

test('claudeAgentContextOptions preloads skills through the main agent definition', () => {
  assert.deepEqual(claudeAgentContextOptions({
    agent: { system: 'Use attached skills.' },
    skill_names: [' workspace-map ', 'regression-check'],
  }), {
    agent: 'managed-agent',
    agents: {
      'managed-agent': {
        description: 'Primary Sandbox0 Managed Agents coding session.',
        prompt: 'Use attached skills.',
        skills: ['workspace-map', 'regression-check'],
      },
    },
  });
});

test('claudeAgentContextOptions keeps systemPrompt when no skills are attached', () => {
  assert.deepEqual(claudeAgentContextOptions({
    agent: { system: 'Use attached skills.' },
    skill_names: [],
  }), {
    systemPrompt: 'Use attached skills.',
  });
});

test('claudeExtraArgsForSession removes bare mode', () => {
  const extraArgs = { bare: null, workload: 'managed-agents' };
  const result = claudeExtraArgsForSession({
    skill_names: [],
    engine: { extra_args: extraArgs },
  });

  assert.deepEqual(result, { workload: 'managed-agents' });
  assert.deepEqual(extraArgs, { bare: null, workload: 'managed-agents' });
});

test('claudeToolsForSession enables the Claude Skill tool when skills are attached', () => {
  const sdkTools = ['Bash', 'Read'];
  const result = claudeToolsForSession({ skill_names: ['workspace-map'] }, sdkTools);

  assert.deepEqual(result, ['Bash', 'Read', 'Skill']);
  assert.deepEqual(sdkTools, ['Bash', 'Read']);
});

test('claudeToolsForSession does not duplicate Skill or enable it without skills', () => {
  assert.deepEqual(claudeToolsForSession({ skill_names: ['workspace-map'] }, ['Skill']), ['Skill']);
  assert.deepEqual(claudeToolsForSession({ skill_names: [] }, ['Bash']), ['Bash']);
});

test('claudeSettingSourcesForSession loads project settings when skills are attached', () => {
  assert.deepEqual(claudeSettingSourcesForSession({ skill_names: ['workspace-map'] }), ['project']);
  assert.deepEqual(claudeSettingSourcesForSession({
    skill_names: ['workspace-map'],
    engine: { setting_sources: ['user', 'invalid'] },
  }), ['user', 'project']);
});

test('claudeSettingSourcesForSession preserves explicit setting sources without skills', () => {
  assert.deepEqual(claudeSettingSourcesForSession({
    skill_names: [],
    engine: { settingSources: ['local'] },
  }), ['local']);
  assert.equal(claudeSettingSourcesForSession({ skill_names: [] }), undefined);
});
