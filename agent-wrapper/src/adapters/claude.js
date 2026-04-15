import { createSdkMcpServer, query } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { inputEventsToPrompt, inputEventsToSDKMessages } from '../lib/prompt.js';
import {
  AgentRuntime,
  finalStatusEventForSessionError,
  providerErrorEventForText,
  runtimeEnvForEngine,
  runtimeModelForSession,
  sessionErrorEventForError,
} from './runtime.js';

async function* emptyPromptStream() {}

export {
  finalStatusEventForSessionError,
  providerErrorEventForText,
  runtimeEnvForEngine,
  runtimeModelForSession,
  sessionErrorEventForError,
};

export function querySkillNames(session) {
  if (!Array.isArray(session?.skill_names)) {
    return undefined;
  }
  const names = session.skill_names
    .map((value) => String(value ?? '').trim())
    .filter((value) => value.length > 0);
  return names.length > 0 ? names : undefined;
}

export function allowToolUseDecision(input, toolUseID) {
  const decision = {
    behavior: 'allow',
    updatedInput: recordInput(input),
  };
  const normalizedToolUseID = String(toolUseID ?? '').trim();
  if (normalizedToolUseID !== '') {
    decision.toolUseID = normalizedToolUseID;
  }
  return decision;
}

export class ClaudeRuntime extends AgentRuntime {
  constructor() {
    super('claude');
    this.activeRuns = new Map();
    this.pendingActions = new Map();
    this.emittedToolUses = new Map();
    this.deferredRunErrors = new Map();
  }

  async startRun(session, run, callbackClient, sessionStore) {
    const prompt = buildPromptInput(run.input_events);
    const options = this.#buildOptions(session, run, callbackClient, sessionStore);
    const modelRequestStartID = newEventID('span');
    if (session.vendor_session_id) {
      options.resume = session.vendor_session_id;
    }
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

    await callbackClient.send(currentSession, {
      session_id: currentSession.session_id,
      run_id: run.run_id,
      vendor_session_id: currentSession.vendor_session_id,
      events: [{ type: 'span.model_request_start', id: modelRequestStartID }],
    });

    const stream = query({ prompt, options });
    this.activeRuns.set(run.run_id, { stream, sessionID: session.session_id });
    try {
      for await (const message of stream) {
        if (!currentSession.vendor_session_id && message?.session_id) {
          currentSession = sessionStore.persistSession((current) => ({
            ...current,
            vendor_session_id: message.session_id,
          }));
        }
        const callbackPayload = this.#mapMessage(currentSession, run, message, modelRequestStartID);
        if (callbackPayload) {
          await callbackClient.send(currentSession, callbackPayload);
        }
      }
    } catch (error) {
      const latestSession = sessionStore.getSession?.() ?? currentSession;
      await callbackClient.send(latestSession, {
        session_id: latestSession.session_id,
        run_id: run.run_id,
        vendor_session_id: latestSession.vendor_session_id,
        events: [buildModelRequestEndEvent(modelRequestStartID, null, true)],
      }).catch(() => {});
      throw error;
    } finally {
      this.pendingActions.delete(session.session_id);
      this.emittedToolUses.delete(session.session_id);
      this.deferredRunErrors.delete(run.run_id);
      this.activeRuns.delete(run.run_id);
    }
  }

  async interruptRun(runID) {
    const active = this.activeRuns.get(runID);
    if (!active) {
      return false;
    }
    await active.stream.interrupt();
    active.stream.close();
    return true;
  }

  deleteSession(sessionID) {
    this.pendingActions.delete(sessionID);
    this.emittedToolUses.delete(sessionID);
  }

