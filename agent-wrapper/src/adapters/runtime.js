export const DEFAULT_VENDOR = 'claude';

const VENDOR_ALIASES = new Map([
  ['anthropic', 'claude'],
  ['claude-code', 'claude'],
  ['claudecode', 'claude'],
  ['openai', 'codex'],
]);

export function normalizeVendor(value, fallback = DEFAULT_VENDOR) {
  const raw = String(value ?? '').trim().toLowerCase();
  const normalized = raw || String(fallback ?? DEFAULT_VENDOR).trim().toLowerCase() || DEFAULT_VENDOR;
  return VENDOR_ALIASES.get(normalized) ?? normalized;
}

export class AgentRuntime {
  constructor(vendor) {
    this.vendor = normalizeVendor(vendor);
  }

  async startRun() {
    throw new Error(`${this.vendor} runtime does not implement startRun`);
  }

  async interruptRun() {
    return false;
  }

  async deleteSession() {}

  async resolveActions() {
    return { resolved_count: 0, remaining_action_ids: [], resume_required: false };
  }
}

export class RuntimeRouter extends AgentRuntime {
  constructor(runtimes, { defaultVendor = process.env.AGENT_WRAPPER_DEFAULT_VENDOR ?? DEFAULT_VENDOR } = {}) {
    super('router');
    this.defaultVendor = normalizeVendor(defaultVendor);
    this.runtimes = new Map();
    for (const [vendor, runtime] of Object.entries(runtimes ?? {})) {
      this.register(vendor, runtime);
    }
  }

  register(vendor, runtime) {
    const normalizedVendor = normalizeVendor(vendor);
    assertRuntimeContract(normalizedVendor, runtime);
    this.runtimes.set(normalizedVendor, runtime);
  }

  runtimeForSession(session) {
    const vendor = normalizeVendor(session?.vendor, this.defaultVendor);
    const runtime = this.runtimes.get(vendor);
    if (!runtime) {
      throw new Error(`unsupported managed-agent runtime vendor: ${vendor}`);
    }
    return runtime;
  }

  async startRun(session, run, callbackClient, sessionStore) {
    return this.runtimeForSession(session).startRun(session, run, callbackClient, sessionStore);
  }

  async interruptRun(runID) {
    for (const runtime of this.runtimes.values()) {
      if (await runtime.interruptRun(runID)) {
        return true;
      }
    }
    return false;
  }

  async deleteSession(sessionID, session) {
    if (session) {
      await this.runtimeForSession(session).deleteSession(sessionID, session);
      return;
    }
    for (const runtime of this.runtimes.values()) {
      await runtime.deleteSession(sessionID, session);
    }
  }

  async resolveActions(sessionID, events, sessionStore) {
    return this.runtimeForSession(sessionStore.getSession?.()).resolveActions(sessionID, events, sessionStore);
  }
}

export function assertRuntimeContract(vendor, runtime) {
  for (const method of ['startRun', 'interruptRun', 'deleteSession', 'resolveActions']) {
    if (typeof runtime?.[method] !== 'function') {
      throw new Error(`${vendor} runtime is missing ${method}`);
    }
  }
}

export function runtimeEnvForEngine(engine) {
  return {
    ...process.env,
    ...(engine?.env ?? {}),
  };
}

export function runtimeModelForSession(session) {
  const engineModel = session?.engine?.model;
  if (typeof engineModel === 'string' && engineModel.trim() !== '') {
    return engineModel;
  }
  if (engineModel && typeof engineModel === 'object' && typeof engineModel.id === 'string' && engineModel.id.trim() !== '') {
    return engineModel.id;
  }
  const agentModel = session?.agent?.model;
  if (typeof agentModel === 'string' && agentModel.trim() !== '') {
    return agentModel;
  }
  if (agentModel && typeof agentModel === 'object' && typeof agentModel.id === 'string' && agentModel.id.trim() !== '') {
    return agentModel.id;
  }
  return undefined;
}

export function sessionErrorEventForError(error, fallbackMessage = 'Managed agent run failed') {
  const message = error instanceof Error ? error.message : String(error ?? '');
  return {
    type: 'session.error',
    error: sessionErrorDetailForMessage(message || fallbackMessage),
  };
}

function sessionErrorDetailForMessage(message) {
  const classified = classifyMCPError(message);
  if (classified) {
    return classified;
  }
  return {
    type: 'unknown_error',
    message: message || 'Managed agent run failed',
  };
}

function classifyMCPError(message) {
  const normalized = String(message ?? '').trim();
  const lower = normalized.toLowerCase();
  if (!lower.includes('mcp')) {
    return null;
  }
  const mcpServerName = extractMCPServerName(normalized) || 'unknown';
  if (/(401|403|unauthori[sz]ed|forbidden|authenticat|oauth|token|credential|permission denied)/i.test(normalized)) {
    return {
      type: 'mcp_authentication_failed_error',
      message: normalized,
      retry_status: { type: 'terminal' },
      mcp_server_name: mcpServerName,
    };
  }
  if (/(connect|connection|network|fetch failed|timed out|timeout|unreachable|enotfound|econnrefused|econnreset|socket|dns)/i.test(normalized)) {
    return {
      type: 'mcp_connection_failed_error',
      message: normalized,
      retry_status: { type: 'terminal' },
      mcp_server_name: mcpServerName,
    };
  }
  return null;
}

function extractMCPServerName(message) {
  const patterns = [
    /\bmcp__([^_\s]+)__/i,
    /\bmcp server ["']([^"']+)["']/i,
    /\bmcp server ([A-Za-z0-9_.-]+)/i,
    /\bserver ["']([^"']+)["']/i,
  ];
  for (const pattern of patterns) {
    const match = pattern.exec(message);
    if (match?.[1]) {
      return match[1];
    }
  }
  return '';
}
