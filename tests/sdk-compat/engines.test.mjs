import test from 'node:test';
import assert from 'node:assert/strict';
import {
  createSessionFixture,
  sdkClient,
  skipReason,
  textFromAgentMessages,
  waitForEvents,
} from './helpers.mjs';

test('the official SDK protocol can create a session backed by a Codex LLM vault', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const { session } = await createSessionFixture(client, t, { engine: 'codex' });

  const sent = await client.beta.sessions.events.send(session.id, {
    events: [{
      type: 'user.message',
      content: [{ type: 'text', text: 'Exercise the Codex managed-agent runtime route' }],
    }],
  });
  assert.equal(sent.data?.[0]?.type, 'user.message');

  const listedEvents = await waitForEvents(
    client,
    session.id,
    (events) => events.some((event) => event.type === 'agent.message'),
    { timeoutMs: 180_000 },
  );
  assert.match(textFromAgentMessages(listedEvents), /fake-wrapper response:/);
});