  resolveActions(sessionID, events, sessionStore) {
    const currentSession = sessionStore.getSession?.();
    const pendingSnapshots = pendingActionSnapshotMap(currentSession?.pending_actions);
    const pendingPromises = this.pendingActions.get(sessionID);
    const hasVendorSession = typeof currentSession?.vendor_session_id === 'string' && currentSession.vendor_session_id.trim() !== '';
    const hasToolConfirmationInput = (events ?? []).some((event) => event?.type === 'user.tool_confirmation');
    if (pendingSnapshots.size === 0 && (!pendingPromises || pendingPromises.size === 0) && !(hasVendorSession && hasToolConfirmationInput)) {
      console.log(JSON.stringify({
        level: 'warn',
        msg: 'claude resolve actions without pending state',
        session_id: sessionID,
        vendor_session_id: currentSession?.vendor_session_id ?? null,
        persisted_pending_actions: currentSession?.pending_actions ?? null,
        in_memory_pending_count: pendingPromises?.size ?? 0,
        input_event_types: (events ?? []).map((event) => event?.type ?? null),
      }));
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
        const decision = this.#permissionDecision(event, action?.input);
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
        pendingPromises.delete(action.id);
        pendingSnapshots.delete(action.id);
        resolved += 1;
        action.resolve(customToolResultToCallToolResult(event));
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
    console.log(JSON.stringify({
      level: 'info',
      msg: 'claude resolve actions',
      session_id: sessionID,
      vendor_session_id: currentSession?.vendor_session_id ?? null,
      resolved_count: response.resolved_count,
      remaining_action_ids: response.remaining_action_ids,
      resume_required: response.resume_required,
      input_event_types: (events ?? []).map((event) => event?.type ?? null),
    }));
    return response;
  }

  #buildOptions(session, run, callbackClient, sessionStore) {
    const engine = session.engine ?? {};
    const toolPlan = buildToolPlan(session.agent?.tools);
    const customTools = buildCustomToolAdapters(
      session.agent?.tools,
      async (tool, args) => this.#invokeCustomTool(session, run, callbackClient, sessionStore, tool, args),
    );
    const options = {
      cwd: session.working_directory,
      model: runtimeModelForSession(session),
      permissionMode: engine.permission_mode ?? 'default',
      allowDangerouslySkipPermissions: false,
      systemPrompt: typeof session.agent?.system === 'string' ? session.agent.system : undefined,
      pathToClaudeCodeExecutable: engine.path_to_claude_code_executable,
      maxTurns: engine.max_turns,
      skills: querySkillNames(session),
      mcpServers: mergeMcpServers(mcpServersFromAgent(session.agent?.mcp_servers), engine.mcp_servers, customTools.mcpServers),
      tools: toolPlan.builtinSDKTools,
      settings: engine.settings,
      extraArgs: engine.extra_args,
      env: runtimeEnvForEngine(engine),
      persistSession: true,
      canUseTool: async (toolName, input, options) => this.#handlePermissionRequest(
        session,
        run,
        callbackClient,
        sessionStore,
        toolPlan,
        customTools,
        toolName,
        input,
        options,
      ),
      hooks: {
        PreToolUse: [{
          hooks: [async (input) => {
            const eventID = String(input.tool_use_id ?? '');
            if (isCustomToolInput(customTools, input)) {
              return { continue: true };
            }
            const currentSession = sessionStore.getSession() ?? session;
            const resolvedTool = resolveToolPolicy(toolPlan, input);
            const evaluatedPermission = resolvedTool.enabled
              ? (resolvedTool.policy === 'always_allow' ? 'allow' : 'ask')
              : 'deny';
            const askForConfirmation = evaluatedPermission === 'ask';
            const toolUseEvent = buildToolUseEvent(input, eventID, evaluatedPermission);
            const resolution = currentSession?.tool_confirmation_resolutions?.[eventID]?.result;
            if (resolution) {
              sessionStore.persistSession((latest) => ({
                ...latest,
                pending_actions: pendingActionSnapshots(removePendingAction(latest?.pending_actions, eventID)),
                tool_confirmation_resolutions: omitActionResolution(latest?.tool_confirmation_resolutions, eventID),
                updated_at: new Date().toISOString(),
              }));
              return {
                continue: true,
                hookSpecificOutput: {
                  hookEventName: 'PreToolUse',
                  permissionDecision: resolution.behavior === 'allow' ? 'allow' : 'deny',
                  permissionDecisionReason: resolution.behavior === 'allow'
                    ? 'Approved by user'
                    : (resolution.message ?? 'Denied by user'),
                },
              };
            }
            if (!this.#wasToolUseEmitted(currentSession.session_id, eventID)) {
              this.#markToolUseEmitted(currentSession.session_id, eventID);
              if (askForConfirmation) {
                sessionStore.persistSession((latest) => ({
                  ...latest,
                  pending_actions: pendingActionSnapshots(addPendingAction(latest?.pending_actions, {
                    id: eventID,
                    kind: 'tool_confirmation',
                    tool_use_id: eventID,
                  })),
                  updated_at: new Date().toISOString(),
                }));
              }
              await callbackClient.send(currentSession, {
                session_id: currentSession.session_id,
                run_id: run.run_id,
                vendor_session_id: currentSession.vendor_session_id,
                events: askForConfirmation
                  ? [toolUseEvent, {
                    type: 'session.status_idle',
                    stop_reason: {
                      type: 'requires_action',
                      event_ids: [eventID],
                    },
                  }]
                  : [toolUseEvent],
              });
            }
            if (!resolvedTool.enabled) {
              return {
                continue: true,
                hookSpecificOutput: {
                  hookEventName: 'PreToolUse',
                  permissionDecision: 'deny',
                  permissionDecisionReason: 'Disabled by managed-agent tool policy',
                },
              };
            }
            if (!askForConfirmation) {
              return {
                continue: true,
                hookSpecificOutput: {
                  hookEventName: 'PreToolUse',
                  permissionDecision: 'allow',
                  permissionDecisionReason: 'Auto-allowed by managed-agent policy',
                },
              };
            }
            return {
              continue: true,
              hookSpecificOutput: {
                hookEventName: 'PreToolUse',
                permissionDecision: 'defer',
                permissionDecisionReason: 'External confirmation required',
              },
            };
          }],
        }],
        PostToolUse: [{
          hooks: [async (input) => {
            if (isCustomToolInput(customTools, input)) {
              return { continue: true };
            }
            const currentSession = sessionStore.getSession() ?? session;
            await callbackClient.send(currentSession, {
              session_id: currentSession.session_id,
              run_id: run.run_id,
              vendor_session_id: currentSession.vendor_session_id,
              events: [buildToolResultEvent(input, toolResultContent(input.tool_response), false)],
            });
            return { continue: true };
          }],
        }],
        PostToolUseFailure: [{
          hooks: [async (input) => {
            if (isCustomToolInput(customTools, input)) {
              return { continue: true };
            }
            const currentSession = sessionStore.getSession() ?? session;
            await callbackClient.send(currentSession, {
              session_id: currentSession.session_id,
              run_id: run.run_id,
              vendor_session_id: currentSession.vendor_session_id,
              events: [buildToolResultEvent(input, toolResultContent(input.error ?? 'tool execution failed'), true)],
            });
            return { continue: true };
          }],
        }],
        PostCompact: [{
          hooks: [async () => {
            const currentSession = sessionStore.getSession() ?? session;
            await callbackClient.send(currentSession, {
              session_id: currentSession.session_id,
              run_id: run.run_id,
              vendor_session_id: currentSession.vendor_session_id,
              events: [{ type: 'agent.thread_context_compacted' }],
            });
            return { continue: true };
          }],
        }],
      },
    };
    if (toolPlan.disallowedTools.length > 0) {
      options.disallowedTools = toolPlan.disallowedTools;
    }
    return options;
  }

  async #handlePermissionRequest(session, run, callbackClient, sessionStore, toolPlan, customTools, toolName, input, options) {
    const eventID = firstNonEmptyString(options?.toolUseID, newEventID('toolu'));
    const hookInput = buildPermissionHookInput(toolName, input, eventID);
    if (isCustomToolInput(customTools, hookInput)) {
      return allowToolUseDecision(input, eventID);
    }

    const currentSession = sessionStore.getSession() ?? session;
    const resolvedTool = resolveToolPolicy(toolPlan, hookInput);
    const evaluatedPermission = resolvedTool.enabled
      ? (resolvedTool.policy === 'always_allow' ? 'allow' : 'ask')
      : 'deny';
    const askForConfirmation = evaluatedPermission === 'ask';
    const existingResolution = currentSession?.tool_confirmation_resolutions?.[eventID]?.result;
    if (existingResolution) {
      sessionStore.persistSession((latest) => ({
        ...latest,
        pending_actions: pendingActionSnapshots(removePendingAction(latest?.pending_actions, eventID)),
        tool_confirmation_resolutions: omitActionResolution(latest?.tool_confirmation_resolutions, eventID),
        updated_at: new Date().toISOString(),
      }));
      return sdkPermissionDecision(existingResolution, input, eventID);
    }

    let pending = null;
    if (askForConfirmation) {
      pending = this.#registerPendingAction(currentSession, {
        id: eventID,
        kind: 'tool_confirmation',
        tool_use_id: eventID,
        name: resolvedTool.name,
        input: recordInput(input),
      }, sessionStore);
    }

    if (!this.#wasToolUseEmitted(currentSession.session_id, eventID)) {
      this.#markToolUseEmitted(currentSession.session_id, eventID);
      const latestSession = sessionStore.getSession() ?? currentSession;
      await callbackClient.send(latestSession, {
        session_id: latestSession.session_id,
        run_id: run.run_id,
        vendor_session_id: latestSession.vendor_session_id,
        events: askForConfirmation
          ? [buildToolUseEvent(hookInput, eventID, evaluatedPermission), {
            type: 'session.status_idle',
            stop_reason: {
              type: 'requires_action',
              event_ids: [eventID],
            },
          }]
          : [buildToolUseEvent(hookInput, eventID, evaluatedPermission)],
      });
    }

    if (!resolvedTool.enabled) {
      return {
        behavior: 'deny',
        message: 'Disabled by managed-agent tool policy',
        toolUseID: eventID,
        interrupt: true,
      };
    }
    if (!askForConfirmation) {
      return allowToolUseDecision(input, eventID);
    }
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

  async #invokeCustomTool(session, run, callbackClient, sessionStore, tool, args) {
    const currentSession = sessionStore.getSession() ?? session;
    const actionID = newCustomToolUseID();
    const pending = this.#registerPendingAction(currentSession, {
      id: actionID,
      kind: 'custom_tool_result',
      custom_tool_use_id: actionID,
      name: tool.name,
    }, sessionStore);
    await callbackClient.send(currentSession, {
      session_id: currentSession.session_id,
      run_id: run.run_id,
      vendor_session_id: currentSession.vendor_session_id,
      events: [
        {
          type: 'agent.custom_tool_use',
          id: actionID,
          name: tool.name,
          input: asStruct(args),
        },
        {
          type: 'session.status_idle',
          stop_reason: {
            type: 'requires_action',
            event_ids: [actionID],
          },
        },
      ],
    });
    return pending.promise;
  }

  #permissionDecision(event, input) {
    if (event.result === 'allow') {
      return allowToolUseDecision(input, event.tool_use_id);
    }
    return {
      behavior: 'deny',
      toolUseID: String(event.tool_use_id ?? ''),
      message: typeof event.deny_message === 'string' && event.deny_message.trim()
        ? event.deny_message.trim()
        : 'Denied by user',
      interrupt: true,
    };
  }

  #wasToolUseEmitted(sessionID, eventID) {
    if (!eventID) {
      return false;
    }
    return this.emittedToolUses.get(sessionID)?.has(eventID) ?? false;
  }

  #markToolUseEmitted(sessionID, eventID) {
    if (!eventID) {
      return;
    }
    const seen = this.emittedToolUses.get(sessionID) ?? new Set();
    seen.add(eventID);
    this.emittedToolUses.set(sessionID, seen);
  }

  #mapMessage(session, run, message, modelRequestStartID) {
    if (!message || typeof message !== 'object') {
      return null;
    }
    if (message.type === 'assistant' && Array.isArray(message.message?.content)) {
      const events = [];
      for (const block of message.message.content) {
        if (block?.type === 'text' && typeof block.text === 'string') {
          const errorEvent = providerErrorEventForText(block.text);
          if (errorEvent) {
            this.deferredRunErrors.set(run.run_id, errorEvent);
            events.push(errorEvent);
            continue;
          }
          events.push({
            type: 'agent.message',
            content: [{ type: 'text', text: block.text }],
          });
        } else if (block?.type === 'thinking') {
          events.push({
            type: 'agent.thinking',
            id: String(block.id ?? ''),
          });
        }
      }
      if (events.length === 0) {
        return null;
      }
      return {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: message.session_id,
        events,
      };
    }

    if (message.type === 'user' && Array.isArray(message.message?.content)) {
      const events = [];
      for (const block of message.message.content) {
        if (block?.type !== 'tool_result') {
          continue;
        }
        events.push({
          type: 'agent.tool_result',
          tool_use_id: String(block.tool_use_id ?? message.parent_tool_use_id ?? ''),
          content: toolResultContent(block.content),
          is_error: block.is_error === true,
        });
      }
      if (events.length === 0) {
        return null;
      }
      return {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: message.session_id,
        events,
      };
    }

    if (message.type === 'result') {
      const usageDelta = buildUsageDelta(message.usage);
      const modelRequestEnd = buildModelRequestEndEvent(modelRequestStartID, message.usage, message.subtype !== 'success');
      if (message.subtype === 'success') {
        const deferredError = this.deferredRunErrors.get(run.run_id);
        if (deferredError) {
          this.deferredRunErrors.delete(run.run_id);
          return {
            session_id: session.session_id,
            run_id: run.run_id,
            vendor_session_id: message.session_id,
            usage_delta: usageDelta,
            events: [
              buildModelRequestEndEvent(modelRequestStartID, message.usage, true),
              finalStatusEventForSessionError(deferredError),
            ],
          };
        }
        if (message.stop_reason === 'tool_deferred') {
          return {
            session_id: session.session_id,
            run_id: run.run_id,
            vendor_session_id: message.session_id,
            usage_delta: usageDelta,
            events: [modelRequestEnd],
          };
        }
        return {
          session_id: session.session_id,
          run_id: run.run_id,
          vendor_session_id: message.session_id,
          usage_delta: usageDelta,
          events: [
            modelRequestEnd,
            {
              type: 'session.status_idle',
              stop_reason: { type: 'end_turn' },
            },
          ],
        };
      }
      const errorEvent = sessionErrorEventForError((message.errors ?? []).join('; ') || message.subtype || 'Claude run failed');
      return {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: message.session_id,
        usage_delta: usageDelta,
        events: [
          modelRequestEnd,
          errorEvent,
          finalStatusEventForSessionError(errorEvent),
        ],
      };
    }

    return null;
  }
}

