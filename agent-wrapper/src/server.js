import http from 'node:http';
import { readJSON, writeJSON } from './lib/http.js';
import { RuntimeStore } from './runtime/store.js';
import { ProcdWebhookClient } from './runtime/callbacks.js';
import { materializeSessionEnvironment } from './runtime/environment.js';
import { materializeSessionResources } from './runtime/resources.js';
import { createDefaultRuntime, finalStatusEventForSessionError, normalizeVendor, sessionErrorEventForError } from './adapters/index.js';
import { agentWrapperStateDir } from './adapters/runtime.js';
import { logError, logInfo, logWarn, summarizePendingActions, safeErrorMessage } from './lib/log.js';
import { requestFields, startOperation } from './lib/observability.js';

function sessionPathname(pathname) {
  const match = pathname.match(/^\/v1\/runtime\/session\/([^/]+)$/);
  return match ? decodeURIComponent(match[1]) : null;
}

function runInterruptPath(pathname) {
  const match = pathname.match(/^\/v1\/runs\/([^/]+)\/interrupt$/);
  return match ? decodeURIComponent(match[1]) : null;
}

function sessionResolveActionsPath(pathname) {
  const match = pathname.match(/^\/v1\/runtime\/session\/([^/]+)\/actions\/resolve$/);
  return match ? decodeURIComponent(match[1]) : null;
}

function authorized(req, token) {
  if (!token) {
    return true;
  }
  const header = String(req.headers.authorization ?? '');
  return header.startsWith('Bearer ') && header.slice('Bearer '.length) === token;
}

