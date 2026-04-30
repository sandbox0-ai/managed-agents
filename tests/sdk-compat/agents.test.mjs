import test from 'node:test';
import assert from 'node:assert/strict';
import {
  agentBody,
  assertFound,
  collectAsyncIterable,
  createCleanup,
  sdkClient,
  skipReason,
  suffix,
} from './helpers.mjs';

test('agents support setup, tools, MCP connectors, permission policies, versions, and archive', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const agent = await client.beta.agents.create(agentBody(runID, {
    description: 'SDK compatibility agent with documented tool surfaces.',
    model: { id: 'claude-sonnet-4-20250514', speed: 'standard' },
    mcp_servers: [{
      type: 'url',
      name: 'docs',
      url: 'https://mcp.example.com/sse',
    }],
    tools: [
      {
        type: 'agent_toolset_20260401',
        default_config: {
          enabled: true,
          permission_policy: { type: 'always_ask' },
        },
        configs: [
          {
            name: 'bash',
            enabled: true,
            permission_policy: { type: 'always_allow' },
          },
          { name: 'web_search', enabled: false },
        ],
      },
      {
        type: 'mcp_toolset',
        mcp_server_name: 'docs',
        default_config: {
          enabled: true,
          permission_policy: { type: 'always_ask' },
        },
        configs: [{ name: 'search_docs', enabled: true }],
      },
      {
        type: 'custom',
        name: 'lookup_customer',
        description: 'Look up a customer record for the current request.',
        input_schema: {
          type: 'object',
          properties: { customer_id: { type: 'string' } },
          required: ['customer_id'],
        },
      },
    ],
  }));
  cleanup.add(() => client.beta.agents.archive(agent.id));

  assert.equal(agent.type, 'agent');
  assert.equal(agent.version, 1);
  assert.equal(agent.model.id, 'claude-sonnet-4-20250514');
  assert.equal(agent.model.speed, 'standard');
  assert.equal(agent.tools.length, 3);
  assertFound(agent.tools, (tool) => tool.type === 'agent_toolset_20260401', 'agent toolset');
  assertFound(agent.tools, (tool) => tool.type === 'mcp_toolset' && tool.mcp_server_name === 'docs', 'MCP toolset');
  assertFound(agent.tools, (tool) => tool.type === 'custom' && tool.name === 'lookup_customer', 'custom tool');
  assert.equal(agent.mcp_servers[0].name, 'docs');

  const retrieved = await client.beta.agents.retrieve(agent.id);
  assert.equal(retrieved.id, agent.id);
  assert.equal(retrieved.tools.length, 3);

  const updated = await client.beta.agents.update(agent.id, {
    version: agent.version,
    name: `${agent.name}-updated`,
    system: 'Use tools only when they are needed.',
    metadata: { sdk_compat_updated: 'true' },
  });
  assert.equal(updated.version, 2);
  assert.equal(updated.name, `${agent.name}-updated`);
  assert.equal(updated.metadata.sdk_compat_updated, 'true');

  await assert.rejects(
    () => client.beta.agents.update(agent.id, { version: agent.version, name: 'stale-version' }),
    /version|conflict|stale/i,
  );

  const versions = await collectAsyncIterable(client.beta.agents.versions.list(agent.id, { limit: 10 }), 10);
  assertFound(versions, (version) => version.version === 1, 'agent version 1');
  assertFound(versions, (version) => version.version === updated.version, 'updated agent version');

  const listed = await collectAsyncIterable(client.beta.agents.list({ limit: 5 }), 20);
  assertFound(listed, (item) => item.id === agent.id, 'created agent');

  const archived = await client.beta.agents.archive(agent.id);
  assert.equal(archived.id, agent.id);
  assert(archived.archived_at);
});

test('agents support string models, version retrieval, and update clearing semantics', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const agent = await client.beta.agents.create({
    name: `sdk-compat-string-model-agent-${runID}`,
    description: 'Temporary description to be cleared.',
    model: 'claude-sonnet-4-20250514',
    system: 'Temporary system prompt to be cleared.',
    mcp_servers: [{
      type: 'url',
      name: 'docs',
      url: 'https://mcp.example.com/sse',
    }],
    tools: [{
      type: 'agent_toolset_20260401',
      default_config: {
        enabled: true,
        permission_policy: { type: 'always_allow' },
      },
    }, {
      type: 'mcp_toolset',
      mcp_server_name: 'docs',
    }],
    metadata: {
      e2e: 'sdk-compat',
      remove_me: 'delete',
      keep_me: 'original',
      run: runID,
    },
  });
  cleanup.add(() => client.beta.agents.archive(agent.id));

  assert.equal(agent.model.id, 'claude-sonnet-4-20250514');
  assert.equal(agent.model.speed, 'standard');
  assert.equal(agent.description, 'Temporary description to be cleared.');
  assert.equal(agent.system, 'Temporary system prompt to be cleared.');
  assert.equal(agent.mcp_servers.length, 1);
  assert.equal(agent.tools.length, 2);

  const cleared = await client.beta.agents.update(agent.id, {
    version: agent.version,
    description: null,
    system: '',
    mcp_servers: [],
    tools: [],
    skills: [],
    metadata: {
      remove_me: null,
      keep_me: 'updated',
    },
  });
  assert.equal(cleared.version, agent.version + 1);
  assert.equal(cleared.description, null);
  assert.equal(cleared.system, null);
  assert.deepEqual(cleared.mcp_servers, []);
  assert.deepEqual(cleared.tools, []);
  assert.deepEqual(cleared.skills, []);
  assert.equal(cleared.metadata.keep_me, 'updated');
  assert.equal(cleared.metadata.remove_me, undefined);

  const versionOne = await client.beta.agents.retrieve(agent.id, { version: agent.version });
  assert.equal(versionOne.version, agent.version);
  assert.equal(versionOne.description, 'Temporary description to be cleared.');
  assert.equal(versionOne.mcp_servers.length, 1);

  const latest = await client.beta.agents.retrieve(agent.id);
  assert.equal(latest.version, cleared.version);
  assert.deepEqual(latest.tools, []);
});

test('agents support stdio MCP server definitions', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const agent = await client.beta.agents.create(agentBody(runID, {
    mcp_servers: [{
      type: 'stdio',
      name: 'local_docs',
      command: 'node',
      args: ['./server.js', '--stdio'],
      env: { API_BASE_URL: 'https://api.example.com' },
    }],
    tools: [{
      type: 'mcp_toolset',
      mcp_server_name: 'local_docs',
      default_config: {
        enabled: true,
        permission_policy: { type: 'always_ask' },
      },
    }],
  }));
  cleanup.add(() => client.beta.agents.archive(agent.id));

  assert.equal(agent.mcp_servers.length, 1);
  assert.equal(agent.mcp_servers[0].type, 'stdio');
  assert.equal(agent.mcp_servers[0].name, 'local_docs');
  assert.equal(agent.mcp_servers[0].command, 'node');
  assert.deepEqual(agent.mcp_servers[0].args, ['./server.js', '--stdio']);
  assert.equal(agent.mcp_servers[0].env.API_BASE_URL, 'https://api.example.com');
});
