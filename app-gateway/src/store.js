const fs = require("node:fs");
const path = require("node:path");
const { randomUUID } = require("node:crypto");

const SUPPORTED_APPS = new Set(["remodex"]);
const SESSION_SCOPES = new Set(["device", "thread", "message"]);

class FileStore {
  constructor({ stateDir }) {
    this.stateDir = stateDir || path.resolve(".app-gateway-state");
    this.filePath = path.join(this.stateDir, "state.json");
    this.state = { bindings: {}, remodexDevices: {} };
    this.load();
  }

  load() {
    try {
      const parsed = JSON.parse(fs.readFileSync(this.filePath, "utf8"));
      this.state = {
        bindings: objectRecord(parsed.bindings),
        remodexDevices: objectRecord(parsed.remodexDevices),
      };
    } catch {
      this.state = { bindings: {}, remodexDevices: {} };
    }
  }

  save() {
    fs.mkdirSync(this.stateDir, { recursive: true });
    const tempPath = `${this.filePath}.${process.pid}.tmp`;
    fs.writeFileSync(tempPath, JSON.stringify(this.state, null, 2), { mode: 0o600 });
    fs.renameSync(tempPath, this.filePath);
  }

  createBinding(input) {
    const binding = normalizeBinding(input);
    const now = new Date().toISOString();
    binding.id = `appb_${randomUUID()}`;
    binding.created_at = now;
    binding.updated_at = now;
    this.state.bindings[binding.id] = binding;
    this.save();
    return clone(binding);
  }

  getBinding(id) {
    return clone(this.state.bindings[String(id || "").trim()]);
  }

  listBindings() {
    return Object.values(this.state.bindings)
      .sort((a, b) => String(a.created_at).localeCompare(String(b.created_at)))
      .map(clone);
  }

  updateBinding(id, patch) {
    const binding = this.state.bindings[String(id || "").trim()];
    if (!binding) {
      return null;
    }
    const next = normalizeBinding({ ...binding, ...patch, id: binding.id }, { allowID: true });
    next.created_at = binding.created_at;
    next.updated_at = new Date().toISOString();
    next.last_session_id = typeof patch.last_session_id === "string"
      ? patch.last_session_id
      : binding.last_session_id;
    this.state.bindings[binding.id] = next;
    this.save();
    return clone(next);
  }

  setLastSession(id, sessionID) {
    const binding = this.state.bindings[String(id || "").trim()];
    if (!binding) {
      return null;
    }
    binding.last_session_id = String(sessionID || "").trim();
    binding.updated_at = new Date().toISOString();
    this.save();
    return clone(binding);
  }

  deleteBinding(id) {
    const key = String(id || "").trim();
    if (!this.state.bindings[key]) {
      return false;
    }
    delete this.state.bindings[key];
    delete this.state.remodexDevices[key];
    this.save();
    return true;
  }

  getRemodexDevice(bindingID) {
    return clone(this.state.remodexDevices[String(bindingID || "").trim()]);
  }

  setRemodexDevice(bindingID, deviceState) {
    this.state.remodexDevices[String(bindingID || "").trim()] = clone(deviceState);
    this.save();
    return clone(deviceState);
  }
}

function normalizeBinding(input, { allowID = false } = {}) {
  const app = normalizeString(input.app).toLowerCase();
  if (!SUPPORTED_APPS.has(app)) {
    throw Object.assign(new Error(`unsupported app: ${app || "missing"}`), { status: 400 });
  }
  const externalID = normalizeString(input.external_id);
  const agentID = normalizeString(input.agent_id);
  const environmentID = normalizeString(input.environment_id);
  const sessionScope = normalizeString(input.session_scope || "thread").toLowerCase();
  if (!externalID || !agentID || !environmentID) {
    throw Object.assign(new Error("external_id, agent_id, and environment_id are required"), { status: 400 });
  }
  if (!SESSION_SCOPES.has(sessionScope)) {
    throw Object.assign(new Error("session_scope must be device, thread, or message"), { status: 400 });
  }
  const binding = {
    app,
    external_id: externalID,
    agent_id: agentID,
    environment_id: environmentID,
    vault_ids: normalizeStringArray(input.vault_ids),
    session_scope: sessionScope,
    metadata: objectRecord(input.metadata),
    last_session_id: normalizeString(input.last_session_id),
  };
  if (allowID) {
    binding.id = normalizeString(input.id);
  }
  return binding;
}

function normalizeStringArray(values) {
  if (!Array.isArray(values)) {
    return [];
  }
  return [...new Set(values.map(normalizeString).filter(Boolean))];
}

function objectRecord(value) {
  return value && typeof value === "object" && !Array.isArray(value) ? { ...value } : {};
}

function normalizeString(value) {
  return typeof value === "string" && value.trim() ? value.trim() : "";
}

function clone(value) {
  return value == null ? null : JSON.parse(JSON.stringify(value));
}

module.exports = {
  FileStore,
  normalizeBinding,
};
