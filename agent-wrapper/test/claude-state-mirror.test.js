import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs/promises';
import path from 'node:path';
import os from 'node:os';
import { ClaudeStateMirror } from '../src/adapters/claude-state-mirror.js';

test('ClaudeStateMirror snapshots local Claude state and propagates deletions on hydrate', async () => {
  const root = await fs.mkdtemp(path.join(os.tmpdir(), 'claude-state-mirror-'));
  const localDir = path.join(root, 'local');
  const mirrorDir = path.join(root, 'mirror');
  const mirror = new ClaudeStateMirror();

  try {
    await fs.mkdir(path.join(localDir, 'nested'), { recursive: true });
    await fs.writeFile(path.join(localDir, 'nested', 'keep.txt'), 'v1\n', 'utf8');
    await fs.writeFile(path.join(localDir, 'remove.txt'), 'remove me\n', 'utf8');

    await mirror.flush({ localDir, mirrorDir });

    await fs.rm(path.join(localDir, 'remove.txt'), { force: true });
    await fs.writeFile(path.join(localDir, 'nested', 'keep.txt'), 'v2\n', 'utf8');
    await fs.writeFile(path.join(localDir, 'added.txt'), 'added\n', 'utf8');

    await mirror.flush({ localDir, mirrorDir });

    await fs.rm(localDir, { recursive: true, force: true });
    await mirror.hydrate({ localDir, mirrorDir });

    await assert.rejects(fs.access(path.join(localDir, 'remove.txt')));
    assert.equal(await fs.readFile(path.join(localDir, 'nested', 'keep.txt'), 'utf8'), 'v2\n');
    assert.equal(await fs.readFile(path.join(localDir, 'added.txt'), 'utf8'), 'added\n');
  } finally {
    await fs.rm(root, { recursive: true, force: true });
  }
});