function buildPromptInput(events) {
  const filteredEvents = filteredPromptEvents(events);
  const structuredPrompt = inputEventsToSDKMessages(filteredEvents);
  if (structuredPrompt) {
    return structuredPrompt;
  }
  const prompt = inputEventsToPrompt(filteredEvents);
  if (prompt) {
    return prompt;
  }
  return emptyPromptStream();
}

function filteredPromptEvents(events) {
  return (events ?? []).filter((event) => event?.type !== 'user.tool_confirmation');
}

function normalizeToolName(toolName) {
  return String(toolName ?? '')
    .trim()
    .replace(/([a-z0-9])([A-Z])/g, '$1_$2')
    .replace(/[\s-]+/g, '_')
    .toLowerCase();
}

function buildToolUseEvent(input, eventID, evaluatedPermission) {
  const mcp = mcpToolMetadata(input);
  if (mcp) {
    return {
      type: 'agent.mcp_tool_use',
      id: eventID,
      name: mcp.name,
      mcp_server_name: mcp.serverName,
      input: asStruct(input.tool_input),
      evaluated_permission: evaluatedPermission,
    };
  }
  return {
    type: 'agent.tool_use',
    id: eventID,
    name: normalizeToolName(input.tool_name),
    input: asStruct(input.tool_input),
    evaluated_permission: evaluatedPermission,
  };
}

