import crypto from 'node:crypto';
import http from 'node:http';

const port = Number(process.env.PORT ?? '8080');
const sessions = new Map();

function readJSON(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    req.on('data', (chunk) => chunks.push(chunk));
    req.on('end', () => {
      const raw = Buffer.concat(chunks).toString('utf8');
      if (!raw.trim()) {
        resolve({});
        return;
      }
      try {
        resolve(JSON.parse(raw));
      } catch (error) {
        reject(error);
      }
    });
    req.on('error', reject);
  });
}

function writeJSON(res, status, body) {
  const raw = JSON.stringify(body);
  res.writeHead(status, {
    'content-type': 'application/json',
    'content-length': Buffer.byteLength(raw),
  });
  res.end(raw);
}

function sessionPath(pathname) {
  const match = pathname.match(/^\/v1\/runtime\/session\/([^/]+)$/);
  return match ? decodeURIComponent(match[1]) : '';
}

function resolveActionsPath(pathname) {
  const match = pathname.match(/^\/v1\/runtime\/session\/([^/]+)\/actions\/resolve$/);
  return match ? decodeURIComponent(match[1]) : '';
}

async function postWebhook(session, run) {
  if (!session.callback_url || !session.control_token || !session.sandbox_id) {
    return;
  }
  const text = inputText(run.input_events);
  const payload = {
    session_id: session.session_id,
    run_id: run.run_id,
    vendor_session_id: session.vendor_session_id,
    usage_delta: {
      input_tokens: 3,
      output_tokens: 2,
      cache_read_input_tokens: 1,
    },
    events: [
      {
        type: 'agent.message',
        content: [{ type: 'text', text: `fake-wrapper response: ${text}` }],
      },
      {
        type: 'session.status_idle',
        stop_reason: { type: 'end_turn' },
      },
    ],
  };
  const envelope = {
    event_id: `evt_${crypto.randomUUID().replaceAll('-', '')}`,
    event_type: 'agent.event',
    sandbox_id: session.sandbox_id,
    payload,
  };
  const body = JSON.stringify(envelope);
  const signature = crypto.createHmac('sha256', session.control_token).update(body).digest('hex');
  const response = await fetch(session.callback_url, {
    method: 'POST',
    headers: {
      'content-type': 'application/json',
      'x-sandbox0-signature': signature,
    },
    body,
  });
  if (!response.ok) {
    throw new Error(`callback failed with ${response.status}: ${await response.text()}`);
  }
}

function inputText(events) {
  for (const event of events ?? []) {
    if (event?.type !== 'user.message') {
      continue;
    }
    if (typeof event.content === 'string') {
      return event.content.trim();
    }
    if (Array.isArray(event.content)) {
      return event.content
        .filter((block) => block?.type === 'text')
        .map((block) => String(block.text ?? '').trim())
        .filter(Boolean)
        .join(' ');
    }
  }
  return 'ok';
}

const server = http.createServer(async (req, res) => {
  try {
    const url = new URL(req.url, 'http://fake-wrapper.local');

    if (req.method === 'GET' && url.pathname === '/healthz') {
      writeJSON(res, 200, { ok: true });
      return;
    }

    if (req.method === 'PUT' && url.pathname === '/v1/runtime/session') {
      const body = await readJSON(req);
      const sessionID = String(body.session_id ?? '').trim();
      if (!sessionID) {
        writeJSON(res, 400, { error: 'session_id is required' });
        return;
      }
      const previous = sessions.get(sessionID) ?? {};
      const next = {
        ...previous,
        ...body,
        session_id: sessionID,
        vendor_session_id: String(body.vendor_session_id ?? previous.vendor_session_id ?? `fake_${sessionID}`).trim(),
      };
      sessions.set(sessionID, next);
      writeJSON(res, 200, {
        session_id: next.session_id,
        vendor_session_id: next.vendor_session_id,
      });
      return;
    }

    const deleteSessionID = sessionPath(url.pathname);
    if (req.method === 'DELETE' && deleteSessionID) {
      sessions.delete(deleteSessionID);
      writeJSON(res, 200, { deleted: true });
      return;
    }

    const resolveSessionID = resolveActionsPath(url.pathname);
    if (req.method === 'POST' && resolveSessionID) {
      writeJSON(res, 200, {
        resolved_count: 0,
        remaining_action_ids: [],
        resume_required: false,
      });
      return;
    }

    if (req.method === 'POST' && url.pathname === '/v1/runs') {
      const body = await readJSON(req);
      const session = sessions.get(String(body.session_id ?? '').trim());
      if (!session) {
        writeJSON(res, 404, { error: 'session not found' });
        return;
      }
      if (!body.run_id) {
        writeJSON(res, 400, { error: 'run_id is required' });
        return;
      }
      setTimeout(() => {
        postWebhook(session, body).catch((error) => {
          console.error(JSON.stringify({ level: 'error', msg: 'fake webhook failed', error: error.message }));
        });
      }, 25);
      writeJSON(res, 202, { accepted: true, run_id: body.run_id });
      return;
    }

    writeJSON(res, 404, { error: 'not found' });
  } catch (error) {
    writeJSON(res, 500, { error: error instanceof Error ? error.message : String(error) });
  }
});

server.listen(port, () => {
  console.log(JSON.stringify({ level: 'info', msg: 'fake managed-agent wrapper listening', port }));
});