export function createServer({
  stateDir = agentWrapperStateDir(),
  runtime = createDefaultRuntime(),
  callbackClient = new ProcdWebhookClient(),
} = {}) {
  const store = new RuntimeStore(stateDir);
  const controlToken = process.env.AGENT_WRAPPER_CONTROL_TOKEN ?? '';

  return http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://agent-wrapper.local');
    const requestOp = startOperation('wrapper_request', requestFields(req, null));
    let requestError = null;
    try {
      const respond = (status, payload, fields = {}) => {
        requestOp.addFields({ status_code: status, ...fields });
        writeJSON(res, status, payload);
      };

      if (req.method === 'GET' && url.pathname === '/healthz') {
        requestOp.addFields({ route: 'healthz' });
        respond(200, { ok: true });
        return;
      }

      if (!authorized(req, controlToken)) {
        requestOp.addFields({ route: 'auth' });
        respond(401, { error: 'unauthorized' });
        return;
      }

      if (req.method === 'PUT' && url.pathname === '/v1/runtime/session') {
        requestOp.addFields({ route: 'put_session' });
        const body = await requestOp.runPhase('read_body', () => readJSON(req));
        if (!body.session_id) {
          respond(400, { error: 'session_id is required' });
          return;
        }
        const current = store.getSession(body.session_id);
        const session = {
          ...(current ?? {}),
          session_id: body.session_id,
          vendor: normalizeVendor(body.vendor ?? current?.vendor ?? process.env.AGENT_WRAPPER_DEFAULT_VENDOR),
          sandbox_id: body.sandbox_id ?? current?.sandbox_id ?? null,
          callback_url: body.callback_url ?? current?.callback_url ?? null,
          control_token: body.control_token ?? current?.control_token ?? null,
          working_directory: body.working_directory ?? current?.working_directory ?? process.cwd(),
          agent: body.agent ?? current?.agent ?? null,
          environment_id: body.environment_id ?? current?.environment_id ?? null,
          environment: body.environment ?? current?.environment ?? null,
          environment_artifact: body.environment_artifact ?? current?.environment_artifact ?? null,
          resources: body.resources ?? current?.resources ?? [],
          vault_ids: body.vault_ids ?? current?.vault_ids ?? [],
          bootstrap_events: Array.isArray(body.bootstrap_events) ? body.bootstrap_events : [],
          skill_names: body.skill_names ?? current?.skill_names ?? [],
          engine: body.engine ?? current?.engine ?? {},
          vendor_session_id: body.vendor_session_id ?? current?.vendor_session_id ?? null,
          pending_actions: current?.pending_actions ?? [],
          tool_confirmation_resolutions: current?.tool_confirmation_resolutions ?? {},
          updated_at: new Date().toISOString(),
        };
        requestOp.addFields({
          session_id: session.session_id,
          sandbox_id: session.sandbox_id ?? null,
          vendor: session.vendor ?? null,
        });
        await requestOp.runPhase('materialize_environment', () => materializeSessionEnvironment(session, { stateDir }), {
          environment_id: session.environment_id ?? null,
        });
        await requestOp.runPhase('materialize_resources', () => materializeSessionResources(session), {
          resource_count: Array.isArray(session.resources) ? session.resources.length : 0,
          vault_count: Array.isArray(session.vault_ids) ? session.vault_ids.length : 0,
        });
        store.upsertSession(body.session_id, () => session);
        const sessionStore = {
          getSession: () => store.getSession(session.session_id),
          persistSession: (updater) => store.upsertSession(session.session_id, updater),
        };
        const bootstrapRequestFields = {
          ...requestFields(req, 'put_session', {
            session_id: session.session_id,
            sandbox_id: session.sandbox_id ?? null,
            vendor: session.vendor ?? null,
          }),
        };
        queueMicrotask(() => {
          const prestartOp = startOperation('wrapper_prestart_session', bootstrapRequestFields);
          void prestartOp.runPhase('runtime_prestart', () => runtime.prestartSession(store.getSession(session.session_id), sessionStore))
            .then(() => prestartOp.end(null))
            .catch((error) => {
              prestartOp.end(error);
            });
        });
        respond(200, { session_id: session.session_id, vendor_session_id: session.vendor_session_id });
        return;
      }

      if (req.method === 'DELETE' && sessionPathname(url.pathname)) {
        const sessionID = sessionPathname(url.pathname);
        requestOp.addFields({ route: 'delete_session', session_id: sessionID });
        await requestOp.runPhase('delete_runtime_session', () => runtime.deleteSession(sessionID, store.getSession(sessionID)));
        store.deleteSession(sessionID);
        respond(200, { deleted: true });
        return;
      }

      if (req.method === 'POST' && sessionResolveActionsPath(url.pathname)) {
        const sessionID = sessionResolveActionsPath(url.pathname);
        requestOp.addFields({ route: 'resolve_actions', session_lookup_id: sessionID });
        const session = store.getSession(sessionID);
        if (!session) {
          respond(404, { error: 'session not found' });
          return;
        }
        requestOp.addFields({
          session_id: session.session_id ?? null,
          sandbox_id: session.sandbox_id ?? null,
          vendor: session.vendor ?? null,
          vendor_session_id: session.vendor_session_id ?? null,
        });
        const body = await requestOp.runPhase('read_body', () => readJSON(req));
        logInfo('wrapper resolving actions', {
          session_lookup_id: sessionID,
          stored_session_id: session.session_id ?? null,
          vendor_session_id: session.vendor_session_id ?? null,
          ...summarizePendingActions(session.pending_actions),
          input_event_types: (body.events ?? []).map((event) => event?.type ?? null),
        });
        const resolution = await requestOp.runPhase('runtime_resolve_actions', () => runtime.resolveActions(session.session_id, body.events ?? [], {
          getSession: () => store.getSession(session.session_id),
          persistSession: (updater) => store.upsertSession(session.session_id, updater),
        }), {
          pending_action_count: Array.isArray(session.pending_actions) ? session.pending_actions.length : 0,
          input_event_count: Array.isArray(body.events) ? body.events.length : 0,
        });
        requestOp.addFields({
          remaining_action_count: Array.isArray(resolution?.remaining_action_ids) ? resolution.remaining_action_ids.length : 0,
          resolved_count: Number.isFinite(resolution?.resolved_count) ? resolution.resolved_count : null,
        });
        respond(200, resolution);
        return;
      }

      if (req.method === 'POST' && url.pathname === '/v1/runs') {
        requestOp.addFields({ route: 'start_run' });
        const body = await requestOp.runPhase('read_body', () => readJSON(req));
        const session = store.getSession(body.session_id);
        if (!session) {
          respond(404, { error: 'session not found' });
          return;
        }
        if (!body.run_id) {
          respond(400, { error: 'run_id is required' });
          return;
        }
        requestOp.addFields({
          session_id: body.session_id,
          run_id: body.run_id,
          sandbox_id: session.sandbox_id ?? null,
          vendor: session.vendor ?? null,
          vendor_session_id: session.vendor_session_id ?? null,
          input_event_count: Array.isArray(body.input_events) ? body.input_events.length : 0,
        });
        logInfo('wrapper accepted run', {
          session_id: body.session_id,
          run_id: body.run_id,
          sandbox_id: session.sandbox_id ?? null,
          callback_url: session.callback_url ?? null,
          has_control_token: Boolean(session.control_token),
          vendor_session_id: session.vendor_session_id ?? null,
          ...summarizePendingActions(session.pending_actions),
          input_event_types: (body.input_events ?? []).map((event) => event?.type ?? null),
        });
        await requestOp.runPhase('mark_run_active', () => Promise.resolve(store.upsertSession(session.session_id, (current) => ({
          ...current,
          active_run_id: body.run_id,
          updated_at: new Date().toISOString(),
        }))));
        const sessionStore = {
          getSession: () => store.getSession(session.session_id),
          persistSession: (updater) => store.upsertSession(session.session_id, updater),
        };
        const acceptedAtMs = Date.now();
        const runRequestFields = {
          ...requestFields(req, 'start_run', {
            session_id: body.session_id,
            run_id: body.run_id,
            sandbox_id: session.sandbox_id ?? null,
            vendor: session.vendor ?? null,
            vendor_session_id: session.vendor_session_id ?? null,
            callback_url: session.callback_url ?? null,
          }),
        };
        queueMicrotask(async () => {
          const runOp = startOperation('wrapper_run', runRequestFields);
          logInfo('wrapper starting runtime run', {
            session_id: body.session_id,
            run_id: body.run_id,
            sandbox_id: session.sandbox_id ?? null,
            vendor_session_id: session.vendor_session_id ?? null,
            queue_delay_ms: Date.now() - acceptedAtMs,
          });
          try {
            runOp.addFields({ queue_delay_ms: Date.now() - acceptedAtMs });
            await runOp.runPhase('runtime_start_run', () => runtime.startRun(store.getSession(session.session_id), body, callbackClient, sessionStore), {
              input_event_count: Array.isArray(body.input_events) ? body.input_events.length : 0,
            });
            const latest = store.getSession(session.session_id);
            runOp.addFields({
              sandbox_id: latest?.sandbox_id ?? session.sandbox_id ?? null,
              vendor_session_id: latest?.vendor_session_id ?? session.vendor_session_id ?? null,
            });
            logInfo('wrapper runtime run completed', {
              session_id: body.session_id,
              run_id: body.run_id,
              sandbox_id: latest?.sandbox_id ?? session.sandbox_id ?? null,
              vendor_session_id: latest?.vendor_session_id ?? session.vendor_session_id ?? null,
              elapsed_ms: Date.now() - acceptedAtMs,
            });
            runOp.end(null, { elapsed_ms: Date.now() - acceptedAtMs });
          } catch (error) {
            runOp.addFields({
              has_control_token: Boolean(session.control_token),
            });
            logError('wrapper run failed', {
              session_id: body.session_id,
              run_id: body.run_id,
              sandbox_id: session.sandbox_id ?? null,
              callback_url: session.callback_url ?? null,
              has_control_token: Boolean(session.control_token),
              error: safeErrorMessage(error),
            });
            const latest = store.getSession(session.session_id);
            const errorEvent = sessionErrorEventForError(error);
            await runOp.runPhase('send_failure_callback', () => callbackClient.send(latest ?? session, {
              session_id: latest?.session_id ?? session.session_id,
              run_id: body.run_id,
              vendor_session_id: latest?.vendor_session_id,
              events: [
                errorEvent,
                finalStatusEventForSessionError(errorEvent),
              ],
            }), {
              callback_url: session.callback_url ?? null,
              failure_event_type: errorEvent?.error?.type ?? null,
            }).catch(() => {});
            runOp.end(error, { elapsed_ms: Date.now() - acceptedAtMs });
          } finally {
            await runOp.runPhase('clear_active_run', () => Promise.resolve(store.upsertSession(session.session_id, (current) => ({
              ...current,
              active_run_id: null,
              updated_at: new Date().toISOString(),
            }))));
          }
        });
        respond(202, { accepted: true, run_id: body.run_id });
        return;
      }

      if (req.method === 'POST' && runInterruptPath(url.pathname)) {
        const runID = runInterruptPath(url.pathname);
        requestOp.addFields({ route: 'interrupt_run', run_id: runID });
        if (await requestOp.runPhase('runtime_interrupt_run', () => runtime.interruptRun(runID))) {
          respond(200, { interrupted: true, run_id: runID });
          return;
        }
        respond(404, { error: 'run not found' });
        return;
      }

      requestOp.addFields({ route: 'not_found' });
      respond(404, { error: 'not found' });
    } catch (error) {
      requestError = error;
      requestOp.addFields({ status_code: 500 });
      writeJSON(res, 500, { error: error instanceof Error ? error.message : String(error) });
    } finally {
      requestOp.end(requestError, {
        status_code: res.statusCode || (requestError ? 500 : 200),
      });
    }
  });
}
