import { EventEmitter } from 'node:events';
import fs from 'node:fs';
import path from 'node:path';
import { inputEventsToPrompt } from '../lib/prompt.js';
import { newID } from '../lib/ids.js';
import { logInfo, logWarn, summarizePendingActions } from '../lib/log.js';
import { buildToolPlan, resolveToolPolicy } from './claude.js';
import { CodexAppServerClient } from './codex-app-server.js';
import {
  AgentRuntime,
  agentWrapperStateDir,
  finalStatusEventForSessionError,
  runtimeEnvForEngine,
  runtimeModelForSession,
  sessionErrorEventForError,
} from './runtime.js';

export class CodexRuntime extends AgentRuntime {
  constructor({ clientFactory } = {}) {
    super('codex');
    this.clientFactory = clientFactory ?? ((session) => new CodexAppServerClient(codexClientOptions(session)));
    this.clients = new Map();
    this.activeRuns = new Map();
    this.pendingActions = new Map();
    this.emittedToolUses = new Map();
  }

  async startRun(session, run, callbackClient, sessionStore) {
    let currentSession = session;
    const bootstrapEvents = Array.isArray(session.bootstrap_events)
      ? session.bootstrap_events.filter((event) => event && typeof event === 'object')
      : [];
    if (bootstrapEvents.length > 0) {
      await callbackClient.send(currentSession, {
        session_id: currentSession.session_id,
        run_id: run.run_id,
        vendor_session_id: currentSession.vendor_session_id,
        events: bootstrapEvents,
      });
      currentSession = sessionStore.persistSession((latest) => ({
        ...latest,
        bootstrap_events: [],
        updated_at: new Date().toISOString(),
      }));
    }

    const client = await this.#clientForSession(currentSession);
    currentSession = await this.#ensureThread(client, currentSession, sessionStore);
    const modelRequestStartID = newEventID('span');
    await callbackClient.send(currentSession, {
      session_id: currentSession.session_id,
      run_id: run.run_id,
      vendor_session_id: currentSession.vendor_session_id,
      events: [{ type: 'span.model_request_start', id: modelRequestStartID }],
    });

    const turnRef = { threadID: currentSession.vendor_session_id, turnID: null };
    const watcher = this.#watchTurn(client, currentSession, run, callbackClient, sessionStore, modelRequestStartID, turnRef);
    try {
      const response = await client.request('turn/start', buildTurnStartParams(currentSession, run));
      turnRef.turnID = String(response?.turn?.id ?? '');
      this.activeRuns.set(run.run_id, {
        client,
        sessionID: currentSession.session_id,
        threadID: turnRef.threadID,
        turnID: turnRef.turnID,
      });
      await watcher.promise;
    } catch (error) {
      watcher.cleanup();
      const latestSession = sessionStore.getSession?.() ?? currentSession;
      const errorEvent = sessionErrorEventForError(error);
      logWarn('codex run failed', {
        session_id: latestSession.session_id,
        run_id: run.run_id,
        vendor_session_id: latestSession.vendor_session_id ?? null,
        error: errorEvent.error.message,
        error_type: errorEvent.error.type,
        retry_status: errorEvent.error.retry_status?.type ?? null,
      });
      await callbackClient.send(latestSession, {
        session_id: latestSession.session_id,
        run_id: run.run_id,
        vendor_session_id: latestSession.vendor_session_id,
        events: [
          buildModelRequestEndEvent(modelRequestStartID, null, true),
          errorEvent,
          finalStatusEventForSessionError(errorEvent),
        ],
      });
    } finally {
      this.activeRuns.delete(run.run_id);
      this.pendingActions.delete(currentSession.session_id);
      this.emittedToolUses.delete(currentSession.session_id);
      this.emittedToolUses.delete(`${currentSession.session_id}:items`);
    }
  }

  async interruptRun(runID) {
    const active = this.activeRuns.get(runID);
    if (!active) {
      return false;
    }
    await active.client.request('turn/interrupt', {
      threadId: active.threadID,
      turnId: active.turnID,
    });
    return true;
  }

  async deleteSession(sessionID) {
    const client = this.clients.get(sessionID);
    if (client) {
      client.close?.();
      this.clients.delete(sessionID);
    }
    this.pendingActions.delete(sessionID);
    this.emittedToolUses.delete(sessionID);
    this.emittedToolUses.delete(`${sessionID}:items`);
  }

