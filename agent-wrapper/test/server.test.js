import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createServer } from '../src/server.js';
import { RuntimeStore } from '../src/runtime/store.js';

class FakeRuntime {
  constructor() {
    this.interrupted = new Set();
  }

  async startRun(session, run, callbackClient, sessionStore) {
    sessionStore.persistSession((current) => ({ ...current, vendor_session_id: 'vendor_123' }));
    await callbackClient.send(session, {
      session_id: session.session_id,
      run_id: run.run_id,
      vendor_session_id: 'vendor_123',
      events: [{ type: 'agent.message', content: [{ type: 'text', text: 'done' }] }],
    });
  }

  resolveActions() {
    return { resolved_count: 0, remaining_action_ids: [] };
  }

  async interruptRun(runID) {
    this.interrupted.add(runID);
    return true;
  }

  deleteSession() {}
}

test('agent-warper bootstraps a session and starts a run', async () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-warper-'));
  const callbacks = [];
  const server = createServer({
    stateDir,
    runtime: new FakeRuntime(),
    callbackClient: { send: async (_session, payload) => callbacks.push(payload) },
  });
  await new Promise((resolve) => server.listen(0, resolve));
  const port = server.address().port;

  let response = await fetch(`http://127.0.0.1:${port}/v1/runtime/session`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ session_id: 'sesn_123', vendor: 'claude' }),
  });
  assert.equal(response.status, 200);

  response = await fetch(`http://127.0.0.1:${port}/v1/runs`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ session_id: 'sesn_123', run_id: 'run_123', input_events: [{ type: 'user.message', content: [{ type: 'text', text: 'hi' }] }] }),
  });
  assert.equal(response.status, 202);

  await new Promise((resolve) => setTimeout(resolve, 50));
  assert.equal(callbacks.length, 1);
  assert.equal(callbacks[0].vendor_session_id, 'vendor_123');

  server.close();
});

test('agent-warper returns action resolution state from runtime', async () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-warper-'));
  const runtime = new FakeRuntime();
  runtime.resolveActions = () => ({ resolved_count: 1, remaining_action_ids: ['evt_2'] });
  const server = createServer({
    stateDir,
    runtime,
    callbackClient: { send: async () => {} },
  });
  await new Promise((resolve) => server.listen(0, resolve));
  const port = server.address().port;

  let response = await fetch(`http://127.0.0.1:${port}/v1/runtime/session`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ session_id: 'sesn_456', vendor: 'claude' }),
  });
  assert.equal(response.status, 200);

  response = await fetch(`http://127.0.0.1:${port}/v1/runtime/session/sesn_456/actions/resolve`, {
    method: 'POST',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ events: [{ type: 'user.tool_confirmation', tool_use_id: 'evt_1', result: 'allow' }] }),
  });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), { resolved_count: 1, remaining_action_ids: ['evt_2'] });

  server.close();
});

test('agent-warper ignores vendor-neutral bootstrap fields and stays Claude-specific', async () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-warper-'));
  const server = createServer({
    stateDir,
    runtime: new FakeRuntime(),
    callbackClient: { send: async () => {} },
  });
  await new Promise((resolve) => server.listen(0, resolve));
  const port = server.address().port;

  const response = await fetch(`http://127.0.0.1:${port}/v1/runtime/session`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ session_id: 'sesn_789' }),
  });
  assert.equal(response.status, 200);
  assert.deepEqual(await response.json(), {
    session_id: 'sesn_789',
    vendor_session_id: null,
  });

  server.close();
});

test('RuntimeStore falls back to in-memory state when persistence is unavailable', () => {
  const store = new RuntimeStore('/proc/agent-wrapper-state');
  const session = store.upsertSession('sesn_mem', () => ({ session_id: 'sesn_mem', ok: true }));

  assert.deepEqual(session, { session_id: 'sesn_mem', ok: true });
  assert.deepEqual(store.getSession('sesn_mem'), { session_id: 'sesn_mem', ok: true });
});
