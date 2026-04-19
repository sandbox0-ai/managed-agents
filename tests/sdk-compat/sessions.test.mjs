import test from 'node:test';
import assert from 'node:assert/strict';
import {
  assertFound,
  collectAsyncIterable,
  createBasicFixture,
  createCleanup,
  createSessionFixture,
  sdkClient,
  skipReason,
  suffix,
  textFromAgentMessages,
  uploadTextFile,
  waitForEvents,
  withTimeout,
} from './helpers.mjs';

test('sessions stream events and report end_turn outcomes', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t);

  const observed = [];
  const stream = await client.beta.sessions.events.stream(session.id);
  const streamDone = (async () => {
    for await (const event of stream) {
      observed.push(event);
      if (event?.type === 'agent.message') {
        return event;
      }
    }
    return null;
  })();

  const sent = await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'Say hello from the official SDK compatibility test' }],
    }],
  });
  assert.equal(sent.data?.[0]?.type, 'user.message');

  const streamedAgentMessage = await withTimeout(streamDone, 60_000, 'agent.message stream');
  stream.controller?.abort();
  assert.equal(streamedAgentMessage?.type, 'agent.message');

  const listedEvents = await waitForEvents(
    client,
    session.id,
    (events) => events.some((event) => event.type === 'session.status_idle'),
  );
  assertFound(listedEvents, (event) => event.type === 'user.message', 'user message event');
  assertFound(listedEvents, (event) => event.type === 'agent.message', 'agent message event');
  assertFound(
    listedEvents,
    (event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'end_turn',
    'end_turn outcome',
  );
  assert.match(textFromAgentMessages(listedEvents), /fake-wrapper response:/);

  const retrieved = await client.beta.sessions.retrieve(session.id);
  assert.equal(retrieved.id, session.id);
  assert.equal(retrieved.status, 'idle');
  assert(retrieved.usage.input_tokens >= 3);
});

test('sessions support update, list filters, archive, and delete', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session, agent } = await createSessionFixture(client, t);

  const updated = await client.beta.sessions.update(session.id, {
    title: `${session.title}-updated`,
    metadata: { sdk_compat_updated: 'true' },
  });
  assert.equal(updated.title, `${session.title}-updated`);
  assert.equal(updated.metadata.sdk_compat_updated, 'true');

  const listed = await collectAsyncIterable(client.beta.sessions.list({
    agent_id: agent.id,
    agent_version: agent.version,
    limit: 10,
  }), 20);
  assertFound(listed, (item) => item.id === session.id, 'filtered session');

  const archived = await client.beta.sessions.archive(session.id);
  assert.equal(archived.id, session.id);
  assert(archived.archived_at);

  const archivedList = await collectAsyncIterable(client.beta.sessions.list({
    include_archived: true,
    limit: 10,
  }), 20);
  assertFound(archivedList, (item) => item.id === session.id && item.archived_at, 'archived session');

  const deleted = await client.beta.sessions.delete(session.id);
  assert.equal(deleted.id, session.id);
  assert.equal(deleted.type, 'session_deleted');
});

test('sessions support pinned agent versions and event ordering through the SDK', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());
  const fixture = await createBasicFixture(client, runID, cleanup);

  const pinned = await client.beta.sessions.create({
    agent: {
      type: 'agent',
      id: fixture.agent.id,
      version: fixture.agent.version,
    },
    environment_id: fixture.environment.id,
    title: `sdk-compat-pinned-session-${runID}`,
    vault_ids: [fixture.vault.id],
    metadata: { e2e: 'sdk-compat', run: runID },
  });
  cleanup.add(() => client.beta.sessions.delete(pinned.id));

  const updatedAgent = await client.beta.agents.update(fixture.agent.id, {
    version: fixture.agent.version,
    name: `${fixture.agent.name}-new-version`,
  });
  assert.equal(updatedAgent.version, fixture.agent.version + 1);

  const retrieved = await client.beta.sessions.retrieve(pinned.id);
  assert.equal(retrieved.agent.id, fixture.agent.id);
  assert.equal(retrieved.agent.version, fixture.agent.version);
  assert.equal(retrieved.agent.name, fixture.agent.name);

  await client.beta.sessions.events.send(pinned.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'Check event list ordering' }],
    }],
  });
  await waitForEvents(
    client,
    pinned.id,
    (events) => events.some((event) => event.type === 'session.status_idle'),
  );

  const ascEvents = await collectAsyncIterable(client.beta.sessions.events.list(pinned.id, {
    order: 'asc',
    limit: 2,
  }), 2);
  const descEvents = await collectAsyncIterable(client.beta.sessions.events.list(pinned.id, {
    order: 'desc',
    limit: 2,
  }), 2);
  assert.equal(ascEvents[0]?.type, 'user.message');
  assert.equal(descEvents[0]?.type, 'session.status_idle');
  assert.notEqual(ascEvents[0]?.id, descEvents[0]?.id);
});

