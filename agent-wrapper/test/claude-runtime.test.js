import test from 'node:test';
import assert from 'node:assert/strict';
import {
  buildToolPlan,
  ClaudeRuntime,
  mcpServersFromAgent,
  querySkillNames,
  resolveToolPolicy,
  runtimeEnvForEngine,
  runtimeModelForSession,
  sessionErrorEventForError,
} from '../src/adapters/claude.js';

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

test('querySkillNames trims and filters preload skill names', () => {
  assert.deepEqual(querySkillNames({ skill_names: [' demo-skill ', '', null, 'search'] }), ['demo-skill', 'search']);
  assert.equal(querySkillNames({ skill_names: [] }), undefined);
});
