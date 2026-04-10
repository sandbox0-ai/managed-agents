import crypto from 'node:crypto';
import { postJSON } from '../lib/http.js';

function procdPublishURL(baseURL) {
  return `${String(baseURL ?? '').replace(/\/$/, '')}/webhook/publish`;
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
      try {
        return await this.sendManagedAgentWebhook(session, payload, callbackURL);
      } catch (error) {
        console.log(JSON.stringify({
          level: 'error',
          msg: 'wrapper managed-agent webhook publish failed',
          session_id: session?.session_id ?? null,
          sandbox_id: session?.sandbox_id ?? null,
          has_control_token: Boolean(String(session?.control_token ?? this.controlToken ?? '').trim()),
          event_types: Array.isArray(payload?.events) ? payload.events.map((event) => event?.type ?? null) : [],
          url: callbackURL,
          error: error instanceof Error ? error.message : String(error),
        }));
        throw error;
      }
    }
    const publishURL = procdPublishURL(this.baseURL);
    try {
      return await postJSON(publishURL, { payload }, {}, 15000);
    } catch (error) {
      console.log(JSON.stringify({
        level: 'error',
        msg: 'wrapper webhook publish failed',
        session_id: session.session_id ?? null,
        event_types: Array.isArray(payload?.events) ? payload.events.map((event) => event?.type ?? null) : [],
        url: publishURL,
        error: error instanceof Error ? error.message : String(error),
      }));
      throw error;
    }
  }

  async sendManagedAgentWebhook(session, payload, callbackURL) {
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
      throw new Error(`POST ${callbackURL} failed with ${response.status}: ${text}`);
    }
    if (!text) {
      return null;
    }
    return JSON.parse(text);
  }
}
