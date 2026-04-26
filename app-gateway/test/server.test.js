const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { createAppGatewayServer, parseAddr } = require("../src/server");

test("parseAddr binds colon-only addresses on all interfaces", () => {
  assert.deepEqual(parseAddr(":8787"), {
    host: "127.0.0.1",
    listenHost: "127.0.0.1",
    rawHost: "0.0.0.0",
    port: 8787,
  });
});

test("server exposes Remodex binding API with optional bearer auth", async (t) => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "app-gateway-server-"));
  const gateway = createAppGatewayServer({
    addr: "127.0.0.1:8978",
    stateDir,
    authToken: "secret",
    managedClient: {},
  });
  await listen(gateway.server, 8978, "127.0.0.1");
  t.after(() => gateway.close());

  const unauthorized = await fetchJSON("http://127.0.0.1:8978/v1/app-bindings");
  assert.equal(unauthorized.status, 401);

  const created = await fetchJSON("http://127.0.0.1:8978/v1/app-bindings", {
    method: "POST",
    headers: {
      authorization: "Bearer secret",
      "content-type": "application/json",
    },
    body: JSON.stringify({
      app: "remodex",
      external_id: "phone",
      agent_id: "agent_123",
      environment_id: "env_123",
    }),
  });
  assert.equal(created.status, 201);
  assert.match(created.body.binding.id, /^appb_/);

  const listed = await fetchJSON("http://127.0.0.1:8978/v1/app-bindings", {
    headers: { authorization: "Bearer secret" },
  });
  assert.equal(listed.body.bindings.length, 1);
});

test("server starts a Remodex bridge that registers a pairing code in the relay", async (t) => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "app-gateway-relay-"));
  const gateway = createAppGatewayServer({
    addr: "127.0.0.1:8979",
    stateDir,
    publicRelayURL: "ws://127.0.0.1:8979/relay",
    localRelayURL: "ws://127.0.0.1:8979/relay",
    managedClient: {},
  });
  await listen(gateway.server, 8979, "127.0.0.1");
  t.after(() => gateway.close());

  const binding = gateway.store.createBinding({
    app: "remodex",
    external_id: "phone",
    agent_id: "agent_123",
    environment_id: "env_123",
  });
  const started = await fetchJSON(`http://127.0.0.1:8979/v1/app-bindings/${binding.id}/remodex/start`, {
    method: "POST",
  });
  assert.equal(started.status, 200);
  assert.equal(started.body.pairing_payload.relay, "ws://127.0.0.1:8979/relay");
  assert.match(started.body.pairing_code, /^[ABCDEFGHJKLMNPQRSTUVWXYZ23456789]{10}$/);

  await waitFor(async () => {
    const resolved = await fetchJSON("http://127.0.0.1:8979/v1/pairing/code/resolve", {
      method: "POST",
      headers: { "content-type": "application/json" },
      body: JSON.stringify({ code: started.body.pairing_code }),
    });
    return resolved.status === 200 && resolved.body.sessionId === started.body.relay_session_id;
  });
});

async function fetchJSON(url, options = {}) {
  const response = await fetch(url, options);
  return {
    status: response.status,
    body: await response.json(),
  };
}

function listen(server, port, host) {
  return new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => {
      server.off("error", reject);
      resolve();
    });
  });
}

async function waitFor(predicate, timeoutMs = 1000) {
  const deadline = Date.now() + timeoutMs;
  while (Date.now() < deadline) {
    if (await predicate()) {
      return;
    }
    await new Promise((resolve) => setTimeout(resolve, 10));
  }
  assert.fail("timed out waiting for condition");
}
