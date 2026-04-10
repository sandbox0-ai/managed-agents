import { postJSON } from '../lib/http.js';

function procdPublishURL(baseURL) {
  return `${String(baseURL ?? '').replace(/\/$/, '')}/webhook/publish`;
}

export class ProcdWebhookClient {
  constructor({ baseURL = process.env.AGENT_WRAPPER_PROCD_URL ?? `http://127.0.0.1:${process.env.PROCD_HTTP_PORT ?? '8095'}` } = {}) {
    this.baseURL = baseURL;
  }

  async send(session, payload) {
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
}
