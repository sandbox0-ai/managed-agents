import test from 'node:test';
import assert from 'node:assert/strict';
import { inputEventsToPrompt, inputEventsToSDKMessages } from '../src/lib/prompt.js';

test('inputEventsToPrompt flattens managed-agent input events', () => {
  const prompt = inputEventsToPrompt([
    { type: 'user.message', content: [{ type: 'text', text: 'hello' }] },
    { type: 'user.interrupt' },
    { type: 'user.custom_tool_result', custom_tool_use_id: 'tool_1', result: { ok: true } },
  ]);

  assert.match(prompt, /hello/);
  assert.match(prompt, /interrupted/);
  assert.match(prompt, /tool_1/);
});

test('inputEventsToSDKMessages preserves ordered multimodal content', async () => {
  const iterable = inputEventsToSDKMessages([{
    type: 'user.message',
    processed_at: '2026-04-12T00:00:00Z',
    content: [
      { type: 'image', source: { type: 'base64', media_type: 'image/png', data: 'iVBORw0=' } },
      { type: 'document', title: 'Plan', context: 'Review this', source: { type: 'base64', media_type: 'application/pdf', data: 'JVBERi0=' } },
      { type: 'text', text: 'summarize' },
    ],
  }]);

  const messages = [];
  for await (const message of iterable) {
    messages.push(message);
  }
  assert.equal(messages.length, 1);
  assert.equal(messages[0].type, 'user');
  assert.equal(messages[0].timestamp, '2026-04-12T00:00:00Z');
  assert.deepEqual(messages[0].message.content.map((block) => block.type), ['image', 'document', 'text']);
  assert.equal(messages[0].message.content[1].title, 'Plan');
  assert.equal(messages[0].message.content[1].context, 'Review this');
});
