import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { createServer } from '../src/server.js';
import { RuntimeStore } from '../src/runtime/store.js';

function closeServer(server) {
  server.closeAllConnections?.();
  return new Promise((resolve, reject) => {
    server.close((error) => {
      if (error) {
        reject(error);
        return;
      }
      resolve();
    });
  });
}

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

  await closeServer(server);
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

  await closeServer(server);
});

test('agent-warper defaults bootstrap vendor to claude', async () => {
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

  const store = new RuntimeStore(stateDir);
  assert.equal(store.getSession('sesn_789')?.vendor, 'claude');

  await closeServer(server);
});

test('agent-warper persists codex bootstrap vendor', async () => {
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
    body: JSON.stringify({ session_id: 'sesn_codex', vendor: 'codex' }),
  });
  assert.equal(response.status, 200);

  const store = new RuntimeStore(stateDir);
  assert.equal(store.getSession('sesn_codex')?.vendor, 'codex');

  await closeServer(server);
});

test('agent-warper persists preload skill names during bootstrap', async () => {
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
    body: JSON.stringify({ session_id: 'sesn_skill', vendor: 'claude', skill_names: ['demo-skill', 'search'] }),
  });
  assert.equal(response.status, 200);

  const store = new RuntimeStore(stateDir);
  assert.deepEqual(store.getSession('sesn_skill')?.skill_names, ['demo-skill', 'search']);

  await closeServer(server);
});

test('agent-warper persists bootstrap diagnostic events during bootstrap', async () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-warper-'));
  const server = createServer({
    stateDir,
    runtime: new FakeRuntime(),
    callbackClient: { send: async () => {} },
  });
  await new Promise((resolve) => server.listen(0, resolve));
  const port = server.address().port;

  const bootstrapEvents = [{
    type: 'session.error',
    error: {
      type: 'mcp_authentication_failed_error',
      message: 'bad token',
      retry_status: { type: 'terminal' },
      mcp_server_name: 'docs',
    },
  }];
  const response = await fetch(`http://127.0.0.1:${port}/v1/runtime/session`, {
    method: 'PUT',
    headers: { 'content-type': 'application/json' },
    body: JSON.stringify({ session_id: 'sesn_bootstrap', vendor: 'claude', bootstrap_events: bootstrapEvents }),
  });
  assert.equal(response.status, 200);

  const store = new RuntimeStore(stateDir);
  assert.deepEqual(store.getSession('sesn_bootstrap')?.bootstrap_events, bootstrapEvents);

  await closeServer(server);
});

test('RuntimeStore falls back to in-memory state when persistence is unavailable', () => {
  const parentFile = path.join(os.tmpdir(), `agent-wrapper-state-${Date.now()}`);
  fs.writeFileSync(parentFile, 'not a directory', 'utf8');
  const store = new RuntimeStore(path.join(parentFile, 'child'));
  const session = store.upsertSession('sesn_mem', () => ({ session_id: 'sesn_mem', ok: true }));

  assert.deepEqual(session, { session_id: 'sesn_mem', ok: true });
  assert.deepEqual(store.getSession('sesn_mem'), { session_id: 'sesn_mem', ok: true });
});