function buildPermissionHookInput(toolName, input, eventID) {
  return {
    hook_event_name: 'PermissionRequest',
    tool_name: String(toolName ?? ''),
    tool_input: recordInput(input),
    tool_use_id: eventID,
  };
}

function sdkPermissionDecision(decision, input, eventID) {
  if (decision?.behavior === 'allow') {
    return allowToolUseDecision(decision.updatedInput ?? input, decision.toolUseID ?? eventID);
  }
  return {
    behavior: 'deny',
    message: typeof decision?.message === 'string' && decision.message.trim() ? decision.message.trim() : 'Denied by user',
    toolUseID: String(decision?.toolUseID ?? eventID ?? ''),
    interrupt: decision?.interrupt !== false,
  };
}

function buildToolResultEvent(input, content, isError) {
  const mcp = mcpToolMetadata(input);
  if (mcp) {
    return {
      type: 'agent.mcp_tool_result',
      id: newEventID('evt'),
      mcp_tool_use_id: String(input.tool_use_id ?? ''),
      content,
      is_error: isError || undefined,
    };
  }
  return {
    type: 'agent.tool_result',
    tool_use_id: String(input.tool_use_id ?? ''),
    content,
    is_error: isError || undefined,
  };
}

function mcpToolMetadata(input) {
  const rawToolName = String(input?.tool_name ?? '').trim();
  const parsed = parseMcpToolName(rawToolName);
  if (parsed) {
    return parsed;
  }
  const serverName = firstNonEmptyString(
    input?.mcp_server_name,
    input?.mcpServerName,
    input?.server_name,
    input?.serverName,
  );
  if (!serverName) {
    return null;
  }
  return {
    name: rawToolName,
    serverName,
  };
}