test('session resources and file-backed input events use the official Files API', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t);
  const uploaded = await uploadTextFile(client, `session-resource-${session.id}.txt`, 'mounted file content\n');
  t.after(async () => {
    try {
      await client.beta.files.delete(uploaded.id);
    } catch {}
  });

  const resource = await client.beta.sessions.resources.add(session.id, {
    type: 'file',
    file_id: uploaded.id,
    mount_path: '/workspace/input.txt',
  });
  assert.equal(resource.type, 'file');
  assert.equal(resource.file_id, uploaded.id);
  assert.equal(resource.mount_path, '/workspace/input.txt');

  const retrieved = await client.beta.sessions.resources.retrieve(resource.id, { session_id: session.id });
  assert.equal(retrieved.id, resource.id);

  const resources = await collectAsyncIterable(client.beta.sessions.resources.list(session.id, { limit: 10 }), 20);
  assertFound(resources, (item) => item.id === resource.id, 'session file resource');

  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [
        { type: 'text', text: 'Read the uploaded document' },
        {
          type: 'document',
          source: { type: 'file', file_id: uploaded.id },
          title: 'uploaded document',
        },
      ],
    }],
  });
  const listedEvents = await waitForEvents(client, session.id, (events) => events.some((event) => event.type === 'agent.message'));
  assertFound(listedEvents, (event) => event.type === 'agent.message', 'agent response after file-backed input');

  const deletedResource = await client.beta.sessions.resources.delete(resource.id, { session_id: session.id });
  assert.equal(deletedResource.id, resource.id);
  assert.equal(deletedResource.type, 'session_resource_deleted');

  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());
  const fixture = await createBasicFixture(client, runID, cleanup);
  const repositorySession = await client.beta.sessions.create({
    agent: fixture.agent.id,
    environment_id: fixture.environment.id,
    title: `sdk-compat-repository-resource-${runID}`,
    vault_ids: [fixture.vault.id],
    resources: [{
      type: 'github_repository',
      url: 'https://github.com/anthropics/anthropic-sdk-typescript',
      authorization_token: 'ghp_fake_initial_token',
      checkout: { type: 'branch', name: 'main' },
    }],
    metadata: { e2e: 'sdk-compat', run: runID },
  });
  cleanup.add(() => client.beta.sessions.delete(repositorySession.id));

  const repositoryResource = repositorySession.resources.find((item) => item.type === 'github_repository');
  assert(repositoryResource);
  assert.equal(repositoryResource.authorization_token, undefined);
  assert.equal(repositoryResource.checkout.type, 'branch');

  const rotatedRepositoryResource = await client.beta.sessions.resources.update(repositoryResource.id, {
    session_id: repositorySession.id,
    authorization_token: 'ghp_fake_rotated_token',
  });
  assert.equal(rotatedRepositoryResource.id, repositoryResource.id);
  assert.equal(rotatedRepositoryResource.authorization_token, undefined);
});

test('sessions accept documented image and document input block variants', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t);
  const uploadedImage = await uploadTextFile(client, `session-image-${session.id}.txt`, 'not really an image\n', 'image/png');
  t.after(async () => {
    try {
      await client.beta.files.delete(uploadedImage.id);
    } catch {}
  });

  const tinyPNG = 'iVBORw0KGgo=';
  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [
        { type: 'text', text: 'Exercise documented image and document blocks' },
        { type: 'image', source: { type: 'base64', media_type: 'image/png', data: tinyPNG } },
        { type: 'image', source: { type: 'url', url: 'https://example.com/image.png' } },
        { type: 'image', source: { type: 'file', file_id: uploadedImage.id } },
        {
          type: 'document',
          source: { type: 'text', media_type: 'text/plain', data: 'plain document text' },
          title: 'plain document',
        },
        {
          type: 'document',
          source: { type: 'base64', media_type: 'text/plain', data: Buffer.from('base64 document').toString('base64') },
          title: 'base64 document',
        },
        {
          type: 'document',
          source: { type: 'url', url: 'https://example.com/document.txt' },
          title: 'url document',
        },
      ],
    }],
  });

  const events = await waitForEvents(client, session.id, (items) => items.some((event) => event.type === 'agent.message'));
  const userMessage = events.find((event) => event.type === 'user.message');
  assert.equal(userMessage.content.filter((block) => block.type === 'image').length, 3);
  assert.equal(userMessage.content.filter((block) => block.type === 'document').length, 3);
});