  async resolveActions(sessionID, events, sessionStore) {
    const currentSession = sessionStore.getSession?.();
    const pendingSnapshots = pendingActionSnapshotMap(currentSession?.pending_actions);
    const pendingPromises = this.pendingActions.get(sessionID);
    const hasVendorSession = typeof currentSession?.vendor_session_id === 'string' && currentSession.vendor_session_id.trim() !== '';
    const hasToolConfirmationInput = (events ?? []).some((event) => event?.type === 'user.tool_confirmation');
    if (pendingSnapshots.size === 0 && (!pendingPromises || pendingPromises.size === 0) && !(hasVendorSession && hasToolConfirmationInput)) {
      logWarn('codex resolve actions without pending state', {
        session_id: sessionID,
        vendor_session_id: currentSession?.vendor_session_id ?? null,
        ...summarizePendingActions(currentSession?.pending_actions),
        in_memory_pending_count: pendingPromises?.size ?? 0,
        input_event_types: (events ?? []).map((event) => event?.type ?? null),
      });
      return { resolved_count: 0, remaining_action_ids: [], resume_required: false };
    }
    let resolved = 0;
    let resumeRequired = false;
    const toolConfirmationResolutions = {
      ...(currentSession?.tool_confirmation_resolutions ?? {}),
    };
    for (const event of events ?? []) {
      if (!event || typeof event !== 'object') {
        continue;
      }
      if (event.type === 'user.tool_confirmation') {
        const actionID = String(event.tool_use_id ?? '');
        const pendingAction = pendingPromises?.get(actionID);
        const action = pendingAction ?? pendingSnapshots.get(actionID);
        if ((!action || action.kind !== 'tool_confirmation') && !hasVendorSession) {
          continue;
        }
        const decision = toolConfirmationDecision(event, action);
        if (pendingAction && pendingAction.kind === 'tool_confirmation') {
          pendingPromises.delete(actionID);
          pendingSnapshots.delete(actionID);
          resolved += 1;
          pendingAction.resolve(decision);
          continue;
        }
        if (action && action.kind === 'tool_confirmation') {
          pendingSnapshots.delete(action.id);
        }
        resolved += 1;
        resumeRequired = true;
        toolConfirmationResolutions[actionID] = {
          result: decision,
        };
        continue;
      }
      if (event.type === 'user.custom_tool_result') {
        const actionID = String(event.custom_tool_use_id ?? '');
        const action = pendingPromises?.get(actionID);
        if (!action || action.kind !== 'custom_tool_result') {
          continue;
        }
        pendingPromises.delete(actionID);
        pendingSnapshots.delete(actionID);
        resolved += 1;
        action.resolve(customToolResult(event));
      }
    }
    if (!pendingPromises || pendingPromises.size === 0) {
      this.pendingActions.delete(sessionID);
    }
    const remainingActionIDs = Array.from(pendingSnapshots.keys());
    sessionStore.persistSession((current) => ({
      ...current,
      pending_actions: pendingActionSnapshots(pendingSnapshots),
      tool_confirmation_resolutions: toolConfirmationResolutions,
      updated_at: new Date().toISOString(),
    }));
    const response = {
      resolved_count: resolved,
      remaining_action_ids: remainingActionIDs,
      resume_required: resumeRequired && remainingActionIDs.length === 0,
    };
    logInfo('codex resolve actions', {
      session_id: sessionID,
      vendor_session_id: currentSession?.vendor_session_id ?? null,
      resolved_count: response.resolved_count,
      remaining_action_ids: response.remaining_action_ids,
      resume_required: response.resume_required,
      input_event_types: (events ?? []).map((event) => event?.type ?? null),
    });
    return response;
  }

