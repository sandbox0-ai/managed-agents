import test from 'node:test';
import assert from 'node:assert/strict';
import { inputEventsToPrompt } from '../src/lib/prompt.js';

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
