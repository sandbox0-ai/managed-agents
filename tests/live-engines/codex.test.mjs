import test from 'node:test';
import {
  createLiveSessionFixture,
  isRetryableLiveEngineError,
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
  const attempts = 2;
  let lastError;

  for (let attempt = 1; attempt <= attempts; attempt += 1) {
    const { cleanup, session } = await createLiveSessionFixture(client, t, codexConfig);
    try {
      if (attempt > 1) {
        t.diagnostic(`retrying Codex live turn after retryable upstream failure (attempt ${attempt}/${attempts})`);
      }
      await runLiveTurn(client, session.id, 'CODEX_ENGINE_OK', {
        timeoutMs: 360_000,
        requireUsage: false,
      });
      return;
    } catch (error) {
      lastError = error;
      await cleanup.run();
      if (attempt >= attempts || !isRetryableLiveEngineError(error)) {
        throw error;
      }
    }
  }

  throw lastError;
});
