const { EventEmitter } = require("node:events");
const { randomUUID } = require("node:crypto");
const {
  agentMessageItem,
  commandExecutionItem,
  dynamicToolCallItem,
  fileChangeItem,
  nowSeconds,
  reasoningItem,
  threadObject,
  threadStartResponse,
  turnObject,
} = require("./codex-shapes");

class ManagedCodexTransport extends EventEmitter {
  constructor({ managedClient, store, binding }) {
    super();
    this.managedClient = managedClient;
    this.store = store;
    this.binding = binding;
    this.streamAbort = null;
    this.seenEventIDsBySession = new Map();
    this.pendingApprovals = new Map();
    this.currentTurnIDBySession = new Map();
    queueMicrotask(() => this.emit("started", { mode: "managed-agents" }));
  }

  send(rawMessage) {
    let message;
    try {
      message = JSON.parse(String(rawMessage || ""));
    } catch {
      return;
    }
    if (message && Object.prototype.hasOwnProperty.call(message, "id") && !message.method) {
      void this.handleClientResponse(message);
      return;
    }
    const method = String(message?.method || "").trim();
    if (!method) {
      return;
    }
    void this.handleRequest(message, method).catch((error) => {
      if (message.id != null) {
        this.emitMessage({ id: message.id, error: rpcError(error) });
      } else {
        this.emitMessage({ method: "error", params: { message: error.message || String(error) } });
      }
    });
  }

  shutdown() {
    this.streamAbort?.abort?.();
    this.removeAllListeners();
  }

  async handleRequest(message, method) {
    const params = message.params || {};
    switch (method) {
      case "initialize":
        this.respond(message.id, {
          serverInfo: { name: "sandbox0-managed-agents-app-gateway", version: "0.1.0" },
          capabilities: { experimentalApi: true },
          bridgeManaged: true,
        });
        return;
      case "initialized":
        return;
      case "thread/list":
        await this.handleThreadList(message);
        return;
      case "thread/start":
        await this.handleThreadStart(message, params);
        return;
      case "thread/resume":
        await this.handleThreadResume(message, params);
        return;
      case "thread/read":
        await this.handleThreadRead(message, params);
        return;
      case "thread/name/set":
      case "thread/archive":
      case "thread/unarchive":
        this.respond(message.id, { ok: true });
        return;
      case "turn/start":
        await this.handleTurnStart(message, params);
        return;
      case "turn/interrupt":
        await this.handleTurnInterrupt(message, params);
        return;
      case "account/read":
      case "getAuthStatus":
      case "account/status/read":
        this.respond(message.id, accountStatus());
        return;
      case "account/rateLimits/read":
        this.respond(message.id, { rateLimits: [], snapshots: [] });
        return;
      case "model/list":
        this.respond(message.id, { models: [{ id: "managed-agent", name: "Managed Agent" }] });
        return;
      case "skills/list":
        this.respond(message.id, { skills: [] });
        return;
      case "thread/contextWindow/read":
        this.respond(message.id, { threadId: params.threadId || params.thread_id || "", usage: null, rolloutPath: null });
        return;
      case "notifications/push/register":
        this.respond(message.id, { ok: false, skipped: true });
        return;
      case "desktop/preferences/read":
        this.respond(message.id, {
          success: true,
          preferences: { keepMacAwake: false },
          applied: false,
        });
        return;
      case "desktop/preferences/update":
        this.respond(message.id, {
          success: true,
          preferences: { keepMacAwake: Boolean(params.keepMacAwake) },
          applied: false,
        });
        return;
      case "desktop/wakeDisplay":
      case "desktop/continueOnMac":
        this.respond(message.id, { success: false, skipped: true, reason: "cloud_managed_session" });
        return;
      case "voice/transcribe":
      case "voice/resolveAuth":
      case "account/login/openOnMac":
        this.respondError(message.id, `${method} is unavailable for cloud-managed Remodex sessions`, "cloud_session_unsupported");
        return;
      default:
        if (method.startsWith("git/") || method.startsWith("workspace/")) {
          this.respondError(
            message.id,
            `${method} requires direct sandbox filesystem access and is not exposed by Managed Agents SDK yet`,
            "sandbox_fallback_unavailable"
          );
          return;
        }
        this.respondError(message.id, `Unsupported Remodex RPC over managed agents: ${method}`, "unsupported_method");
    }
  }

