import test from 'node:test';
import assert from 'node:assert/strict';
import { createServer } from 'node:http';
import { ProcdWebhookClient } from '../src/runtime/callbacks.js';

function listen(server) {
  return new Promise((resolve) => server.listen(0, resolve));
}

test('managed-agent webhook delivery retries transient callback failures', async () => {
  let requests = 0;
  const server = createServer((req, res) => {
    requests += 1;
    if (requests === 1) {
      res.writeHead(503, { 'content-type': 'application/json' });
      res.end('{"error":"warming up"}');
      return;
    }
    res.writeHead(200, { 'content-type': 'application/json' });
    res.end('{"ok":true}');
  });
  await listen(server);
  const port = server.address().port;
  const client = new ProcdWebhookClient({ controlToken: 'token' });

  const result = await client.sendManagedAgentWebhook({
    session_id: 'sesn_1',
    sandbox_id: 'sbx_1',
  }, {
    events: [{ type: 'agent.message' }],
  }, `http://127.0.0.1:${port}/webhook`);

  assert.deepEqual(result, { ok: true });
  assert.equal(requests, 2);
  server.close();
});

test('managed-agent webhook delivery does not retry client errors', async () => {
  let requests = 0;
  const server = createServer((_req, res) => {
    requests += 1;
    res.writeHead(400, { 'content-type': 'application/json' });
    res.end('{"error":"bad request"}');
  });
  await listen(server);
  const port = server.address().port;
  const client = new ProcdWebhookClient({ controlToken: 'token' });

  await assert.rejects(
    () => client.sendManagedAgentWebhook({
      session_id: 'sesn_1',
      sandbox_id: 'sbx_1',
    }, {
      events: [{ type: 'agent.message' }],
    }, `http://127.0.0.1:${port}/webhook`),
    /failed with 400/,
  );
  assert.equal(requests, 1);
  server.close();
});
