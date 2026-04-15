import test from 'node:test';
import assert from 'node:assert/strict';
import { sanitizeLogFields, summarizePendingActions } from '../src/lib/log.js';

test('sanitizeLogFields redacts sensitive fields and URL credentials', () => {
  const sanitized = sanitizeLogFields({
    control_token: 'secret-token',
    has_control_token: true,
    callback_url: 'https://user:pass@example.com/webhook?token=secret&ok=1',
    error: 'Authorization: Bearer bearer-secret api_key=sk-secret123',
  });

  assert.equal(sanitized.control_token, '[redacted]');
  assert.equal(sanitized.has_control_token, true);
  assert.equal(sanitized.callback_url, 'https://%5Bredacted%5D:%5Bredacted%5D@example.com/webhook?token=%5Bredacted%5D&ok=1');
  assert.equal(sanitized.error.includes('bearer-secret'), false);
  assert.equal(sanitized.error.includes('sk-secret123'), false);
});

test('summarizePendingActions avoids logging action input', () => {
  const summary = summarizePendingActions([{
    id: 'toolu_1',
    kind: 'tool_confirmation',
    input: { command: 'echo secret' },
  }]);

  assert.deepEqual(summary, {
    pending_action_count: 1,
    pending_action_ids: ['toolu_1'],
    pending_action_kinds: ['tool_confirmation'],
  });
});