  async handleThreadList(message) {
    const sessions = await this.managedClient.listSessions({ agent_id: this.binding.agent_id }).catch(() => []);
    const items = sessions.map((session) => threadObject({
      id: session.id,
      name: session.title || null,
      preview: session.title || "",
      status: session.status === "running" ? "running" : "idle",
      turns: [],
    }));
    if (this.binding.last_session_id && !items.some((thread) => thread.id === this.binding.last_session_id)) {
      items.unshift(threadObject({ id: this.binding.last_session_id, name: "Managed agent session" }));
    }
    this.respond(message.id, { threads: items, items });
  }

  async handleThreadStart(message, params) {
    const session = await this.ensureSession({
      forceNew: true,
      title: firstUserText(params.input) || "Remodex managed agent session",
    });
    const thread = threadObject({
      id: session.id,
      name: session.title || null,
      preview: session.title || "",
      status: "idle",
    });
    this.respond(message.id, threadStartResponse(thread));
    this.emitMessage({ method: "thread/started", params: { thread } });
    await this.startEventStream(session.id);
  }

  async handleThreadResume(message, params) {
    const sessionID = String(params.threadId || params.thread_id || this.binding.last_session_id || "").trim();
    if (!sessionID) {
      throw new Error("thread/resume requires threadId or an existing binding session");
    }
    await this.startEventStream(sessionID);
    const thread = await this.threadFromSession(sessionID, { includeTurns: true });
    this.respond(message.id, threadStartResponse(thread));
  }

  async handleThreadRead(message, params) {
    const sessionID = String(params.threadId || params.thread_id || this.binding.last_session_id || "").trim();
    if (!sessionID) {
      throw new Error("thread/read requires threadId or an existing binding session");
    }
    const thread = await this.threadFromSession(sessionID, { includeTurns: Boolean(params.includeTurns ?? params.include_turns ?? true) });
    this.respond(message.id, { thread });
  }

  async handleTurnStart(message, params) {
    const session = await this.ensureSession({
      threadID: params.threadId || params.thread_id,
      title: firstUserText(params.input) || "Remodex turn",
    });
    const sessionID = session.id;
    const turnID = `turn_${randomUUID()}`;
    this.currentTurnIDBySession.set(sessionID, turnID);
    const turn = turnObject({ id: turnID, status: "running" });
    this.respond(message.id, { turn });
    this.emitMessage({ method: "turn/started", params: { threadId: sessionID, turn } });
    this.emitMessage({ method: "thread/status/changed", params: { threadId: sessionID, status: "running" } });
    await this.startEventStream(sessionID);
    const text = firstUserText(params.input) || firstUserText(params.items) || "";
    await this.managedClient.sendUserMessage(sessionID, text);
  }

  async handleTurnInterrupt(message, params) {
    const sessionID = String(params.threadId || params.thread_id || this.binding.last_session_id || "").trim();
    if (sessionID) {
      await this.managedClient.sendInterrupt(sessionID);
    }
    this.respond(message.id, { ok: true });
  }

  async ensureSession({ forceNew = false, threadID = "", title = "" } = {}) {
    const existingID = String(threadID || (!forceNew ? this.binding.last_session_id : "") || "").trim();
    if (existingID) {
      return { id: existingID, title };
    }
    const session = await this.managedClient.createSession({
      agentID: this.binding.agent_id,
      environmentID: this.binding.environment_id,
      title,
      vaultIDs: this.binding.vault_ids || [],
      metadata: {
        ...(this.binding.metadata || {}),
        "sandbox0.app_gateway.app": "remodex",
        "sandbox0.app_gateway.binding_id": this.binding.id,
        "sandbox0.app_gateway.external_id": this.binding.external_id,
      },
    });
    this.binding = this.store.setLastSession(this.binding.id, session.id) || { ...this.binding, last_session_id: session.id };
    return session;
  }

