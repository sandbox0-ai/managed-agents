import test from 'node:test';
import assert from 'node:assert/strict';
import fs from 'node:fs';
import os from 'node:os';
import path from 'node:path';
import { materializeSessionEnvironment, normalizePackages } from '../src/runtime/environment.js';

test('normalizePackages keeps known package managers only', () => {
  assert.deepEqual(normalizePackages({ pip: ['ruff', '', null], npm: ['typescript'], unknown: ['x'] }), {
    apt: [],
    cargo: [],
    gem: [],
    go: [],
    npm: ['typescript'],
    pip: ['ruff'],
  });
});

test('materializeSessionEnvironment installs packages once per environment digest', async () => {
  const stateDir = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-env-'));
  const calls = [];
  const session = {
    environment_id: 'env_123',
    environment: {
      config: {
        packages: {
          apt: ['ripgrep'],
          pip: ['ruff==0.8.0'],
          npm: ['typescript'],
          go: ['golang.org/x/tools/cmd/stringer'],
        },
      },
    },
  };
  const runCommand = async (command, args) => calls.push([command, args]);

  await materializeSessionEnvironment(session, { stateDir, runCommand });
  await materializeSessionEnvironment(session, { stateDir, runCommand });

  assert.deepEqual(calls, [
    ['apt-get', ['update']],
    ['apt-get', ['install', '-y', '--no-install-recommends', 'ripgrep', 'python3', 'python3-pip', 'golang-go']],
    ['python3', ['-m', 'pip', 'install', '--break-system-packages', 'ruff==0.8.0']],
    ['npm', ['install', '-g', 'typescript']],
    ['go', ['install', 'golang.org/x/tools/cmd/stringer@latest']],
  ]);
});
