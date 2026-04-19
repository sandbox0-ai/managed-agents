import test from 'node:test';
import {
  createLiveSessionFixture,
  liveCodex,
  liveSkipReason,
  runLiveTurn,
  sdkClient,
} from './helpers.mjs';

const codexConfig = {
  engine: 'codex',
  ...liveCodex,
};

test('live codex engine runs a real managed-agent turn through MiniMax', { skip: liveSkipReason(codexConfig) }, async (t) => {
  const client = sdkClient();
  const { session } = await createLiveSessionFixture(client, t, codexConfig);
  await runLiveTurn(client, session.id, 'CODEX_ENGINE_OK', { timeoutMs: 360_000 });
});
