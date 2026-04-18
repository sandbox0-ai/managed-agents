import crypto from 'node:crypto';
import { postJSON } from '../lib/http.js';
import { logError, logWarn, safeErrorMessage } from '../lib/log.js';
import { startOperation } from '../lib/observability.js';

const managedAgentWebhookRetryDelays = [250, 500, 1000, 2000, 4000];

function procdPublishURL(baseURL) {
  return `${String(baseURL ?? '').replace(/\/$/, '')}/webhook/publish`;
}

function sleep(ms) {
  return new Promise((resolve) => setTimeout(resolve, ms));
}

class WebhookHTTPError extends Error {
  constructor(callbackURL, status, body) {
    super(`POST ${callbackURL} failed with ${status}: ${body}`);
    this.name = 'WebhookHTTPError';
    this.status = status;
  }
}

function isRetriableManagedAgentWebhookError(error) {
  if (error instanceof WebhookHTTPError) {
    return error.status === 408 || error.status === 429 || error.status >= 500;
  }
  const name = String(error?.name ?? '').toLowerCase();
  const message = String(error?.message ?? error ?? '').toLowerCase();
  return name.includes('timeout')
    || message.includes('fetch failed')
    || message.includes('network')
    || message.includes('timeout')
    || message.includes('econnreset')
    || message.includes('econnrefused')
    || message.includes('enotfound');
}

export class ProcdWebhookClient {
  constructor({
    baseURL = process.env.AGENT_WRAPPER_PROCD_URL ?? `http://127.0.0.1:${process.env.PROCD_HTTP_PORT ?? '8095'}`,
    controlToken = process.env.AGENT_WRAPPER_CONTROL_TOKEN ?? '',
  } = {}) {
    this.baseURL = baseURL;
    this.controlToken = controlToken;
  }

  async send(session, payload) {
    const callbackURL = String(session?.callback_url ?? '').trim();
    if (callbackURL) {
      const webhookOp = startOperation('wrapper_managed_agent_webhook', {
        session_id: session?.session_id ?? null,
        sandbox_id: session?.sandbox_id ?? null,
        callback_url: callbackURL,
        event_count: Array.isArray(payload?.events) ? payload.events.length : 0,
        event_types: Array.isArray(payload?.events) ? payload.events.map((event) => event?.type ?? null) : [],
        run_id: payload?.run_id ?? null,
        has_control_token: Boolean(String(session?.control_token ?? this.controlToken ?? '').trim()),
      });
      try {
        const result = await this.sendManagedAgentWebhook(session, payload, callbackURL, webhookOp);
        webhookOp.end(null);
        return result;
      } catch (error) {
        webhookOp.end(error);
        logError('wrapper managed-agent webhook publish failed', {
          session_id: session?.session_id ?? null,
          sandbox_id: session?.sandbox_id ?? null,
          has_control_token: Boolean(String(session?.control_token ?? this.controlToken ?? '').trim()),
          event_types: Array.isArray(payload?.events) ? payload.events.map((event) => event?.type ?? null) : [],
          url: callbackURL,
          error: safeErrorMessage(error),
        });
        throw error;
      }
    }
    const publishURL = procdPublishURL(this.baseURL);
    try {
      return await postJSON(publishURL, { payload }, {}, 15000);
    } catch (error) {
      logError('wrapper webhook publish failed', {
        session_id: session.session_id ?? null,
        event_types: Array.isArray(payload?.events) ? payload.events.map((event) => event?.type ?? null) : [],
        url: publishURL,
        error: safeErrorMessage(error),
      });
      throw error;
    }
  }

  async sendManagedAgentWebhook(session, payload, callbackURL, webhookOp = null) {
    let lastError = null;
    for (let attempt = 0; attempt <= managedAgentWebhookRetryDelays.length; attempt += 1) {
      try {
        if (webhookOp) {
          webhookOp.addFields({ attempts: attempt + 1 });
          return await webhookOp.runPhase('post_callback', () => this.postManagedAgentWebhook(session, payload, callbackURL), {
            attempt: attempt + 1,
          });
        }
        return await this.postManagedAgentWebhook(session, payload, callbackURL);
      } catch (error) {
        lastError = error;
        const delayMs = managedAgentWebhookRetryDelays[attempt];
        if (delayMs == null || !isRetriableManagedAgentWebhookError(error)) {
          throw error;
        }
        logWarn('wrapper managed-agent webhook publish retrying', {
          session_id: session?.session_id ?? null,
          sandbox_id: session?.sandbox_id ?? null,
          event_types: Array.isArray(payload?.events) ? payload.events.map((event) => event?.type ?? null) : [],
          url: callbackURL,
          attempt: attempt + 1,
          delay_ms: delayMs,
          error: safeErrorMessage(error),
        });
        await sleep(delayMs);
      }
    }
    throw lastError;
  }

  async postManagedAgentWebhook(session, payload, callbackURL) {
    const sandboxID = String(session?.sandbox_id ?? '').trim();
    const controlToken = String(session?.control_token ?? this.controlToken ?? '').trim();
    if (!sandboxID) {
      throw new Error('sandbox_id is required for managed-agent webhook delivery');
    }
    if (!controlToken) {
      throw new Error('control token is required for managed-agent webhook delivery');
    }
    const envelope = {
      event_type: 'agent.event',
      sandbox_id: sandboxID,
      payload,
    };
    const body = JSON.stringify(envelope);
    const signature = crypto.createHmac('sha256', controlToken).update(body).digest('hex');
    const response = await fetch(callbackURL, {
      method: 'POST',
      signal: AbortSignal.timeout(15000),
      headers: {
        'content-type': 'application/json',
        'x-sandbox0-signature': signature,
      },
      body,
    });
    const text = await response.text();
    if (!response.ok) {
      throw new WebhookHTTPError(callbackURL, response.status, text);
    }
    if (!text) {
      return null;
    }
    return JSON.parse(text);
  }
}
