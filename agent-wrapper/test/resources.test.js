import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { execFileSync } from 'node:child_process';
import { materializeSessionResources } from '../src/runtime/resources.js';

test('materializeSessionResources clones github_repository resources into mount_path', async () => {
  const baseDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-wrapper-resource-'));
  const repoDir = path.join(baseDir, 'origin');
  const cloneDir = path.join(baseDir, 'workspace', 'repo');

  fs.mkdirSync(repoDir, { recursive: true });
  execFileSync('git', ['init', '--initial-branch=main'], { cwd: repoDir });
  execFileSync('git', ['config', 'user.email', 'test@example.com'], { cwd: repoDir });
  execFileSync('git', ['config', 'user.name', 'Test User'], { cwd: repoDir });
  fs.writeFileSync(path.join(repoDir, 'README.md'), 'hello managed agent\n', 'utf8');
  execFileSync('git', ['add', 'README.md'], { cwd: repoDir });
  execFileSync('git', ['commit', '-m', 'init'], { cwd: repoDir });

  await materializeSessionResources({
    resources: [{
      type: 'github_repository',
      url: repoDir,
      mount_path: cloneDir,
      checkout: { branch: 'main' },
    }],
  });

  assert.equal(fs.readFileSync(path.join(cloneDir, 'README.md'), 'utf8'), 'hello managed agent\n');
});