  async #clientForSession(session) {
    const existing = this.clients.get(session.session_id);
    if (existing) {
      return existing;
    }
    const client = this.clientFactory(session);
    if (!(client instanceof EventEmitter) && typeof client.on !== 'function') {
      throw new Error('codex client must provide EventEmitter-compatible on/off methods');
    }
    client.on?.('stderr', (line) => {
      logWarn('codex app-server stderr', {
        session_id: session.session_id,
        vendor_session_id: session.vendor_session_id ?? null,
        data: line,
      });
    });
    await client.start?.();
    this.clients.set(session.session_id, client);
    return client;
  }

  async #ensureThread(client, session, sessionStore) {
    if (session.vendor_session_id) {
      await client.request('thread/resume', {
        threadId: session.vendor_session_id,
        ...buildThreadResumeParams(session),
      });
      return session;
    }
    const response = await client.request('thread/start', buildThreadStartParams(session));
    const threadID = String(response?.thread?.id ?? '').trim();
    if (!threadID) {
      throw new Error('codex app-server thread/start did not return thread.id');
    }
    return sessionStore.persistSession((current) => ({
      ...current,
      vendor_session_id: threadID,
      updated_at: new Date().toISOString(),
    }));
  }

  #watchTurn(client, session, run, callbackClient, sessionStore, modelRequestStartID, turnRef) {
    let usage = null;
    let cleanup = () => {};
    const promise = new Promise((resolve, reject) => {
      cleanup = () => {
        client.off?.('notification', onNotification);
        client.off?.('serverRequest', onServerRequest);
        client.off?.('error', onError);
        client.off?.('close', onClose);
      };
      const onError = (error) => {
        cleanup();
        reject(error);
      };
      const onClose = () => {
        cleanup();
        reject(new Error('codex app-server closed during run'));
      };
      const onNotification = async (message) => {
        try {
          if (!notificationMatchesTurn(message, turnRef)) {
            return;
          }
          if (message.method === 'turn/started') {
            turnRef.turnID = String(message.params?.turn?.id ?? turnRef.turnID ?? '');
            return;
          }
          if (message.method === 'thread/tokenUsage/updated') {
            usage = message.params?.tokenUsage?.last ?? usage;
            return;
          }
          const payload = this.#mapNotification(sessionStore.getSession?.() ?? session, run, message);
          if (payload) {
            await callbackClient.send(sessionStore.getSession?.() ?? session, payload);
          }
          if (message.method === 'turn/completed') {
            const latestSession = sessionStore.getSession?.() ?? session;
            await callbackClient.send(latestSession, buildTurnCompletedPayload(latestSession, run, message.params?.turn, modelRequestStartID, usage));
            cleanup();
            resolve();
          }
        } catch (error) {
          cleanup();
          reject(error);
        }
      };
      const onServerRequest = async (message) => {
        if (!serverRequestMatchesTurn(message, turnRef)) {
          return;
        }
        try {
          const result = await this.#handleServerRequest(sessionStore.getSession?.() ?? session, run, callbackClient, sessionStore, message);
          client.respond(message.id, result);
        } catch (error) {
          client.respondError(message.id, error);
        }
      };
      client.on('notification', onNotification);
      client.on('serverRequest', onServerRequest);
      client.on('error', onError);
      client.on('close', onClose);
    });
    return { promise, cleanup };
  }

  #mapNotification(session, run, message) {
    if (message.method !== 'item/started' && message.method !== 'item/completed') {
      return null;
    }
    const item = message.params?.item;
    const events = mapThreadItem(this, session, item, message.method === 'item/completed');
    if (events.length === 0) {
      return null;
    }
    return {
      session_id: session.session_id,
      run_id: run.run_id,
      vendor_session_id: session.vendor_session_id,
      events,
    };
  }

  async #handleServerRequest(session, run, callbackClient, sessionStore, message) {
    switch (message.method) {
      case 'item/commandExecution/requestApproval':
        return this.#handleToolConfirmation(session, run, callbackClient, sessionStore, message, 'bash');
      case 'item/fileChange/requestApproval':
        return this.#handleToolConfirmation(session, run, callbackClient, sessionStore, message, 'edit');
      case 'item/tool/call':
        return this.#handleCustomToolCall(session, run, callbackClient, sessionStore, message);
      default:
        logWarn('unsupported codex server request', { method: message.method });
        return {};
    }
  }

  async #handleToolConfirmation(session, run, callbackClient, sessionStore, message, toolName) {
    const params = message.params ?? {};
    const actionID = firstNonEmptyString(params.approvalId, params.itemId, newEventID('toolu'));
    const input = toolInputForApproval(params, toolName, actionID);
    const toolPlan = buildToolPlan(session.agent?.tools);
    const resolvedTool = resolveToolPolicy(toolPlan, input);
    const evaluatedPermission = resolvedTool.enabled
      ? (resolvedTool.policy === 'always_allow' ? 'allow' : 'ask')
      : 'deny';
    const existingResolution = session?.tool_confirmation_resolutions?.[actionID]?.result;
    if (existingResolution) {
      sessionStore.persistSession((latest) => ({
        ...latest,
        pending_actions: pendingActionSnapshots(removePendingAction(latest?.pending_actions, actionID)),
        tool_confirmation_resolutions: omitActionResolution(latest?.tool_confirmation_resolutions, actionID),
        updated_at: new Date().toISOString(),
      }));
      return existingResolution;
    }
    rememberToolUse(this, session.session_id, params.itemId, actionID);
    if (!this.wasToolUseEmitted(session.session_id, actionID)) {
      this.markToolUseEmitted(session.session_id, actionID);
      await callbackClient.send(session, {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: session.vendor_session_id,
        events: evaluatedPermission === 'ask'
          ? [toolUseEvent(input, actionID, evaluatedPermission), requiresActionEvent(actionID)]
          : [toolUseEvent(input, actionID, evaluatedPermission)],
      });
    }
    if (!resolvedTool.enabled) {
      return { decision: 'cancel' };
    }
    if (resolvedTool.policy === 'always_allow') {
      return { decision: 'accept' };
    }
    return this.#registerPendingAction(session, {
      id: actionID,
      kind: 'tool_confirmation',
      tool_use_id: actionID,
      name: resolvedTool.name,
      input: input.tool_input,
      responseKind: toolName === 'edit' ? 'file_change' : 'command',
      snapshot: {
        id: actionID,
        kind: 'tool_confirmation',
        tool_use_id: actionID,
        name: resolvedTool.name,
        input: input.tool_input,
        response_kind: toolName === 'edit' ? 'file_change' : 'command',
      },
    }, sessionStore).promise;
  }

  async #handleCustomToolCall(session, run, callbackClient, sessionStore, message) {
    const params = message.params ?? {};
    const actionID = firstNonEmptyString(params.callId, newEventID('ctoolu'));
    const toolName = firstNonEmptyString(params.tool, 'custom_tool');
    const pending = this.#registerPendingAction(session, {
      id: actionID,
      kind: 'custom_tool_result',
      custom_tool_use_id: actionID,
      name: toolName,
      snapshot: {
        id: actionID,
        kind: 'custom_tool_result',
        custom_tool_use_id: actionID,
        name: toolName,
      },
    }, sessionStore);
    await callbackClient.send(session, {
      session_id: session.session_id,
      run_id: run.run_id,
      vendor_session_id: session.vendor_session_id,
      events: [
        { type: 'agent.custom_tool_use', id: actionID, name: toolName, input: asStruct(params.arguments) },
        requiresActionEvent(actionID),
      ],
    });
    return pending.promise;
  }

  #registerPendingAction(session, action, sessionStore) {
    const deferred = deferredPromise();
    const pending = this.pendingActions.get(session.session_id) ?? new Map();
    pending.set(action.id, { ...action, resolve: deferred.resolve, reject: deferred.reject });
    this.pendingActions.set(session.session_id, pending);
    sessionStore.persistSession((current) => ({
      ...current,
      pending_actions: pendingActionSnapshots(mergePendingSnapshots(current?.pending_actions, pending)),
      updated_at: new Date().toISOString(),
    }));
    return deferred;
  }

  wasToolUseEmitted(sessionID, eventID) {
    if (!eventID) {
      return false;
    }
    return this.emittedToolUses.get(sessionID)?.has(eventID) ?? false;
  }

  markToolUseEmitted(sessionID, eventID) {
    if (!eventID) {
      return;
    }
    const seen = this.emittedToolUses.get(sessionID) ?? new Set();
    seen.add(eventID);
    this.emittedToolUses.set(sessionID, seen);
  }
}

