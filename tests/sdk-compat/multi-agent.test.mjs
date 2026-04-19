import test from 'node:test';
import {
  assertFound,
  createSessionFixture,
  sdkClient,
  skipReason,
  waitForEvents,
} from './helpers.mjs';

test('multi-agent thread signals round-trip as session events where the SDK exposes them', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t);

  await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'trigger-thread-context-compacted' }],
    }],
  });

  const events = await waitForEvents(
    client,
    session.id,
    (items) => items.some((event) => event.type === 'agent.thread_context_compacted'),
  );
  assertFound(events, (event) => event.type === 'agent.thread_context_compacted', 'thread compaction event');
  assertFound(events, (event) => event.type === 'agent.message', 'agent message after thread compaction');
});
