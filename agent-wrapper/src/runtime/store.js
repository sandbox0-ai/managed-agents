import fs from 'node:fs';
import path from 'node:path';

const DEFAULT_STATE = { sessions: {} };

export class RuntimeStore {
  constructor(stateDir) {
    this.stateDir = stateDir;
    this.stateFile = path.join(stateDir, 'runtime-state.json');
    this.state = structuredClone(DEFAULT_STATE);
    this.persistenceEnabled = true;
    try {
      fs.mkdirSync(this.stateDir, { recursive: true });
      this.state = this.#readState();
    } catch {
      this.persistenceEnabled = false;
    }
  }

  load() {
    if (!this.persistenceEnabled) {
      return structuredClone(this.state);
    }
    this.state = this.#readState();
    return structuredClone(this.state);
  }

  #readState() {
    if (!fs.existsSync(this.stateFile)) {
      return structuredClone(DEFAULT_STATE);
    }
    const raw = fs.readFileSync(this.stateFile, 'utf8');
    if (!raw.trim()) {
      return structuredClone(DEFAULT_STATE);
    }
    return JSON.parse(raw);
  }

  save(state) {
    this.state = structuredClone(state);
    if (!this.persistenceEnabled) {
      return;
    }
    try {
      fs.writeFileSync(this.stateFile, `${JSON.stringify(state, null, 2)}\n`, 'utf8');
    } catch {
      this.persistenceEnabled = false;
    }
  }

  getSession(sessionID) {
    const state = this.load();
    return state.sessions[sessionID] ?? null;
  }

  upsertSession(sessionID, updater) {
    const state = this.load();
    const current = state.sessions[sessionID] ?? null;
    const next = updater(current);
    state.sessions[sessionID] = next;
    this.save(state);
    return next;
  }

  deleteSession(sessionID) {
    const state = this.load();
    delete state.sessions[sessionID];
    this.save(state);
  }
}
