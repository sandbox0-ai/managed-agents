import test from 'node:test';
import {
  createLiveSessionFixture,
  liveClaude,
  liveSkipReason,
  runLiveTurn,
  sdkClient,
} from './helpers.mjs';

const claudeConfig = {
  engine: 'claude',
  ...liveClaude,
};

test('live claude engine runs a real managed-agent turn', { skip: liveSkipReason(claudeConfig) }, async (t) => {
  const client = sdkClient();
  const { session } = await createLiveSessionFixture(client, t, claudeConfig);
  await runLiveTurn(client, session.id, 'CLAUDE_ENGINE_OK');
});