function codexClientOptions(session) {
  const engine = session.engine ?? {};
  const env = runtimeEnvForEngine(engine);
  if (!env.CODEX_HOME) {
    env.CODEX_HOME = path.join(agentWrapperStateDir(env), 'codex');
  }
  fs.mkdirSync(env.CODEX_HOME, { recursive: true });
  return {
    command: firstNonEmptyString(engine.path_to_codex_executable, engine.codex_executable, process.env.CODEX_EXECUTABLE, 'codex'),
    args: Array.isArray(engine.app_server_args) && engine.app_server_args.length > 0
      ? engine.app_server_args.map((value) => String(value))
      : ['app-server', '--listen', 'stdio://'],
    cwd: session.working_directory,
    env,
    requestTimeoutMs: finiteNumber(engine.app_server_request_timeout_ms, 120000),
  };
}

function buildThreadStartParams(session) {
  return buildThreadParams(session, { includeDynamicTools: true, includeServiceName: true });
}

function buildThreadResumeParams(session) {
  return buildThreadParams(session, { includeDynamicTools: false, includeServiceName: false });
}

function buildThreadParams(session, { includeDynamicTools, includeServiceName }) {
  const engine = session.engine ?? {};
  const params = {
    cwd: session.working_directory,
    model: runtimeModelForSession(session),
    approvalPolicy: codexApprovalPolicy(engine),
    sandbox: engine.sandbox ?? 'workspace-write',
    serviceName: includeServiceName ? 'sandbox0_managed_agents' : undefined,
    personality: engine.personality ?? 'none',
    config: codexConfigForSession(session),
  };
  if (typeof session.agent?.system === 'string' && session.agent.system.trim() !== '') {
    params.developerInstructions = session.agent.system;
  }
  const dynamicTools = includeDynamicTools ? dynamicToolsForAgent(session.agent?.tools) : [];
  if (dynamicTools.length > 0) {
    params.dynamicTools = dynamicTools;
  }
  return omitUndefined(params);
}

