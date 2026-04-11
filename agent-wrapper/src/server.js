import http from 'node:http';
import { readJSON, writeJSON } from './lib/http.js';
import { RuntimeStore } from './runtime/store.js';
import { ProcdWebhookClient } from './runtime/callbacks.js';
import { materializeSessionEnvironment } from './runtime/environment.js';
import { materializeSessionResources } from './runtime/resources.js';
import { ClaudeRuntime, sessionErrorEventForError } from './adapters/claude.js';

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
  stateDir = process.env.AGENT_WRAPPER_STATE_DIR ?? '/var/lib/agent-wrapper',
  runtime = new ClaudeRuntime(),
  callbackClient = new ProcdWebhookClient(),
} = {}) {
  const store = new RuntimeStore(stateDir);
  const controlToken = process.env.AGENT_WRAPPER_CONTROL_TOKEN ?? '';

  return http.createServer(async (req, res) => {
    const url = new URL(req.url, 'http://agent-wrapper.local');
    try {
      if (req.method === 'GET' && url.pathname === '/healthz') {
        writeJSON(res, 200, { ok: true });
        return;
      }

      if (!authorized(req, controlToken)) {
        writeJSON(res, 401, { error: 'unauthorized' });
        return;
      }

      if (req.method === 'PUT' && url.pathname === '/v1/runtime/session') {
        const body = await readJSON(req);
        if (!body.session_id) {
          writeJSON(res, 400, { error: 'session_id is required' });
          return;
        }
        const current = store.getSession(body.session_id);
        const session = {
          ...(current ?? {}),
          session_id: body.session_id,
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
        await materializeSessionEnvironment(session, { stateDir });
        await materializeSessionResources(session);
        store.upsertSession(body.session_id, () => session);
        writeJSON(res, 200, { session_id: session.session_id, vendor_session_id: session.vendor_session_id });
        return;
      }

      if (req.method === 'DELETE' && sessionPathname(url.pathname)) {
        runtime.deleteSession(sessionPathname(url.pathname));
        store.deleteSession(sessionPathname(url.pathname));
        writeJSON(res, 200, { deleted: true });
        return;
      }

      if (req.method === 'POST' && sessionResolveActionsPath(url.pathname)) {
        const sessionID = sessionResolveActionsPath(url.pathname);
        const session = store.getSession(sessionID);
        if (!session) {
          writeJSON(res, 404, { error: 'session not found' });
          return;
        }
        const body = await readJSON(req);
        console.log(JSON.stringify({
          level: 'info',
          msg: 'wrapper resolving actions',
          session_lookup_id: sessionID,
          stored_session_id: session.session_id ?? null,
          vendor_session_id: session.vendor_session_id ?? null,
          pending_actions: session.pending_actions ?? null,
          input_event_types: (body.events ?? []).map((event) => event?.type ?? null),
        }));
        const resolution = runtime.resolveActions(session.session_id, body.events ?? [], {
          getSession: () => store.getSession(session.session_id),
          persistSession: (updater) => store.upsertSession(session.session_id, updater),
        });
        writeJSON(res, 200, resolution);
        return;
      }

      if (req.method === 'POST' && url.pathname === '/v1/runs') {
        const body = await readJSON(req);
        const session = store.getSession(body.session_id);
        if (!session) {
          writeJSON(res, 404, { error: 'session not found' });
          return;
        }
        if (!body.run_id) {
          writeJSON(res, 400, { error: 'run_id is required' });
          return;
        }
        console.log(JSON.stringify({
          level: 'info',
          msg: 'wrapper accepted run',
          session_id: body.session_id,
          run_id: body.run_id,
          sandbox_id: session.sandbox_id ?? null,
          callback_url: session.callback_url ?? null,
          has_control_token: Boolean(session.control_token),
          vendor_session_id: session.vendor_session_id ?? null,
          pending_actions: session.pending_actions ?? null,
          input_event_types: (body.input_events ?? []).map((event) => event?.type ?? null),
        }));
        store.upsertSession(session.session_id, (current) => ({
          ...current,
          active_run_id: body.run_id,
          updated_at: new Date().toISOString(),
        }));
        const sessionStore = {
          getSession: () => store.getSession(session.session_id),
          persistSession: (updater) => store.upsertSession(session.session_id, updater),
        };
        queueMicrotask(async () => {
          try {
            await runtime.startRun(store.getSession(session.session_id), body, callbackClient, sessionStore);
          } catch (error) {
            console.log(JSON.stringify({
              level: 'error',
              msg: 'wrapper run failed',
              session_id: body.session_id,
              run_id: body.run_id,
              sandbox_id: session.sandbox_id ?? null,
              callback_url: session.callback_url ?? null,
              has_control_token: Boolean(session.control_token),
              error: error instanceof Error ? error.message : String(error),
            }));
            const latest = store.getSession(session.session_id);
            await callbackClient.send(latest ?? session, {
              session_id: latest?.session_id ?? session.session_id,
              run_id: body.run_id,
              vendor_session_id: latest?.vendor_session_id,
              events: [
                sessionErrorEventForError(error),
                { type: 'session.status_terminated' },
              ],
            }).catch(() => {});
          } finally {
            store.upsertSession(session.session_id, (current) => ({
              ...current,
              active_run_id: null,
              updated_at: new Date().toISOString(),
            }));
          }
        });
        writeJSON(res, 202, { accepted: true, run_id: body.run_id });
        return;
      }

      if (req.method === 'POST' && runInterruptPath(url.pathname)) {
        const runID = runInterruptPath(url.pathname);
        if (await runtime.interruptRun(runID)) {
          writeJSON(res, 200, { interrupted: true, run_id: runID });
          return;
        }
        writeJSON(res, 404, { error: 'run not found' });
        return;
      }

      writeJSON(res, 404, { error: 'not found' });
    } catch (error) {
      writeJSON(res, 500, { error: error instanceof Error ? error.message : String(error) });
    }
  });
}