test('sessions expose MCP tool, agent tool result, and model span event shapes', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t, {
    agent: {
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
            permission_policy: { type: 'always_allow' },
          },
        },
        {
          type: 'mcp_toolset',
          mcp_server_name: 'docs',
          default_config: {
            enabled: true,
            permission_policy: { type: 'always_allow' },
          },
        },
      ],
    },
  });

  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-mcp-tool-success' }],
    }],
  });
  let events = await waitForEvents(
    client,
    session.id,
    (items) => items.some((event) => event.type === 'agent.mcp_tool_result'),
  );
  const mcpUse = events.find((event) => event.type === 'agent.mcp_tool_use');
  const mcpResult = events.find((event) => event.type === 'agent.mcp_tool_result');
  assert.equal(mcpUse.mcp_server_name, 'docs');
  assert.equal(mcpUse.evaluated_permission, 'allow');
  assert.equal(mcpResult.mcp_tool_use_id, mcpUse.id);
  assert.equal(mcpResult.is_error, false);

  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-agent-tool-success' }],
    }],
  });
  events = await waitForEvents(
    client,
    session.id,
    (items) => items.some((event) => event.type === 'agent.tool_result'),
  );
  const toolUse = events.find((event) => event.type === 'agent.tool_use');
  const toolResult = events.find((event) => event.type === 'agent.tool_result');
  assert.equal(toolUse.evaluated_permission, 'allow');
  assert.equal(toolResult.tool_use_id, toolUse.id);
  assert.equal(toolResult.is_error, false);

  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-model-span-events' }],
    }],
  });
  events = await waitForEvents(
    client,
    session.id,
    (items) => items.some((event) => event.type === 'span.model_request_end'),
  );
  const spanStart = events.find((event) => event.type === 'span.model_request_start');
  const spanEnd = events.find((event) => event.type === 'span.model_request_end');
  assert.equal(spanEnd.model_request_start_id, spanStart.id);
  assert.equal(spanEnd.is_error, false);
  assert.equal(spanEnd.model_usage.input_tokens, 7);
  assert.equal(spanEnd.model_usage.speed, 'standard');
});

test('sessions cover requires_action outcomes for permission policies and custom tools', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const agentToolFixture = await createSessionFixture(client, t, {
    agent: {
      tools: [{
        type: 'agent_toolset_20260401',
        default_config: {
          enabled: true,
          permission_policy: { type: 'always_ask' },
        },
      }],
    },
  });

  await client.beta.sessions.events.send(agentToolFixture.session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-agent-tool-requires-action' }],
    }],
  });
  let events = await waitForEvents(
    client,
    agentToolFixture.session.id,
    (items) => items.some((event) => event.type === 'agent.tool_use' && event.evaluated_permission === 'ask'),
  );
  const toolUse = events.find((event) => event.type === 'agent.tool_use');
  assert(toolUse);
  assertFound(
    events,
    (event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'requires_action',
    'tool requires_action outcome',
  );

  const confirmation = await client.beta.sessions.events.send(agentToolFixture.session.id, {
    events: [{
      type: 'user.tool_confirmation',
      tool_use_id: toolUse.id,
      result: 'deny',
      deny_message: 'Not needed for this test.',
    }],
  });
  assert.equal(confirmation.data?.[0]?.type, 'user.tool_confirmation');

  const customToolFixture = await createSessionFixture(client, t, {
    agent: {
      tools: [{
        type: 'custom',
        name: 'lookup_customer',
        description: 'Look up a customer by ID.',
        input_schema: {
          type: 'object',
          properties: { customer_id: { type: 'string' } },
          required: ['customer_id'],
        },
      }],
    },
  });

  await client.beta.sessions.events.send(customToolFixture.session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-custom-tool-requires-action' }],
    }],
  });
  events = await waitForEvents(
    client,
    customToolFixture.session.id,
    (items) => items.some((event) => event.type === 'agent.custom_tool_use'),
  );
  const customToolUse = events.find((event) => event.type === 'agent.custom_tool_use');
  assert(customToolUse);

  const customResult = await client.beta.sessions.events.send(customToolFixture.session.id, {
    events: [{
      type: 'user.custom_tool_result',
      custom_tool_use_id: customToolUse.id,
      content: [{ type: 'text', text: 'customer found' }],
      is_error: false,
    }],
  });
  assert.equal(customResult.data?.[0]?.type, 'user.custom_tool_result');
});

test('sessions expose retries_exhausted outcomes', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t);

  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-retries-exhausted' }],
    }],
  });
  const events = await waitForEvents(
    client,
    session.id,
    (items) => items.some((event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'retries_exhausted'),
  );
  assertFound(events, (event) => event.type === 'session.error', 'session error event');
  assertFound(events, (event) => event.type === 'session.status_idle' && event.stop_reason.type === 'retries_exhausted', 'retries_exhausted outcome');
});