function buildTurnStartParams(session, run) {
  const engine = session.engine ?? {};
  return omitUndefined({
    threadId: session.vendor_session_id,
    input: inputEventsToCodexInput(run.input_events, session),
    model: runtimeModelForSession(session),
    cwd: session.working_directory,
    approvalPolicy: codexApprovalPolicy(engine),
    sandboxPolicy: engine.sandbox_policy ?? workspaceSandboxPolicy(session.working_directory, engine),
    effort: engine.reasoning_effort,
    summary: engine.reasoning_summary,
    serviceTier: engine.service_tier,
    personality: engine.personality ?? 'none',
  });
}

function codexConfigForSession(session) {
  const engine = session.engine ?? {};
  const config = engine.config && typeof engine.config === 'object' && !Array.isArray(engine.config)
    ? { ...engine.config }
    : {};
  if (engine.openai_base_url && !config.openai_base_url) {
    config.openai_base_url = engine.openai_base_url;
  }
  if (engine.model_provider && !config.model_provider) {
    config.model_provider = engine.model_provider;
  }
  return Object.keys(config).length > 0 ? config : undefined;
}

function codexApprovalPolicy(engine) {
  if (engine.approval_policy) {
    return engine.approval_policy;
  }
  return { granular: { sandbox_approval: true, rules: true, skill_approval: true, request_permissions: true, mcp_elicitations: true } };
}

function workspaceSandboxPolicy(workingDirectory, engine) {
  const networkAccess = engine.network_access === false ? false : true;
  const writableRoots = Array.isArray(engine.writable_roots) && engine.writable_roots.length > 0
    ? engine.writable_roots.map((value) => String(value))
    : [workingDirectory].filter(Boolean);
  return {
    type: 'workspaceWrite',
    writableRoots,
    networkAccess,
    readOnlyAccess: { type: 'fullAccess' },
  };
}

function inputEventsToCodexInput(events, session) {
  const items = [];
  for (const event of events ?? []) {
    if (!event || typeof event !== 'object' || event.type === 'user.tool_confirmation' || event.type === 'user.custom_tool_result') {
      continue;
    }
    if (event.type === 'user.message') {
      const text = userMessageText(event.content);
      if (text) {
        items.push({ type: 'text', text });
      }
      continue;
    }
    const text = inputEventsToPrompt([event]);
    if (text) {
      items.push({ type: 'text', text });
    }
  }
  if (items.length === 0) {
    items.push({ type: 'text', text: '' });
  }
  return [...items, ...skillInputItemsForSession(session)];
}