function parseMcpToolName(toolName) {
  const normalized = String(toolName ?? '').trim();
  if (!normalized.toLowerCase().startsWith('mcp__')) {
    return null;
  }
  const parts = normalized.split('__');
  if (parts.length < 3) {
    return null;
  }
  return {
    serverName: parts[1],
    name: parts.slice(2).join('__'),
  };
}

function buildUsageDelta(usage) {
  if (!usage || typeof usage !== 'object') {
    return undefined;
  }
  const cacheCreation = normalizeCacheCreationUsage(usage);
  const delta = {
    input_tokens: numberValue(usage.input_tokens),
    output_tokens: numberValue(usage.output_tokens),
    cache_read_input_tokens: numberValue(usage.cache_read_input_tokens),
  };
  if (cacheCreation) {
    delta.cache_creation = cacheCreation;
  }
  return delta;
}

function buildModelRequestEndEvent(modelRequestStartID, usage, isError) {
  return {
    type: 'span.model_request_end',
    id: newEventID('span'),
    is_error: isError,
    model_request_start_id: modelRequestStartID,
    model_usage: buildModelUsage(usage),
  };
}

function buildModelUsage(usage) {
  const cacheCreation = normalizeCacheCreationUsage(usage);
  const value = {
    input_tokens: numberValue(usage?.input_tokens),
    output_tokens: numberValue(usage?.output_tokens),
    cache_creation_input_tokens: sumCacheCreationTokens(cacheCreation, usage?.cache_creation_input_tokens),
    cache_read_input_tokens: numberValue(usage?.cache_read_input_tokens),
  };
  const speed = firstNonEmptyString(usage?.speed);
  if (speed) {
    value.speed = speed;
  }
  return value;
}