  async threadFromSession(sessionID, { includeTurns = true } = {}) {
    const [session, events] = await Promise.all([
      this.managedClient.retrieveSession(sessionID).catch(() => ({ id: sessionID, title: "Managed agent session", status: "idle" })),
      includeTurns ? this.managedClient.listEvents(sessionID, { limit: 100 }).catch(() => []) : Promise.resolve([]),
    ]);
    return threadObject({
      id: sessionID,
      name: session.title || null,
      preview: session.title || "",
      status: session.status === "running" ? "running" : "idle",
      turns: includeTurns ? eventsToTurns(sessionID, events) : [],
    });
  }

  async startEventStream(sessionID) {
    if (this.activeStreamSessionID === sessionID) {
      return;
    }
    this.streamAbort?.abort?.();
    this.activeStreamSessionID = sessionID;
    const controller = new AbortController();
    this.streamAbort = controller;
    const events = await this.managedClient.listEvents(sessionID, { limit: 100 }).catch(() => []);
    for (const event of events) {
      this.markSeen(sessionID, event);
    }
    queueMicrotask(async () => {
      try {
        const stream = await this.managedClient.streamEvents(sessionID);
        controller.abort = () => stream.controller?.abort?.();
        for await (const event of stream) {
          if (this.hasSeen(sessionID, event)) {
            continue;
          }
          this.markSeen(sessionID, event);
          await this.emitManagedEvent(sessionID, event);
        }
      } catch (error) {
        if (!String(error?.message || error).toLowerCase().includes("abort")) {
          this.emitMessage({ method: "error", params: { message: error.message || String(error), threadId: sessionID } });
        }
      }
    });
  }

  hasSeen(sessionID, event) {
    const id = eventID(event);
    return id && this.seenEventIDsBySession.get(sessionID)?.has(id);
  }

  markSeen(sessionID, event) {
    const id = eventID(event);
    if (!id) {
      return;
    }
    const seen = this.seenEventIDsBySession.get(sessionID) || new Set();
    seen.add(id);
    this.seenEventIDsBySession.set(sessionID, seen);
  }

  async emitManagedEvent(sessionID, event) {
    const turnID = this.currentTurnIDBySession.get(sessionID) || `turn_${sessionID}`;
    switch (event.type) {
      case "session.status_running":
        this.emitMessage({ method: "thread/status/changed", params: { threadId: sessionID, status: "running" } });
        break;
      case "agent.thinking": {
        const item = reasoningItem(event.id || `thinking_${turnID}`, []);
        this.emitMessage({ method: "item/started", params: { threadId: sessionID, turnId: turnID, item } });
        this.emitMessage({ method: "item/completed", params: { threadId: sessionID, turnId: turnID, item } });
        break;
      }
      case "agent.message": {
        const text = eventText(event);
        const itemID = event.id || `msg_${randomUUID()}`;
        const item = agentMessageItem(itemID, text);
        this.emitMessage({ method: "item/started", params: { threadId: sessionID, turnId: turnID, item: agentMessageItem(itemID, "") } });
        if (text) {
          this.emitMessage({ method: "item/agentMessage/delta", params: { threadId: sessionID, turnId: turnID, itemId: itemID, delta: text } });
        }
        this.emitMessage({ method: "item/completed", params: { threadId: sessionID, turnId: turnID, item } });
        break;
      }
      case "agent.tool_use":
      case "agent.mcp_tool_use": {
        const item = toolEventToItem(event, "running");
        this.emitMessage({ method: "item/started", params: { threadId: sessionID, turnId: turnID, item } });
        if (event.evaluated_permission === "ask") {
          this.requestApproval(sessionID, turnID, event, item);
        }
        break;
      }
      case "agent.tool_result":
      case "agent.mcp_tool_result": {
        const itemID = event.tool_use_id || event.mcp_tool_use_id || event.id || `tool_${randomUUID()}`;
        const item = dynamicToolCallItem(itemID, "tool", {}, event.is_error ? "failed" : "completed", !event.is_error);
        this.emitMessage({ method: "item/completed", params: { threadId: sessionID, turnId: turnID, item } });
        break;
      }
      case "session.status_idle": {
        const status = stopReasonType(event) === "requires_action" ? "idle" : "completed";
        const turn = turnObject({ id: turnID, status, completedAt: nowSeconds() });
        this.emitMessage({ method: "turn/completed", params: { threadId: sessionID, turn } });
        this.emitMessage({ method: "thread/status/changed", params: { threadId: sessionID, status: "idle" } });
        break;
      }
      case "session.status_terminated": {
        const turn = turnObject({ id: turnID, status: "failed", completedAt: nowSeconds(), error: { message: "Session terminated" } });
        this.emitMessage({ method: "turn/completed", params: { threadId: sessionID, turn } });
        this.emitMessage({ method: "thread/status/changed", params: { threadId: sessionID, status: "failed" } });
        break;
      }
      default:
        break;
    }
  }