function userMessageText(content) {
  if (typeof content === 'string') {
    return content;
  }
  if (!Array.isArray(content)) {
    return '';
  }
  const lines = [];
  for (const block of content) {
    if (block?.type === 'text' && typeof block.text === 'string') {
      lines.push(block.text);
      continue;
    }
    if (block && typeof block === 'object') {
      lines.push(JSON.stringify(block));
    }
  }
  return lines.join('\n');
}

function dynamicToolsForAgent(tools) {
  const out = [];
  for (const tool of tools ?? []) {
    if (!tool || typeof tool !== 'object' || tool.type !== 'custom') {
      continue;
    }
    const name = firstNonEmptyString(tool.name);
    const description = firstNonEmptyString(tool.description);
    if (!name || !description) {
      continue;
    }
    out.push({
      name,
      description,
      inputSchema: tool.input_schema ?? tool.inputSchema ?? { type: 'object', additionalProperties: true },
    });
  }
  return out;
}

function skillInputItemsForSession(session) {
  if (!Array.isArray(session?.skill_names)) {
    return [];
  }
  const workingDirectory = firstNonEmptyString(session.working_directory, '/workspace');
  return session.skill_names
    .map((value) => String(value ?? '').trim())
    .filter((value) => value.length > 0)
    .map((name) => ({
      type: 'skill',
      name,
      path: path.join(workingDirectory, '.claude', 'skills', name, 'SKILL.md'),
    }));
}

function notificationMatchesTurn(message, turnRef) {
  const params = message.params ?? {};
  if (params.threadId && params.threadId !== turnRef.threadID) {
    return false;
  }
  const turnID = params.turnId ?? params.turn?.id;
  if (turnRef.turnID && turnID && turnID !== turnRef.turnID) {
    return false;
  }
  return true;
}

function serverRequestMatchesTurn(message, turnRef) {
  const params = message.params ?? {};
  if (params.threadId && params.threadId !== turnRef.threadID) {
    return false;
  }
  if (turnRef.turnID && params.turnId && params.turnId !== turnRef.turnID) {
    return false;
  }
  return true;
}

function mapThreadItem(runtime, session, item, completed) {
  if (!item || typeof item !== 'object') {
    return [];
  }
  switch (item.type) {
    case 'agentMessage':
      return completed && item.text ? [{ type: 'agent.message', content: [{ type: 'text', text: item.text }] }] : [];
    case 'reasoning':
      return completed ? [{ type: 'agent.thinking', id: String(item.id ?? newEventID('rsn')) }] : [];
    case 'contextCompaction':
      return completed ? [{ type: 'agent.thread_context_compacted' }] : [];
    case 'commandExecution':
      return commandExecutionEvents(runtime, session, item, completed);
    case 'fileChange':
      return fileChangeEvents(runtime, session, item, completed);
    case 'mcpToolCall':
      return mcpToolEvents(runtime, session, item, completed);
    default:
      return [];
  }
}

function commandExecutionEvents(runtime, session, item, completed) {
  const actionID = toolUseIDForItem(runtime, session.session_id, item.id);
  const input = {
    tool_name: 'bash',
    tool_input: {
      command: item.command,
      cwd: item.cwd,
      command_actions: item.commandActions,
    },
  };
  if (!completed) {
    return [];
  }
  const events = [];
  if (!runtime.wasToolUseEmitted?.(session.session_id, actionID)) {
    events.push(toolUseEvent(input, actionID, 'allow'));
  }
  events.push({
    type: 'agent.tool_result',
    tool_use_id: actionID,
    content: toolResultContent(item.aggregatedOutput ?? ''),
    is_error: item.status === 'failed' || item.status === 'declined' || undefined,
  });
  return events;
}

function fileChangeEvents(runtime, session, item, completed) {
  const actionID = toolUseIDForItem(runtime, session.session_id, item.id);
  if (!completed) {
    return [];
  }
  const events = [];
  if (!runtime.wasToolUseEmitted?.(session.session_id, actionID)) {
    events.push(toolUseEvent({ tool_name: 'edit', tool_input: { changes: item.changes } }, actionID, 'allow'));
  }
  events.push({
    type: 'agent.tool_result',
    tool_use_id: actionID,
    content: toolResultContent(JSON.stringify({ status: item.status, changes: item.changes ?? [] })),
    is_error: item.status === 'failed' || item.status === 'declined' || undefined,
  });
  return events;
}

