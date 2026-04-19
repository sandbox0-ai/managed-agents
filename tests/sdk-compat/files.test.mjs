import test from 'node:test';
import assert from 'node:assert/strict';
import { toFile } from '@anthropic-ai/sdk';
import {
  assertFound,
  collectAsyncIterable,
  sdkClient,
  skipReason,
  suffix,
  uploadTextFile,
} from './helpers.mjs';

test('Files API supports upload, metadata retrieval, download, list, and delete', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const uploaded = await uploadTextFile(client, `sdk-compat-${runID}.txt`, `hello files ${runID}\n`);
  t.after(async () => {
    try {
      await client.beta.files.delete(uploaded.id);
    } catch {}
  });

  assert.equal(uploaded.type, 'file');
  assert.equal(uploaded.filename, `sdk-compat-${runID}.txt`);
  assert.equal(uploaded.mime_type, 'text/plain');
  assert(uploaded.size_bytes > 0);

  const metadata = await client.beta.files.retrieveMetadata(uploaded.id);
  assert.equal(metadata.id, uploaded.id);
  assert.equal(metadata.filename, uploaded.filename);

  const response = await client.beta.files.download(uploaded.id);
  assert.equal(response.headers.get('content-type'), 'text/plain');
  assert.equal(await response.text(), `hello files ${runID}\n`);

  const listed = await collectAsyncIterable(client.beta.files.list({ limit: 10 }), 20);
  assertFound(listed, (item) => item.id === uploaded.id, 'uploaded file');

  const deleted = await client.beta.files.delete(uploaded.id);
  assert.equal(deleted.id, uploaded.id);
  assert.equal(deleted.type, 'file_deleted');

  await assert.rejects(
    () => client.beta.files.retrieveMetadata(uploaded.id),
    /404|not found|file/i,
  );
});

test('Files API supports binary round-trips and pagination cursors', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const binary = Buffer.from([0x89, 0x50, 0x4e, 0x47, 0x00, 0x01, 0x02, 0xff]);
  const uploaded = await client.beta.files.upload({
    file: await toFile(binary, `sdk-compat-${runID}.png`, { type: 'image/png' }),
  });
  t.after(async () => {
    try {
      await client.beta.files.delete(uploaded.id);
    } catch {}
  });

  assert.equal(uploaded.mime_type, 'image/png');
  assert.equal(uploaded.size_bytes, binary.length);

  const response = await client.beta.files.download(uploaded.id);
  assert.equal(response.headers.get('content-type'), 'image/png');
  assert.deepEqual(Buffer.from(await response.arrayBuffer()), binary);

  const firstPage = await client.beta.files.list({ limit: 1 });
  const firstItem = firstPage.data[0];
  assert(firstItem);
  if (firstPage.has_more) {
    const secondPage = await client.beta.files.list({ limit: 1, after_id: firstItem.id });
    assert.notEqual(secondPage.data[0]?.id, firstItem.id);
  }
});
