import test from 'node:test';
import assert from 'node:assert/strict';
import {
  assertFound,
  collectAsyncIterable,
  createSessionFixture,
  sdkClient,
  skipReason,
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
