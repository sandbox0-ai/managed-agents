import test from 'node:test';
import assert from 'node:assert/strict';
import { betaMemoryTool } from '@anthropic-ai/sdk/helpers/beta/memory';
import { sdkClient, skipReason } from './helpers.mjs';

test('memory helper remains available from the official SDK while Managed Agents has no memory-store resource', { skip: skipReason }, () => {
  const client = sdkClient();
  const memoryTool = betaMemoryTool({
    read_memory: async () => 'ok',
    view_memory: async () => 'ok',
    create_memory: async () => 'ok',
    str_replace_memory: async () => 'ok',
    insert_memory: async () => 'ok',
    delete_memory: async () => 'ok',
  });

  assert.equal(memoryTool.type, 'memory_20250818');
  assert.equal(memoryTool.name, 'memory');
  assert.equal(client.beta.memoryStores, undefined);
  assert.equal(client.beta.memories, undefined);
});
