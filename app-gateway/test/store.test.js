const assert = require("node:assert/strict");
const fs = require("node:fs");
const os = require("node:os");
const path = require("node:path");
const test = require("node:test");
const { FileStore } = require("../src/store");

test("FileStore persists Remodex app bindings", () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "app-gateway-store-"));
  const store = new FileStore({ stateDir });

  const binding = store.createBinding({
    app: "remodex",
    external_id: "iphone-1",
    agent_id: "agent_123",
    environment_id: "env_123",
    vault_ids: ["vault_1", "vault_1", ""],
    session_scope: "thread",
  });

  assert.match(binding.id, /^appb_/);
  assert.equal(binding.app, "remodex");
  assert.deepEqual(binding.vault_ids, ["vault_1"]);

  const reloaded = new FileStore({ stateDir });
  assert.equal(reloaded.getBinding(binding.id).agent_id, "agent_123");
});

test("FileStore rejects unsupported app protocols", () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), "app-gateway-store-"));
  const store = new FileStore({ stateDir });

  assert.throws(() => store.createBinding({
    app: "slack",
    external_id: "team",
    agent_id: "agent_123",
    environment_id: "env_123",
  }), /unsupported app/);
});
