const assert = require("node:assert/strict");
const test = require("node:test");
const { ManagedCodexTransport } = require("../src/remodex/managed-codex-transport");

test("ManagedCodexTransport starts a managed session and sends user text", async () => {
  const calls = [];
  const binding = {
    id: "appb_1",
    app: "remodex",
    external_id: "phone",
    agent_id: "agent_123",
    environment_id: "env_123",
    vault_ids: [],
    metadata: {},
  };
  const store = {
    setLastSession(id, sessionID) {
      calls.push(["setLastSession", id, sessionID]);
      return { ...binding, last_session_id: sessionID };
    },
  };
  const transport = new ManagedCodexTransport({
    store,
    binding,
    managedClient: {
      createSession: async () => ({ id: "sess_1", title: "Session" }),
      listEvents: async () => [],
      streamEvents: async () => emptyStream(),
      sendUserMessage: async (sessionID, text) => calls.push(["sendUserMessage", sessionID, text]),
    },
  });
  const messages = collectMessages(transport);

  transport.send(JSON.stringify({
    id: 1,
    method: "turn/start",
    params: { input: [{ type: "text", text: "hello" }] },
  }));
  await waitFor(() => calls.some((call) => call[0] === "sendUserMessage"));

  assert.deepEqual(calls.find((call) => call[0] === "sendUserMessage"), ["sendUserMessage", "sess_1", "hello"]);
  assert.ok(messages.some((message) => message.id === 1 && message.result?.turn?.status === "running"));
  assert.ok(messages.some((message) => message.method === "turn/started"));
});

test("ManagedCodexTransport maps Remodex approval responses to Managed Agents confirmations", async () => {
  const confirmations = [];
  const transport = new ManagedCodexTransport({
    store: { setLastSession: () => null },
    binding: {
      id: "appb_1",
      app: "remodex",
      external_id: "phone",
      agent_id: "agent_123",
      environment_id: "env_123",
      metadata: {},
    },
    managedClient: {
      sendToolConfirmation: async (...args) => confirmations.push(args),
    },
  });
  const messages = collectMessages(transport);

  await transport.emitManagedEvent("sess_1", {
    type: "agent.tool_use",
    id: "tool_1",
    name: "bash",
    input: { command: "pwd" },
    evaluated_permission: "ask",
  });
  const approvalRequest = messages.find((message) => message.id?.startsWith("approval_"));
  assert.ok(approvalRequest);

  transport.send(JSON.stringify({
    id: approvalRequest.id,
    result: { decision: "acceptForSession" },
  }));
  await waitFor(() => confirmations.length === 1);

  assert.deepEqual(confirmations[0], ["sess_1", "tool_1", "allow", ""]);
});

test("ManagedCodexTransport handles cloud-safe bridge-owned Remodex RPCs", async () => {
  const transport = new ManagedCodexTransport({
    store: { setLastSession: () => null },
    binding: {
      id: "appb_1",
      app: "remodex",
      external_id: "phone",
      agent_id: "agent_123",
      environment_id: "env_123",
      metadata: {},
    },
    managedClient: {},
  });
  const messages = collectMessages(transport);

  transport.send(JSON.stringify({ id: 1, method: "notifications/push/register", params: {} }));
  transport.send(JSON.stringify({ id: 2, method: "desktop/preferences/read", params: {} }));
  transport.send(JSON.stringify({ id: 3, method: "git/status", params: {} }));
  await waitFor(() => messages.length >= 3);

  assert.deepEqual(messages.find((message) => message.id === 1).result, { ok: false, skipped: true });
  assert.equal(messages.find((message) => message.id === 2).result.preferences.keepMacAwake, false);
  assert.equal(messages.find((message) => message.id === 3).error.data.errorCode, "sandbox_fallback_unavailable");
});

function collectMessages(transport) {
  const messages = [];
  transport.on("message", (raw) => messages.push(JSON.parse(raw)));
  return messages;
}

async function* emptyStream() {}

async function waitFor(predicate, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (predicate()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail("timed out waiting for condition");
}
