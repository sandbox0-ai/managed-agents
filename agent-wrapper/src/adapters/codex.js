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
  providerErrorEventForText,
  runtimeEnvForEngine,
  runtimeModelForSession,
  sessionErrorEventForError,
  sessionErrorEventForMessage,
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

  async prestartSession(session) {
    await this.#clientForSession(session);
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
    let sawCodexEvent = false;
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
            usage = normalizeCodexTokenUsage(message.params?.tokenUsage?.last) ?? usage;
            return;
          }
          if (isCodexEventNotification(message)) {
            sawCodexEvent = true;
            const event = codexEventFromNotification(message);
            usage = codexEventUsage(event) ?? usage;
            const payload = this.#mapCodexEvent(sessionStore.getSession?.() ?? session, run, event);
            if (payload) {
              await callbackClient.send(sessionStore.getSession?.() ?? session, payload);
            }
            const terminalPayload = buildCodexEventTerminalPayload(
              sessionStore.getSession?.() ?? session,
              run,
              event,
              modelRequestStartID,
              usage,
            );
            if (terminalPayload) {
              await callbackClient.send(sessionStore.getSession?.() ?? session, terminalPayload);
              cleanup();
              resolve();
            }
            return;
          }
          const payload = this.#mapNotification(sessionStore.getSession?.() ?? session, run, message);
          if (payload) {
            await callbackClient.send(sessionStore.getSession?.() ?? session, payload);
          }
          if (message.method === 'turn/completed') {
            if (sawCodexEvent) {
              return;
            }
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

  #mapCodexEvent(session, run, event) {
    const text = codexEventAgentText(event);
    if (!text) {
      return null;
    }
    return {
      session_id: session.session_id,
      run_id: run.run_id,
      vendor_session_id: session.vendor_session_id,
      events: [{ type: 'agent.message', content: [{ type: 'text', text }] }],
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

export function codexClientOptions(session) {
  const engine = session.engine ?? {};
  const env = runtimeEnvForEngine(engine);
  if (!env.CODEX_HOME) {
    env.CODEX_HOME = path.join(agentWrapperStateDir(env), 'codex');
  }
  fs.mkdirSync(env.CODEX_HOME, { recursive: true });
  ensureCodexProviderConfig(env, engine);
  return {
    command: firstNonEmptyString(engine.path_to_codex_executable, engine.codex_executable, process.env.CODEX_EXECUTABLE, 'codex'),
    args: Array.isArray(engine.app_server_args) && engine.app_server_args.length > 0
      ? engine.app_server_args.map((value) => String(value))
      : ['app-server'],
    cwd: session.working_directory,
    env,
    requestTimeoutMs: finiteNumber(engine.app_server_request_timeout_ms, 300000),
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
    sandbox: codexSandbox(engine),
    serviceName: includeServiceName ? 'sandbox0_managed_agents' : undefined,
    personality: engine.personality ?? 'none',
    config: codexConfigForSession(session),
  };
  if (codexSupportsDeveloperInstructions(session) && typeof session.agent?.system === 'string' && session.agent.system.trim() !== '') {
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
    sandboxPolicy: codexSandboxPolicy(session.working_directory, engine),
    effort: engine.reasoning_effort,
    summary: engine.reasoning_summary,
    serviceTier: engine.service_tier,
    personality: engine.personality ?? 'none',
  });
}

export function codexConfigForSession(session) {
  const engine = session.engine ?? {};
  const config = engine.config && typeof engine.config === 'object' && !Array.isArray(engine.config)
    ? { ...engine.config }
    : {};
  const provider = codexModelProviderForEngine(engine);
  if (engine.openai_base_url && !config.openai_base_url && (!provider || provider === 'openai')) {
    config.openai_base_url = engine.openai_base_url;
  }
  if (provider && !config.model_provider) {
    config.model_provider = provider;
  }
  return Object.keys(config).length > 0 ? config : undefined;
}

export function codexApprovalPolicy(engine) {
  const requested = engine?.approval_policy;
  if (typeof requested === 'string') {
    switch (requested) {
      case 'unlessTrusted':
      case 'onFailure':
      case 'onRequest':
      case 'never':
        return requested;
      case 'untrusted':
        return 'unlessTrusted';
      case 'on-failure':
        return 'onFailure';
      case 'on-request':
        return 'onRequest';
      default:
        break;
    }
  }
  if (requested && typeof requested === 'object') {
    if (typeof requested.mode === 'string') {
      return codexApprovalPolicy({ approval_policy: requested.mode });
    }
    if (requested.granular || requested.type === 'granular') {
      return 'onRequest';
    }
  }
  return 'onRequest';
}

export function codexSandbox(engine) {
  const requested = engine?.sandbox;
  if (typeof requested === 'string') {
    switch (requested) {
      case 'readOnly':
      case 'workspaceWrite':
      case 'dangerFullAccess':
        return requested;
      case 'read-only':
      case 'read_only':
        return 'readOnly';
      case 'workspace-write':
      case 'workspace_write':
        return 'workspaceWrite';
      case 'danger-full-access':
      case 'danger_full_access':
        return 'dangerFullAccess';
      default:
        break;
    }
  }
  return 'workspaceWrite';
}

export function codexSandboxPolicy(workingDirectory, engine) {
  const requested = engine?.sandbox_policy;
  const networkAccess = sandboxPolicyNetworkAccess(requested, engine);
  const writableRoots = sandboxPolicyWritableRoots(requested, workingDirectory, engine);
  return {
    mode: sandboxPolicyMode(requested),
    writableRoots,
    networkAccess,
    readOnlyAccess: codexReadOnlyAccess(requested?.readOnlyAccess ?? requested?.read_only_access),
  };
}

function sandboxPolicyMode(requested) {
  if (typeof requested === 'string') {
    return codexSandbox({ sandbox: requested });
  }
  if (requested && typeof requested === 'object') {
    if (typeof requested.mode === 'string') {
      return codexSandbox({ sandbox: requested.mode });
    }
    if (typeof requested.type === 'string') {
      return codexSandbox({ sandbox: requested.type });
    }
  }
  return 'workspaceWrite';
}

function sandboxPolicyWritableRoots(requested, workingDirectory, engine) {
  const explicitRoots = Array.isArray(requested?.writableRoots) && requested.writableRoots.length > 0
    ? requested.writableRoots
    : Array.isArray(requested?.writable_roots) && requested.writable_roots.length > 0
      ? requested.writable_roots
      : Array.isArray(engine?.writable_roots) && engine.writable_roots.length > 0
        ? engine.writable_roots
        : [workingDirectory].filter(Boolean);
  return explicitRoots.map((value) => String(value));
}

function sandboxPolicyNetworkAccess(requested, engine) {
  if (typeof requested?.networkAccess === 'boolean') {
    return requested.networkAccess;
  }
  if (typeof requested?.network_access === 'boolean') {
    return requested.network_access;
  }
  return engine.network_access === false ? false : true;
}

function codexReadOnlyAccess(requested) {
  const mode = normalizeReadOnlyAccessMode(requested);
  return { mode };
}

function normalizeReadOnlyAccessMode(requested) {
  const value = typeof requested === 'string'
    ? requested
    : requested && typeof requested === 'object'
      ? requested.mode ?? requested.type
      : undefined;
  switch (value) {
    case 'fullAccess':
    case 'full-access':
    case 'full_access':
      return 'fullAccess';
    default:
      return 'fullAccess';
  }
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
  return [...applyInlineSystemInstructions(items, session), ...skillInputItemsForSession(session)];
}

function applyInlineSystemInstructions(items, session) {
  const systemInstructions = inlineSystemInstructions(session);
  if (!systemInstructions) {
    return items;
  }
  const nextItems = items.map((item) => ({ ...item }));
  const firstTextIndex = nextItems.findIndex((item) => item?.type === 'text');
  if (firstTextIndex === -1) {
    nextItems.unshift({ type: 'text', text: formatInlineSystemInstructions(systemInstructions) });
    return nextItems;
  }
  nextItems[firstTextIndex] = {
    ...nextItems[firstTextIndex],
    text: formatInlineSystemInstructions(systemInstructions, nextItems[firstTextIndex].text),
  };
  return nextItems;
}

function inlineSystemInstructions(session) {
  if (codexSupportsDeveloperInstructions(session)) {
    return '';
  }
  return typeof session?.agent?.system === 'string' ? session.agent.system.trim() : '';
}

function formatInlineSystemInstructions(systemInstructions, userInput = '') {
  const normalizedInput = String(userInput ?? '').trim();
  if (!normalizedInput) {
    return `System instructions:\n${systemInstructions}`;
  }
  return `System instructions:\n${systemInstructions}\n\nUser input:\n${normalizedInput}`;
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

function buildCodexEventTerminalPayload(session, run, event, modelRequestStartID, usage) {
  switch (event?.type) {
    case 'task_complete': {
      const completedUsage = codexEventUsage(event) ?? usage;
      const agentMessage = codexEventAgentText(event);
      return {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: session.vendor_session_id,
        usage_delta: buildUsageDelta(completedUsage),
        events: [
          ...(agentMessage ? [{ type: 'agent.message', content: [{ type: 'text', text: agentMessage }] }] : []),
          buildModelRequestEndEvent(modelRequestStartID, completedUsage, false),
          { type: 'session.status_idle', stop_reason: { type: 'end_turn' } },
        ],
      };
    }
    case 'error':
    case 'turn_aborted': {
      const message = codexEventErrorText(event) || `Codex ${String(event.type).replace(/_/g, ' ')}`;
      const errorEvent = providerErrorEventForText(message) ?? sessionErrorEventForMessage(message, 'Codex run failed');
      return {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: session.vendor_session_id,
        usage_delta: buildUsageDelta(codexEventUsage(event) ?? usage),
        events: [
          buildModelRequestEndEvent(modelRequestStartID, codexEventUsage(event) ?? usage, true),
          errorEvent,
          finalStatusEventForSessionError(errorEvent),
        ],
      };
    }
    default:
      return null;
  }
}

function buildUsageDelta(usage) {
  const normalized = normalizeCodexTokenUsage(usage);
  if (!normalized) {
    return undefined;
  }
  return {
    input_tokens: numberValue(normalized.inputTokens),
    output_tokens: numberValue(normalized.outputTokens),
    cache_read_input_tokens: numberValue(normalized.cachedInputTokens),
  };
}

function buildModelRequestEndEvent(modelRequestStartID, usage, isError) {
  const normalized = normalizeCodexTokenUsage(usage);
  return {
    type: 'span.model_request_end',
    id: newEventID('span'),
    is_error: isError,
    model_request_start_id: modelRequestStartID,
    model_usage: {
      input_tokens: numberValue(normalized?.inputTokens),
      output_tokens: numberValue(normalized?.outputTokens),
      cache_creation_input_tokens: 0,
      cache_read_input_tokens: numberValue(normalized?.cachedInputTokens),
    },
  };
}

function isCodexEventNotification(message) {
  return typeof message?.method === 'string' && message.method.startsWith('codex/event/');
}

function codexEventFromNotification(message) {
  const typeFromMethod = String(message?.method ?? '').replace(/^codex\/event\//, '');
  const msg = message?.params?.msg;
  if (msg && typeof msg === 'object') {
    return { ...msg, type: String(msg.type ?? typeFromMethod) };
  }
  return { type: typeFromMethod };
}

function codexEventUsage(event) {
  return normalizeCodexTokenUsage(
    event?.total_token_usage
    ?? event?.totalTokenUsage
    ?? event?.token_usage
    ?? event?.tokenUsage
    ?? event?.usage,
  );
}

function codexEventErrorText(event) {
  const message = firstNonEmptyString(
    event?.message,
    event?.error?.message,
    event?.cause?.message,
  );
  if (message) {
    return message;
  }
  if (event?.error && typeof event.error === 'object') {
    return JSON.stringify(event.error);
  }
  return '';
}

function normalizeCodexTokenUsage(usage) {
  if (!usage || typeof usage !== 'object') {
    return null;
  }
  return {
    inputTokens: numberValue(usage.inputTokens ?? usage.input_tokens),
    outputTokens: numberValue(usage.outputTokens ?? usage.output_tokens),
    cachedInputTokens: numberValue(usage.cachedInputTokens ?? usage.cached_input_tokens),
  };
}

function codexEventAgentText(event) {
  if (!event || typeof event !== 'object') {
    return '';
  }
  switch (event.type) {
    case 'agent_message':
      return codexEventText(event.message ?? event.content ?? event);
    case 'task_complete':
      return codexEventText(
        codexEventField(event, 'last_agent_message')
        ?? codexEventField(event, 'agent_message')
        ?? codexEventField(event, 'message')
        ?? codexEventField(event, 'content'),
      );
    case 'item_completed':
    case 'raw_response_item':
      return codexEventText(codexAgentMessageValue(event.item ?? event.response_item ?? event.raw_response_item));
    default:
      return '';
  }
}

function codexEventText(value) {
  if (typeof value === 'string') {
    return value;
  }
  if (Array.isArray(value)) {
    const lines = value.map((item) => codexEventText(item)).filter(Boolean);
    return lines.join('\n');
  }
  if (!value || typeof value !== 'object') {
    return '';
  }
  for (const key of ['text', 'message', 'content', 'delta', 'last_agent_message', 'agent_message', 'assistant_message', 'output_text', 'input_text']) {
    const field = codexEventField(value, key);
    if (field !== undefined) {
      const text = codexEventText(field);
      if (text) {
        return text;
      }
    }
  }
  const entries = Object.entries(value);
  if (entries.length === 1) {
    return codexEventText(entries[0][1]);
  }
  return '';
}

function codexAgentMessageValue(value) {
  if (!value || typeof value !== 'object') {
    return null;
  }
  const type = normalizeCodexEventKey(codexEventField(value, 'type'));
  const role = normalizeCodexEventKey(codexEventField(value, 'role'));
  if (type === 'agentmessage' || type === 'assistantmessage' || role === 'assistant') {
    return value;
  }
  for (const key of ['agent_message', 'assistant_message']) {
    const field = codexEventField(value, key);
    if (field !== undefined) {
      return field;
    }
  }
  const entries = Object.entries(value);
  if (entries.length === 1) {
    return entries[0][1];
  }
  return null;
}

function codexEventField(value, key) {
  if (!value || typeof value !== 'object') {
    return undefined;
  }
  const target = normalizeCodexEventKey(key);
  for (const [entryKey, entryValue] of Object.entries(value)) {
    if (normalizeCodexEventKey(entryKey) === target) {
      return entryValue;
    }
  }
  return undefined;
}

function normalizeCodexEventKey(value) {
  return String(value ?? '')
    .trim()
    .toLowerCase()
    .replace(/[^a-z0-9]/g, '');
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

export function codexModelProviderForEngine(engine = {}) {
  const explicitProvider = firstNonEmptyString(engine.model_provider);
  if (explicitProvider) {
    return explicitProvider;
  }
  const openAIBaseURL = firstNonEmptyString(engine.openai_base_url);
  if (isMiniMaxBaseURL(openAIBaseURL)) {
    return 'minimax';
  }
  return '';
}

function codexSupportsDeveloperInstructions(session) {
  return codexModelProviderForEngine(session?.engine ?? {}) !== 'minimax';
}

function ensureCodexProviderConfig(env, engine) {
  const provider = codexModelProviderForEngine(engine);
  if (!provider || provider === 'openai') {
    return;
  }
  if (provider === 'minimax') {
    if (!env.MINIMAX_API_KEY && env.MINIMAX_TOKEN) {
      env.MINIMAX_API_KEY = env.MINIMAX_TOKEN;
    }
    delete env.OPENAI_API_KEY;
    delete env.OPENAI_BASE_URL;
    delete env.CODEX_API_KEY;
  }
  const configPath = path.join(env.CODEX_HOME, 'config.toml');
  fs.writeFileSync(configPath, codexProviderConfigToml(provider, engine, env), 'utf8');
}

function codexProviderConfigToml(provider, engine, env) {
  switch (provider) {
    case 'minimax':
      return [
        `[model_providers.${provider}]`,
        `name = ${tomlString('MiniMax Chat Completions API')}`,
        `base_url = ${tomlString(firstNonEmptyString(engine.openai_base_url, env.CODEX_PROVIDER_BASE_URL, 'https://api.minimax.io/v1'))}`,
        `env_key = ${tomlString(firstNonEmptyString(env.CODEX_PROVIDER_ENV_KEY, 'MINIMAX_API_KEY'))}`,
        `wire_api = ${tomlString(firstNonEmptyString(env.CODEX_PROVIDER_WIRE_API, 'chat'))}`,
        `requires_openai_auth = ${tomlBoolean(firstNonEmptyString(env.CODEX_PROVIDER_REQUIRES_OPENAI_AUTH, 'false'))}`,
        `request_max_retries = ${tomlInteger(env.CODEX_PROVIDER_REQUEST_MAX_RETRIES, 4)}`,
        `stream_max_retries = ${tomlInteger(env.CODEX_PROVIDER_STREAM_MAX_RETRIES, 10)}`,
        `stream_idle_timeout_ms = ${tomlInteger(env.CODEX_PROVIDER_STREAM_IDLE_TIMEOUT_MS, 300000)}`,
        '',
      ].join('\n');
    default:
      return '';
  }
}

function tomlString(value) {
  return JSON.stringify(String(value ?? ''));
}

function tomlBoolean(value) {
  return /^true$/i.test(String(value ?? '').trim()) ? 'true' : 'false';
}

function tomlInteger(value, fallback) {
  const numeric = Number.parseInt(String(value ?? ''), 10);
  return Number.isFinite(numeric) ? String(numeric) : String(fallback);
}

function isMiniMaxBaseURL(value) {
  const baseURL = String(value ?? '').trim();
  if (!baseURL) {
    return false;
  }
  try {
    const parsedURL = new URL(baseURL);
    return parsedURL.hostname === 'api.minimax.io' || parsedURL.hostname === 'api.minimaxi.com';
  } catch {
    return false;
  }
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