function mcpToolEvents(runtime, session, item, completed) {
  const actionID = toolUseIDForItem(runtime, session.session_id, item.id);
  if (!completed) {
    if (runtime.wasToolUseEmitted?.(session.session_id, actionID)) {
      return [];
    }
    runtime.markToolUseEmitted?.(session.session_id, actionID);
    return [{
      type: 'agent.mcp_tool_use',
      id: actionID,
      name: String(item.tool ?? ''),
      mcp_server_name: String(item.server ?? ''),
      input: asStruct(item.arguments),
      evaluated_permission: 'allow',
    }];
  }
  return [{
    type: 'agent.mcp_tool_result',
    id: newEventID('evt'),
    mcp_tool_use_id: actionID,
    content: toolResultContent(item.error ? JSON.stringify(item.error) : JSON.stringify(item.result ?? {})),
    is_error: item.status === 'failed' || undefined,
  }];
}

function toolInputForApproval(params, toolName, actionID) {
  if (toolName === 'edit') {
    return {
      tool_name: 'edit',
      tool_use_id: actionID,
      tool_input: asStruct({ item_id: params.itemId, reason: params.reason, grant_root: params.grantRoot }),
    };
  }
  return {
    tool_name: 'bash',
    tool_use_id: actionID,
    tool_input: asStruct({
      command: params.command,
      cwd: params.cwd,
      reason: params.reason,
      command_actions: params.commandActions,
      network_approval_context: params.networkApprovalContext,
    }),
  };
}

function toolUseEvent(input, eventID, evaluatedPermission) {
  return {
    type: 'agent.tool_use',
    id: eventID,
    name: normalizeToolName(input.tool_name),
    input: asStruct(input.tool_input),
    evaluated_permission: evaluatedPermission,
  };
}

function requiresActionEvent(eventID) {
  return { type: 'session.status_idle', stop_reason: { type: 'requires_action', event_ids: [eventID] } };
}

function toolConfirmationDecision(event, action) {
  if (event.result === 'allow') {
    return { decision: 'accept' };
  }
  return { decision: action?.responseKind === 'file_change' ? 'cancel' : 'cancel' };
}

function customToolResult(event) {
  return {
    contentItems: toolResultContent(event.content).map((block) => ({ type: 'inputText', text: block.text })),
    success: event.is_error !== true,
  };
}

function buildTurnCompletedPayload(session, run, turn, modelRequestStartID, usage) {
  const status = String(turn?.status ?? 'completed');
  if (status === 'completed') {
    return {
      session_id: session.session_id,
      run_id: run.run_id,
      vendor_session_id: session.vendor_session_id,
      usage_delta: buildUsageDelta(usage),
      events: [
        buildModelRequestEndEvent(modelRequestStartID, usage, false),
        { type: 'session.status_idle', stop_reason: { type: 'end_turn' } },
      ],
    };
  }
  const message = turn?.error?.message ?? turn?.error?.type ?? `Codex turn ${status}`;
  const errorEvent = sessionErrorEventForError(message, 'Codex run failed');
  return {
    session_id: session.session_id,
    run_id: run.run_id,
    vendor_session_id: session.vendor_session_id,
    usage_delta: buildUsageDelta(usage),
    events: [
      buildModelRequestEndEvent(modelRequestStartID, usage, true),
      errorEvent,
      finalStatusEventForSessionError(errorEvent),
    ],
  };
}

function buildUsageDelta(usage) {
  if (!usage || typeof usage !== 'object') {
    return undefined;
  }
  return {
    input_tokens: numberValue(usage.inputTokens),
    output_tokens: numberValue(usage.outputTokens),
    cache_read_input_tokens: numberValue(usage.cachedInputTokens),
  };
}

function buildModelRequestEndEvent(modelRequestStartID, usage, isError) {
  return {
    type: 'span.model_request_end',
    id: newEventID('span'),
    is_error: isError,
    model_request_start_id: modelRequestStartID,
    model_usage: {
      input_tokens: numberValue(usage?.inputTokens),
      output_tokens: numberValue(usage?.outputTokens),
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: numberValue(usage?.cachedInputTokens),
    },
  };
}

