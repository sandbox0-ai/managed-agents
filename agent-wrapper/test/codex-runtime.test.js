import test from 'node:test';
import assert from 'node:assert/strict';
import { EventEmitter } from 'node:events';
import { CodexRuntime } from '../src/adapters/codex.js';

class FakeCodexClient extends EventEmitter {
  constructor({ approval = false, stderrLine = null, startError = null, completionStatus = 'completed', completionError = null } = {}) {
    super();
    this.approval = approval;
    this.stderrLine = stderrLine;
    this.startError = startError;
    this.completionStatus = completionStatus;
    this.completionError = completionError;
    this.requests = [];
    this.responses = [];
  }

  async start() {
    if (this.stderrLine) {
      this.emit('stderr', this.stderrLine);
    }
  }

  async request(method, params) {
    this.requests.push({ method, params });
    if (method === 'thread/start') {
      return { thread: { id: 'thr_codex' } };
    }
    if (method === 'thread/resume') {
      return { thread: { id: params.threadId } };
    }
    if (method === 'turn/start') {
      if (this.startError) {
        throw this.startError;
      }
      const turn = { id: 'turn_codex', status: 'inProgress', items: [] };
      setImmediate(() => {
        this.emit('notification', { method: 'turn/started', params: { threadId: 'thr_codex', turn } });
        if (this.approval) {
          this.emit('serverRequest', {
            id: 101,
            method: 'item/commandExecution/requestApproval',
            params: {
              threadId: 'thr_codex',
              turnId: 'turn_codex',
              itemId: 'cmd_1',
              command: 'date',
              cwd: '/workspace',
            },
          });
          return;
        }
        this.emitAgentMessageAndComplete();
      });
      return { turn };
    }
    if (method === 'turn/interrupt') {
      return {};
    }
    throw new Error(`unexpected request ${method}`);
  }

  respond(id, result) {
    this.responses.push({ id, result });
    setImmediate(() => {
      this.emit('notification', {
        method: 'item/completed',
        params: {
          threadId: 'thr_codex',
          turnId: 'turn_codex',
          item: {
            id: 'cmd_1',
            type: 'commandExecution',
            command: 'date',
            cwd: '/workspace',
            commandActions: [],
            status: 'completed',
            aggregatedOutput: 'today\n',
          },
        },
      });
      this.emitComplete();
    });
  }

  respondError(id, error) {
    this.responses.push({ id, error });
  }

  close() {}

  emitAgentMessageAndComplete() {
    this.emit('notification', {
      method: 'item/completed',
      params: {
        threadId: 'thr_codex',
        turnId: 'turn_codex',
        item: { id: 'msg_1', type: 'agentMessage', text: 'done' },
      },
    });
    this.emit('notification', {
      method: 'thread/tokenUsage/updated',
      params: {
        threadId: 'thr_codex',
        turnId: 'turn_codex',
        tokenUsage: { last: { inputTokens: 3, outputTokens: 2, cachedInputTokens: 1 } },
      },
    });
    this.emitComplete();
  }

  emitComplete() {
    this.emit('notification', {
      method: 'turn/completed',
      params: {
        threadId: 'thr_codex',
        turn: { id: 'turn_codex', status: this.completionStatus, error: this.completionError, items: [] },
      },
    });
  }
}