function normalizeCacheCreationUsage(usage) {
  const ephemeral1h = numberValue(usage?.cache_creation?.ephemeral_1h_input_tokens);
  let ephemeral5m = numberValue(usage?.cache_creation?.ephemeral_5m_input_tokens);
  if (ephemeral1h === 0 && ephemeral5m === 0) {
    ephemeral5m = numberValue(usage?.cache_creation_input_tokens);
  }
  if (ephemeral1h === 0 && ephemeral5m === 0) {
    return undefined;
  }
  return {
    ...(ephemeral1h > 0 ? { ephemeral_1h_input_tokens: ephemeral1h } : {}),
    ...(ephemeral5m > 0 ? { ephemeral_5m_input_tokens: ephemeral5m } : {}),
  };
}

function sumCacheCreationTokens(cacheCreation, fallback) {
  if (cacheCreation) {
    return numberValue(cacheCreation.ephemeral_1h_input_tokens) + numberValue(cacheCreation.ephemeral_5m_input_tokens);
  }
  return numberValue(fallback);
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

const BUILTIN_AGENT_TOOLS = [
  ['bash', 'Bash'],
  ['edit', 'Edit'],
  ['read', 'Read'],
  ['write', 'Write'],
  ['glob', 'Glob'],
  ['grep', 'Grep'],
  ['web_fetch', 'WebFetch'],
  ['web_search', 'WebSearch'],
];

const BUILTIN_AGENT_TOOL_BY_CONTRACT_NAME = new Map(BUILTIN_AGENT_TOOLS);
const BUILTIN_AGENT_TOOL_CONTRACT_NAMES = new Set(BUILTIN_AGENT_TOOL_BY_CONTRACT_NAME.keys());

export function buildToolPlan(tools) {
  const builtinPolicies = new Map();
  const mcpDefaults = new Map();
  const mcpPolicies = new Map();
  const disallowedTools = [];
  let hasAgentToolset = false;

  for (const tool of tools ?? []) {
    if (!tool || typeof tool !== 'object') {
      continue;
    }

    if (tool.type === 'agent_toolset_20260401') {
      hasAgentToolset = true;
      const defaultConfig = defaultToolConfig(tool.default_config, 'always_allow');
      for (const [contractName, sdkName] of BUILTIN_AGENT_TOOLS) {
        builtinPolicies.set(contractName, {
          enabled: defaultConfig.enabled,
          policy: defaultConfig.policy,
          sdkName,
        });
      }
      for (const config of tool.configs ?? []) {
        const name = normalizeToolName(config?.name);
        if (!BUILTIN_AGENT_TOOL_CONTRACT_NAMES.has(name)) {
          continue;
        }
        const current = builtinPolicies.get(name) ?? {
          enabled: defaultConfig.enabled,
          policy: defaultConfig.policy,
          sdkName: BUILTIN_AGENT_TOOL_BY_CONTRACT_NAME.get(name),
        };
        builtinPolicies.set(name, {
          enabled: booleanValue(config?.enabled, current.enabled),
          policy: permissionPolicyType(config?.permission_policy, current.policy),
          sdkName: current.sdkName,
        });
      }
      continue;
    }

    if (tool.type === 'mcp_toolset') {
      const serverName = String(tool.mcp_server_name ?? '').trim();
      if (!serverName) {
        continue;
      }
      const defaultConfig = defaultToolConfig(tool.default_config, 'always_ask');
      mcpDefaults.set(normalizeToolName(serverName), {
        serverName,
        enabled: defaultConfig.enabled,
        policy: defaultConfig.policy,
      });
      for (const config of tool.configs ?? []) {
        const name = String(config?.name ?? '').trim();
        const normalizedName = normalizeToolName(name);
        if (!normalizedName) {
          continue;
        }
        const resolved = {
          serverName,
          name,
          enabled: booleanValue(config?.enabled, defaultConfig.enabled),
          policy: permissionPolicyType(config?.permission_policy, defaultConfig.policy),
        };
        mcpPolicies.set(mcpPolicyKey(serverName, name), resolved);
        if (!resolved.enabled) {
          disallowedTools.push(mcpSDKToolName(serverName, name));
        }
      }
    }
  }

  return {
    builtinSDKTools: hasAgentToolset
      ? Array.from(builtinPolicies.values())
        .filter((entry) => entry.enabled)
        .map((entry) => entry.sdkName)
      : [],
    disallowedTools: compactStrings(disallowedTools),
    builtinPolicies,
    mcpDefaults,
    mcpPolicies,
  };
}

export function resolveToolPolicy(plan, input) {
  const mcp = mcpToolMetadata(input);
  if (mcp) {
    const specific = plan?.mcpPolicies?.get(mcpPolicyKey(mcp.serverName, mcp.name));
    if (specific) {
      return {
        kind: 'mcp',
        name: mcp.name,
        serverName: specific.serverName,
        enabled: specific.enabled,
        policy: specific.policy,
      };
    }
    const defaults = plan?.mcpDefaults?.get(normalizeToolName(mcp.serverName));
    if (defaults) {
      return {
        kind: 'mcp',
        name: mcp.name,
        serverName: defaults.serverName,
        enabled: defaults.enabled,
        policy: defaults.policy,
      };
    }
    console.log(JSON.stringify({
      level: 'warn',
      msg: 'claude mcp tool policy missing',
      mcp_server_name: mcp.serverName,
      tool_name: mcp.name,
    }));
    return { kind: 'mcp', name: mcp.name, serverName: mcp.serverName, enabled: false, policy: 'always_ask' };
  }

  const name = normalizeToolName(input?.tool_name);
  const builtin = plan?.builtinPolicies?.get(name);
  if (builtin) {
    return {
      kind: 'builtin',
      name,
      enabled: builtin.enabled,
      policy: builtin.policy,
    };
  }
  return { kind: 'builtin', name, enabled: false, policy: 'always_ask' };
}

function defaultToolConfig(config, fallbackPolicy) {
  return {
    enabled: booleanValue(config?.enabled, true),
    policy: permissionPolicyType(config?.permission_policy, fallbackPolicy),
  };
}

function permissionPolicyType(policy, fallback) {
  const policyType = String(policy?.type ?? '').trim();
  return policyType === 'always_allow' || policyType === 'always_ask' ? policyType : fallback;
}

function booleanValue(value, fallback) {
  return typeof value === 'boolean' ? value : fallback;
}

function mcpPolicyKey(serverName, toolName) {
  return `${normalizeToolName(serverName)}\u0000${normalizeToolName(toolName)}`;
}

function mcpSDKToolName(serverName, toolName) {
  const normalizedServerName = normalizeToolName(serverName);
  const normalizedToolName = normalizeToolName(toolName);
  if (!normalizedServerName || !normalizedToolName) {
    return '';
  }
  return `mcp__${normalizedServerName}__${normalizedToolName}`;
}

function compactStrings(values) {
  const out = [];
  const seen = new Set();
  for (const value of values) {
    const normalized = String(value ?? '').trim();
    if (!normalized || seen.has(normalized)) {
      continue;
    }
    seen.add(normalized);
    out.push(normalized);
  }
  return out;
}

function isCustomToolInput(customTools, input) {
  const rawName = String(input?.tool_name ?? '').trim();
  if (customTools.toolNames.has(normalizeToolName(rawName))) {
    return true;
  }
  const mcp = mcpToolMetadata(input);
  return normalizeToolName(mcp?.serverName) === normalizeToolName('sandbox0_custom_tools')
    && customTools.toolNames.has(normalizeToolName(mcp?.name));
}

function buildCustomToolAdapters(tools, onInvoke) {
  const customTools = [];
  const toolNames = new Set();
  for (const entry of tools ?? []) {
    if (!entry || typeof entry !== 'object' || entry.type !== 'custom') {
      continue;
    }
    const name = String(entry.name ?? '').trim();
    const description = String(entry.description ?? '').trim();
    if (!name || !description) {
      continue;
    }
    customTools.push({
      name,
      description,
      inputSchema: jsonSchemaObjectToZodShape(entry.input_schema),
      handler: async (args) => onInvoke(entry, args),
    });
    toolNames.add(normalizeToolName(name));
  }
  if (customTools.length === 0) {
    return { mcpServers: undefined, toolNames };
  }
  const server = createSdkMcpServer({
    name: 'sandbox0_custom_tools',
    tools: customTools,
  });
  return {
    mcpServers: {
      [server.name]: server,
    },
    toolNames,
  };
}

function mergeMcpServers(...candidates) {
  const merged = {};
  for (const candidate of candidates) {
    if (!candidate || typeof candidate !== 'object' || Array.isArray(candidate)) {
      continue;
    }
    Object.assign(merged, candidate);
  }
  return Object.keys(merged).length > 0 ? merged : undefined;
}

export function mcpServersFromAgent(definitions) {
  const servers = {};
  for (const entry of Array.isArray(definitions) ? definitions : []) {
    if (!entry || typeof entry !== 'object' || Array.isArray(entry)) {
      continue;
    }
    if (String(entry.type ?? '').trim() !== 'url') {
      continue;
    }
    const name = String(entry.name ?? '').trim();
    const serverURL = String(entry.url ?? '').trim();
    if (!name || !serverURL) {
      continue;
    }
    servers[name] = mcpServerConfigForURL(serverURL);
  }
  return Object.keys(servers).length > 0 ? servers : undefined;
}

function mcpServerConfigForURL(serverURL) {
  return {
    type: isSSEServerURL(serverURL) ? 'sse' : 'http',
    url: serverURL,
  };
}

function isSSEServerURL(serverURL) {
  try {
    const parsed = new URL(serverURL);
    return /\/sse$/i.test(parsed.pathname.replace(/\/+$/, ''));
  } catch {
    return /\/sse(?:$|[?#])/i.test(String(serverURL));
  }
}

function jsonSchemaObjectToZodShape(schema) {
  const properties = schema && typeof schema === 'object' && schema.properties && typeof schema.properties === 'object'
    ? schema.properties
    : {};
  const required = new Set(Array.isArray(schema?.required) ? schema.required : []);
  const shape = {};
  for (const [name, propertySchema] of Object.entries(properties)) {
    let field = jsonSchemaToZod(propertySchema);
    if (!required.has(name)) {
      field = field.optional();
    }
    shape[name] = field;
  }
  return shape;
}

function jsonSchemaToZod(schema) {
  if (!schema || typeof schema !== 'object') {
    return z.any();
  }
  if (Array.isArray(schema.enum) && schema.enum.length > 0) {
    const values = schema.enum.filter((value) => ['string', 'number', 'boolean'].includes(typeof value));
    if (values.length === schema.enum.length) {
      const literals = values.map((value) => z.literal(value));
      return literals.length === 1 ? literals[0] : z.union(literals);
    }
  }
  const declaredType = Array.isArray(schema.type) ? schema.type.find((value) => value !== 'null') : schema.type;
  let resolved;
  switch (declaredType) {
    case 'string':
      resolved = z.string();
      break;
    case 'number':
      resolved = z.number();
      break;
    case 'integer':
      resolved = z.number().int();
      break;
    case 'boolean':
      resolved = z.boolean();
      break;
    case 'array':
      resolved = z.array(jsonSchemaToZod(schema.items));
      break;
    case 'object':
      resolved = z.object(jsonSchemaObjectToZodShape(schema)).passthrough();
      break;
    default:
      resolved = z.any();
      break;
  }
  if (schema.nullable === true || (Array.isArray(schema.type) && schema.type.includes('null'))) {
    return resolved.nullable();
  }
  return resolved;
}

function asStruct(input) {
  if (!input || typeof input !== 'object' || Array.isArray(input)) {
    return {};
  }
  return input;
}

function customToolResultToCallToolResult(event) {
  return {
    content: normalizeCustomToolContent(event?.content),
    isError: event?.is_error === true,
  };
}

function normalizeCustomToolContent(content) {
  if (!Array.isArray(content) || content.length === 0) {
    return [{ type: 'text', text: '' }];
  }
  return content.map((block) => {
    if (block && block.type === 'text' && typeof block.text === 'string') {
      return { type: 'text', text: block.text };
    }
    return { type: 'text', text: JSON.stringify(block ?? null) };
  });
}

function toolResultContent(value) {
  if (Array.isArray(value)) {
    return value;
  }
  if (typeof value === 'string') {
    return [{ type: 'text', text: value }];
  }
  if (value === undefined || value === null) {
    return [{ type: 'text', text: '' }];
  }
  return [{ type: 'text', text: JSON.stringify(value, null, 2) }];
}

function deferredPromise() {
  let resolve;
  let reject;
  const promise = new Promise((res, rej) => {
    resolve = res;
    reject = rej;
  });
  return { promise, resolve, reject };
}

function pendingActionSnapshots(pending) {
  if (pending instanceof Map) {
    return Array.from(pending.values()).map((action) => ({
      id: action.id,
      kind: action.kind,
      tool_use_id: action.tool_use_id,
      custom_tool_use_id: action.custom_tool_use_id,
      name: action.name,
      input: action.input === undefined ? undefined : recordInput(action.input),
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
      input: action.input === undefined ? undefined : recordInput(action.input),
    });
  }
  return map;
}

function addPendingAction(pending, action) {
  const map = pendingActionSnapshotMap(pending);
  map.set(action.id, action);
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

function newCustomToolUseID() {
  return `ctu_${Math.random().toString(16).slice(2, 10)}`;
}

function recordInput(value) {
  if (!value || typeof value !== 'object' || Array.isArray(value)) {
    return {};
  }
  return value;
}

function newEventID(prefix) {
  return `${prefix}_${Math.random().toString(16).slice(2, 10)}`;
}