  requestApproval(sessionID, turnID, event, item) {
    const requestID = `approval_${event.id || randomUUID()}`;
    this.pendingApprovals.set(requestID, {
      sessionID,
      toolUseID: event.id,
    });
    const method = item.type === "fileChange"
      ? "item/fileChange/requestApproval"
      : "item/commandExecution/requestApproval";
    this.emitMessage({
      id: requestID,
      method,
      params: {
        threadId: sessionID,
        turnId: turnID,
        itemId: item.id,
        approvalId: event.id,
        item,
      },
    });
  }

  async handleClientResponse(message) {
    const pending = this.pendingApprovals.get(String(message.id));
    if (!pending) {
      return;
    }
    this.pendingApprovals.delete(String(message.id));
    const decision = String(message?.result?.decision || "").toLowerCase();
    const result = decision === "accept" || decision === "acceptforsession" ? "allow" : "deny";
    await this.managedClient.sendToolConfirmation(pending.sessionID, pending.toolUseID, result, result === "deny" ? "Denied from Remodex" : "");
    this.emitMessage({ method: "serverRequest/resolved", params: { requestId: String(message.id) } });
  }

  respond(id, result) {
    if (id == null) {
      return;
    }
    this.emitMessage({ id, result });
  }

  respondError(id, message, errorCode = "request_failed") {
    if (id == null) {
      return;
    }
    this.emitMessage({ id, error: { code: -32000, message, data: { errorCode } } });
  }

  emitMessage(message) {
    this.emit("message", JSON.stringify(message));
  }
}

function eventsToTurns(sessionID, events) {
  const items = [];
  for (const event of events || []) {
    if (event.type === "agent.message") {
      items.push(agentMessageItem(event.id || `msg_${items.length}`, eventText(event)));
    } else if (event.type === "agent.thinking") {
      items.push(reasoningItem(event.id || `thinking_${items.length}`));
    } else if (event.type === "agent.tool_use" || event.type === "agent.mcp_tool_use") {
      items.push(toolEventToItem(event, "completed"));
    }
  }
  if (items.length === 0) {
    return [];
  }
  return [turnObject({ id: `turn_${sessionID}`, status: "completed", items, completedAt: nowSeconds() })];
}

function toolEventToItem(event, status) {
  const id = event.id || `tool_${randomUUID()}`;
  const input = event.input || {};
  if (event.name === "bash" || event.name === "shell" || input.command) {
    return commandExecutionItem(id, input, status);
  }
  if (event.name === "edit" || event.name === "apply_patch") {
    return fileChangeItem(id, input, status === "failed" ? "failed" : "applied");
  }
  return dynamicToolCallItem(id, event.name || "tool", input, status, status !== "failed");
}

function firstUserText(input) {
  if (typeof input === "string") {
    return input.trim();
  }
  if (!Array.isArray(input)) {
    return "";
  }
  return input.map((item) => {
    if (typeof item === "string") {
      return item;
    }
    return item?.text || item?.content?.text || "";
  }).filter(Boolean).join("\n").trim();
}

function eventText(event) {
  return Array.isArray(event?.content)
    ? event.content.filter((block) => block?.type === "text").map((block) => block.text || "").join("\n")
    : "";
}

function eventID(event) {
  return String(event?.id || event?.processed_at || "");
}

function stopReasonType(event) {
  return String(event?.stop_reason?.type || "");
}

function accountStatus() {
  return {
    requiresOpenaiAuth: false,
    account: { email: "managed-agents@sandbox0.ai", planType: "managed-agents" },
    auth: { mode: "managed-agents", authenticated: true },
    transportMode: "managed-agents",
  };
}

function rpcError(error) {
  return {
    code: -32000,
    message: error?.message || String(error || "request failed"),
    data: { errorCode: error?.errorCode || "request_failed" },
  };
}

module.exports = { ManagedCodexTransport };