test('CodexRuntime starts an app-server thread and maps a completed turn', async () => {
  const client = new FakeCodexClient();
  const runtime = new CodexRuntime({ clientFactory: () => client });
  const callbacks = [];
  let storedSession = codexSession();
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  await runtime.startRun(storedSession, runRequest(), { send: async (_session, payload) => callbacks.push(payload) }, sessionStore);

  assert.equal(storedSession.vendor_session_id, 'thr_codex');
  assert.equal(client.requests[0].method, 'thread/start');
  assert.equal(client.requests[1].method, 'turn/start');
  assert.deepEqual(client.requests[1].params.input, [{ type: 'text', text: 'hello' }]);
  assert(callbacks.flatMap((payload) => payload.events).some((event) => event.type === 'agent.message'));
  assert(callbacks.flatMap((payload) => payload.events).some((event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'end_turn'));
  assert(callbacks.some((payload) => payload.usage_delta?.input_tokens === 3 && payload.usage_delta?.output_tokens === 2));
});

test('CodexRuntime passes preloaded skills as app-server skill input items', async () => {
  const client = new FakeCodexClient();
  const runtime = new CodexRuntime({ clientFactory: () => client });
  const callbacks = [];
  let storedSession = codexSession({ skill_names: ['workspace-map', ' regression-check '] });
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  await runtime.startRun(storedSession, runRequest(), { send: async (_session, payload) => callbacks.push(payload) }, sessionStore);

  assert.deepEqual(client.requests[1].params.input, [
    { type: 'text', text: 'hello' },
    { type: 'skill', name: 'workspace-map', path: '/workspace/.claude/skills/workspace-map/SKILL.md' },
    { type: 'skill', name: 'regression-check', path: '/workspace/.claude/skills/regression-check/SKILL.md' },
  ]);
});

test('CodexRuntime maps app-server approvals to managed-agent required actions', async () => {
  const client = new FakeCodexClient({ approval: true });
  const runtime = new CodexRuntime({ clientFactory: () => client });
  const callbacks = [];
  let storedSession = codexSession({
    agent: {
      model: 'gpt-5.1-codex',
      tools: [{
        type: 'agent_toolset_20260401',
        default_config: { enabled: true, permission_policy: { type: 'always_ask' } },
      }],
    },
  });
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  const run = runtime.startRun(storedSession, runRequest(), { send: async (_session, payload) => callbacks.push(payload) }, sessionStore);
  await waitFor(() => callbacks.flatMap((payload) => payload.events).find((event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'requires_action'));

  const result = await runtime.resolveActions(storedSession.session_id, [{
    type: 'user.tool_confirmation',
    tool_use_id: 'cmd_1',
    result: 'allow',
  }], sessionStore);
  assert.deepEqual(result, { resolved_count: 1, remaining_action_ids: [], resume_required: false });
  await run;

  assert.deepEqual(client.responses, [{ id: 101, result: { decision: 'accept' } }]);
  assert(callbacks.flatMap((payload) => payload.events).some((event) => event.type === 'agent.tool_use' && event.evaluated_permission === 'ask'));
  assert(callbacks.flatMap((payload) => payload.events).some((event) => event.type === 'agent.tool_result' && event.tool_use_id === 'cmd_1'));
});

test('CodexRuntime resolves persisted tool confirmations like Claude', async () => {
  const runtime = new CodexRuntime({ clientFactory: () => new FakeCodexClient() });
  let storedSession = codexSession({
    vendor_session_id: 'thr_codex',
    pending_actions: [{
      id: 'cmd_1',
      kind: 'tool_confirmation',
      tool_use_id: 'cmd_1',
      name: 'bash',
      input: { command: 'date' },
      response_kind: 'command',
    }],
    tool_confirmation_resolutions: {},
  });
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  const result = await runtime.resolveActions(storedSession.session_id, [{
    type: 'user.tool_confirmation',
    tool_use_id: 'cmd_1',
    result: 'allow',
  }], sessionStore);

  assert.deepEqual(result, { resolved_count: 1, remaining_action_ids: [], resume_required: true });
  assert.deepEqual(storedSession.pending_actions, []);
  assert.deepEqual(storedSession.tool_confirmation_resolutions.cmd_1.result, { decision: 'accept' });
});

test('CodexRuntime consumes persisted tool confirmation resolutions on resume', async () => {
  const client = new FakeCodexClient({ approval: true });
  const runtime = new CodexRuntime({ clientFactory: () => client });
  const callbacks = [];
  let storedSession = codexSession({
    vendor_session_id: 'thr_codex',
    pending_actions: [{
      id: 'cmd_1',
      kind: 'tool_confirmation',
      tool_use_id: 'cmd_1',
      name: 'bash',
      input: { command: 'date' },
      response_kind: 'command',
    }],
    tool_confirmation_resolutions: {
      cmd_1: { result: { decision: 'accept' } },
    },
  });
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  await runtime.startRun(storedSession, runRequest(), { send: async (_session, payload) => callbacks.push(payload) }, sessionStore);

  assert.deepEqual(client.responses, [{ id: 101, result: { decision: 'accept' } }]);
  assert.deepEqual(storedSession.pending_actions, []);
  assert.deepEqual(storedSession.tool_confirmation_resolutions, {});
  assert.equal(callbacks.flatMap((payload) => payload.events).some((event) => event.stop_reason?.type === 'requires_action'), false);
});

test('CodexRuntime reports app-server start failures as managed-agent session errors', async () => {
  const client = new FakeCodexClient({
    startError: new Error('API Error: 429 {"error":{"code":"1302","message":"Rate limit reached for requests"}}'),
  });
  const runtime = new CodexRuntime({ clientFactory: () => client });
  const callbacks = [];
  let storedSession = codexSession();
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  await runtime.startRun(storedSession, runRequest(), { send: async (_session, payload) => callbacks.push(payload) }, sessionStore);

  const events = callbacks.flatMap((payload) => payload.events);
  assert(events.some((event) => event.type === 'span.model_request_end' && event.is_error === true));
  assert(events.some((event) => event.type === 'session.error' && event.error.type === 'model_rate_limited_error'));
  assert(events.some((event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'retries_exhausted'));
});

test('CodexRuntime maps failed turns through the shared final status helper', async () => {
  const client = new FakeCodexClient({
    completionStatus: 'failed',
    completionError: { message: 'API Error: 429 rate limit reached' },
  });
  const runtime = new CodexRuntime({ clientFactory: () => client });
  const callbacks = [];
  let storedSession = codexSession();
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  await runtime.startRun(storedSession, runRequest(), { send: async (_session, payload) => callbacks.push(payload) }, sessionStore);

  const events = callbacks.flatMap((payload) => payload.events);
  assert(events.some((event) => event.type === 'session.error' && event.error.type === 'model_rate_limited_error'));
  assert(events.some((event) => event.type === 'session.status_idle' && event.stop_reason?.type === 'retries_exhausted'));
});

test('CodexRuntime forwards app-server stderr to redacted wrapper logs', async () => {
  const client = new FakeCodexClient({ stderrLine: 'Authorization: Bearer secret-token api_key=sk-secret123' });
  const runtime = new CodexRuntime({ clientFactory: () => client });
  let storedSession = codexSession();
  const sessionStore = sessionStoreFor(() => storedSession, (next) => { storedSession = next; });

  const lines = await captureConsole(async () => {
    await runtime.startRun(storedSession, runRequest(), { send: async () => {} }, sessionStore);
  });

  const stderrLog = lines.map((line) => JSON.parse(line)).find((entry) => entry.msg === 'codex app-server stderr');
  assert(stderrLog, `missing stderr log in ${lines.join('\n')}`);
  assert.equal(stderrLog.level, 'warn');
  assert.equal(stderrLog.data.includes('secret-token'), false);
  assert.equal(stderrLog.data.includes('sk-secret123'), false);
  assert(stderrLog.data.includes('Bearer [redacted]'));
  assert(stderrLog.data.includes('api_key=[redacted]'));
});

function codexSession(overrides = {}) {
  return {
    session_id: 'sesn_codex',
    vendor: 'codex',
    vendor_session_id: null,
    working_directory: '/workspace',
    bootstrap_events: [],
    engine: {},
    agent: { model: 'gpt-5.1-codex', tools: [] },
    ...overrides,
  };
}

function runRequest() {
  return {
    run_id: 'run_codex',
    input_events: [{ type: 'user.message', content: [{ type: 'text', text: 'hello' }] }],
  };
}

async function captureConsole(fn) {
  const originalLog = console.log;
  const lines = [];
  console.log = (line) => lines.push(String(line));
  try {
    await fn();
  } finally {
    console.log = originalLog;
  }
  return lines;
}

function sessionStoreFor(getSession, setSession) {
  return {
    getSession,
    persistSession(updater) {
      const next = updater(getSession());
      setSession(next);
      return next;
    },
  };
}

async function waitFor(predicate) {
  const deadline = Date.now() + 1000;
  while (Date.now() < deadline) {
    const value = predicate();
    if (value) {
      return value;
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  throw new Error('condition not met');
}
