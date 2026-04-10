import { createSdkMcpServer, query } from '@anthropic-ai/claude-agent-sdk';
import { z } from 'zod';
import { inputEventsToPrompt } from '../lib/prompt.js';

async function* emptyPromptStream() {}

export function runtimeEnvForEngine(engine) {
  return {
    ...process.env,
    ...(engine?.env ?? {}),
  };
}

export class ClaudeRuntime {
  constructor() {
    this.activeRuns = new Map();
    this.pendingActions = new Map();
    this.emittedToolUses = new Map();
  }

  async startRun(session, run, callbackClient, sessionStore) {
    const prompt = buildPromptInput(run.input_events);
    const options = this.#buildOptions(session, run, callbackClient, sessionStore);
    if (session.vendor_session_id) {
      options.resume = session.vendor_session_id;
    }
    const stream = query({ prompt, options });
    this.activeRuns.set(run.run_id, { stream, sessionID: session.session_id });
    try {
      for await (const message of stream) {
        if (!session.vendor_session_id && message?.session_id) {
          session = sessionStore.persistSession((current) => ({
            ...current,
            vendor_session_id: message.session_id,
          }));
        }
        const callbackPayload = this.#mapMessage(session, run, message);
        if (callbackPayload) {
          await callbackClient.send(session, callbackPayload);
        }
      }
    } finally {
      this.pendingActions.delete(session.session_id);
      this.emittedToolUses.delete(session.session_id);
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
        const action = pendingSnapshots.get(actionID);
        if ((!action || action.kind !== 'tool_confirmation') && !hasVendorSession) {
          continue;
        }
        if (action && action.kind === 'tool_confirmation') {
          pendingSnapshots.delete(action.id);
        }
        resolved += 1;
        resumeRequired = true;
        toolConfirmationResolutions[actionID] = {
          result: this.#permissionDecision(event),
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
    const permissionPolicies = buildPermissionPolicies(session.agent?.tools);
    const customTools = buildCustomToolAdapters(
      session.agent?.tools,
      async (tool, args) => this.#invokeCustomTool(session, run, callbackClient, sessionStore, tool, args),
    );
    return {
      cwd: session.working_directory,
      model: engine.model,
      permissionMode: engine.permission_mode ?? 'default',
      allowDangerouslySkipPermissions: false,
      systemPrompt: typeof session.agent?.system === 'string' ? session.agent.system : undefined,
      pathToClaudeCodeExecutable: engine.path_to_claude_code_executable,
      maxTurns: engine.max_turns,
      mcpServers: mergeMcpServers(engine.mcp_servers, customTools.mcpServers),
      settings: engine.settings,
      extraArgs: engine.extra_args,
      env: runtimeEnvForEngine(engine),
      persistSession: true,
      canUseTool: async (_toolName, _input, options) => ({
        behavior: 'allow',
        toolUseID: String(options.toolUseID ?? ''),
      }),
      hooks: {
        PreToolUse: [{
          hooks: [async (input) => {
            const eventID = String(input.tool_use_id ?? '');
            const normalizedToolName = normalizeToolName(input.tool_name);
            if (customTools.toolNames.has(normalizedToolName)) {
              return { continue: true };
            }
            const currentSession = sessionStore.getSession() ?? session;
            const askForConfirmation = permissionPolicies.get(normalizedToolName) !== 'always_allow';
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
            if (!this.#wasToolUseEmitted(currentSession.session_id, eventID)) {
              this.#markToolUseEmitted(currentSession.session_id, eventID);
              sessionStore.persistSession((latest) => ({
                ...latest,
                pending_actions: pendingActionSnapshots(addPendingAction(latest?.pending_actions, {
                  id: eventID,
                  kind: 'tool_confirmation',
                  tool_use_id: eventID,
                })),
                updated_at: new Date().toISOString(),
              }));
              await callbackClient.send(currentSession, {
                session_id: currentSession.session_id,
                run_id: run.run_id,
                vendor_session_id: currentSession.vendor_session_id,
                events: [
                  {
                    type: 'agent.tool_use',
                    id: eventID,
                    name: normalizedToolName,
                    input: asStruct(input.tool_input),
                    evaluated_permission: 'ask',
                  },
                  {
                    type: 'session.status_idle',
                    stop_reason: {
                      type: 'requires_action',
                      event_ids: [eventID],
                    },
                  },
                ],
              });
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
            if (customTools.toolNames.has(normalizeToolName(input.tool_name))) {
              return { continue: true };
            }
            const currentSession = sessionStore.getSession() ?? session;
            await callbackClient.send(currentSession, {
              session_id: currentSession.session_id,
              run_id: run.run_id,
              vendor_session_id: currentSession.vendor_session_id,
              events: [{
                type: 'agent.tool_result',
                tool_use_id: String(input.tool_use_id ?? ''),
                content: toolResultContent(input.tool_response),
              }],
            });
            return { continue: true };
          }],
        }],
        PostToolUseFailure: [{
          hooks: [async (input) => {
            if (customTools.toolNames.has(normalizeToolName(input.tool_name))) {
              return { continue: true };
            }
            const currentSession = sessionStore.getSession() ?? session;
            await callbackClient.send(currentSession, {
              session_id: currentSession.session_id,
              run_id: run.run_id,
              vendor_session_id: currentSession.vendor_session_id,
              events: [{
                type: 'agent.tool_result',
                tool_use_id: String(input.tool_use_id ?? ''),
                content: toolResultContent(input.error ?? 'tool execution failed'),
                is_error: true,
              }],
            });
            return { continue: true };
          }],
        }],
      },
    };
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

  #permissionDecision(event) {
    if (event.result === 'allow') {
      return { behavior: 'allow', toolUseID: String(event.tool_use_id ?? '') };
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

  #mapMessage(session, run, message) {
    if (!message || typeof message !== 'object') {
      return null;
    }
    if (message.type === 'assistant' && Array.isArray(message.message?.content)) {
      const events = [];
      for (const block of message.message.content) {
        if (block?.type === 'text' && typeof block.text === 'string') {
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
      if (message.subtype === 'success') {
        if (message.stop_reason === 'tool_deferred') {
          return null;
        }
        return {
          session_id: session.session_id,
          run_id: run.run_id,
          vendor_session_id: message.session_id,
          usage_delta: {
            input_tokens: message.usage?.input_tokens ?? 0,
            output_tokens: message.usage?.output_tokens ?? 0,
            cache_read_input_tokens: message.usage?.cache_read_input_tokens ?? 0,
          },
          events: [{
            type: 'session.status_idle',
            stop_reason: { type: 'end_turn' },
          }],
        };
      }
      return {
        session_id: session.session_id,
        run_id: run.run_id,
        vendor_session_id: message.session_id,
        events: [
          {
            type: 'session.error',
            error: {
              type: 'unknown_error',
              message: (message.errors ?? []).join('; ') || message.subtype || 'Claude run failed',
            },
          },
          {
            type: 'session.status_terminated',
          },
        ],
      };
    }

    return null;
  }
}

function buildPromptInput(events) {
  const prompt = inputEventsToPrompt(filteredPromptEvents(events));
  if (prompt) {
    return prompt;
  }
  return emptyPromptStream();
}

function filteredPromptEvents(events) {
  return (events ?? []).filter((event) => event?.type !== 'user.tool_confirmation');
}

function normalizeToolName(toolName) {
  return String(toolName ?? '').trim().toLowerCase().replace(/\s+/g, '_');
}

function buildPermissionPolicies(tools) {
  const policies = new Map();
  for (const tool of tools ?? []) {
    if (!tool || typeof tool !== 'object' || tool.type !== 'agent_toolset_20260401') {
      continue;
    }
    const defaultPolicy = tool.default_config?.permission_policy?.type === 'always_allow' ? 'always_allow' : 'always_ask';
    for (const name of ['bash', 'edit', 'read', 'write', 'glob', 'grep', 'web_fetch', 'web_search']) {
      policies.set(name, defaultPolicy);
    }
    for (const config of tool.configs ?? []) {
      const name = normalizeToolName(config?.name);
      if (!name) {
        continue;
      }
      const policy = config?.permission_policy?.type === 'always_allow' ? 'always_allow' : defaultPolicy;
      policies.set(name, policy);
    }
  }
  return policies;
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