function toolResultContent(content) {
  if (Array.isArray(content)) {
    return content.map((item) => {
      if (item?.type === 'text' && typeof item.text === 'string') {
        return { type: 'text', text: item.text };
      }
      return { type: 'text', text: JSON.stringify(item) };
    });
  }
  return [{ type: 'text', text: String(content ?? '') }];
}

function pendingActionSnapshots(pending) {
  if (pending instanceof Map) {
    return Array.from(pending.values()).map((action) => ({
      id: action.id,
      kind: action.kind,
      tool_use_id: action.tool_use_id,
      custom_tool_use_id: action.custom_tool_use_id,
      name: action.name,
      input: action.input === undefined ? undefined : asStruct(action.input),
      response_kind: action.responseKind ?? action.response_kind,
    }));
  }
  if (!pending || pending.size === 0) {
    return [];
  }
  return Array.from(pending).map((action) => ({
    id: action.id,
    kind: action.kind,
    tool_use_id: action.tool_use_id,
    custom_tool_use_id: action.custom_tool_use_id,
    name: action.name,
    input: action.input === undefined ? undefined : asStruct(action.input),
    response_kind: action.responseKind ?? action.response_kind,
  }));
}

function pendingActionSnapshotMap(pending) {
  const map = new Map();
  for (const action of pending ?? []) {
    if (!action || typeof action !== 'object') {
      continue;
    }
    const id = String(action.id ?? '').trim();
    if (!id) {
      continue;
    }
    map.set(id, {
      id,
      kind: String(action.kind ?? ''),
      tool_use_id: String(action.tool_use_id ?? ''),
      custom_tool_use_id: String(action.custom_tool_use_id ?? ''),
      name: String(action.name ?? ''),
      input: action.input === undefined ? undefined : asStruct(action.input),
      responseKind: String(action.response_kind ?? action.responseKind ?? ''),
    });
  }
  return map;
}

function removePendingAction(pending, actionID) {
  const map = pendingActionSnapshotMap(pending);
  map.delete(String(actionID ?? ''));
  return map;
}

function mergePendingSnapshots(existing, pendingPromises) {
  const map = pendingActionSnapshotMap(existing);
  for (const action of pendingPromises?.values?.() ?? []) {
    map.set(action.id, action);
  }
  return map;
}

function omitActionResolution(resolutions, actionID) {
  const next = { ...(resolutions ?? {}) };
  delete next[String(actionID ?? '')];
  return next;
}

function rememberToolUse(runtime, sessionID, itemID, actionID) {
  if (!itemID) {
    return;
  }
  const sessionMap = runtime.emittedToolUses.get(`${sessionID}:items`) ?? new Map();
  sessionMap.set(String(itemID), String(actionID));
  runtime.emittedToolUses.set(`${sessionID}:items`, sessionMap);
}

function toolUseIDForItem(runtime, sessionID, itemID) {
  return runtime.emittedToolUses.get(`${sessionID}:items`)?.get(String(itemID ?? '')) ?? String(itemID ?? newEventID('toolu'));
}

function asStruct(value) {
  if (value && typeof value === 'object' && !Array.isArray(value)) {
    return value;
  }
  return { value };
}

function normalizeToolName(toolName) {
  return String(toolName ?? '')
    .trim()
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .replace(/[\s-]+/g, '_')
    .toLowerCase();
}

function firstNonEmptyString(...values) {
  for (const value of values) {
    const normalized = String(value ?? '').trim();
    if (normalized) {
      return normalized;
    }
  }
  return '';
}

function numberValue(value) {
  const numeric = Number(value ?? 0);
  return Number.isFinite(numeric) ? numeric : 0;
}

function finiteNumber(value, fallback) {
  const numeric = Number(value ?? fallback);
  return Number.isFinite(numeric) && numeric > 0 ? numeric : fallback;
}

function omitUndefined(value) {
  const out = {};
  for (const [key, item] of Object.entries(value)) {
    if (item !== undefined) {
      out[key] = item;
    }
  }
  return out;
}

function newEventID(prefix) {
  return newID(prefix);
}

function deferredPromise() {
  let resolve;
  let reject;
  const promise = new Promise((promiseResolve, promiseReject) => {
    resolve = promiseResolve;
    reject = promiseReject;
  });
  return { promise, resolve, reject };
}
