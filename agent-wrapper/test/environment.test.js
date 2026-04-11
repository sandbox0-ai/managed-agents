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

test('materializeSessionEnvironment activates mounted package-manager artifacts', async () => {
  const root = fs.mkdtempSync(path.join(os.tmpdir(), 'agent-env-'));
  const aptRoot = path.join(root, 'apt', 'rootfs', 'usr', 'bin');
  const cargoRoot = path.join(root, 'cargo', 'root', 'bin');
  const cargoHome = path.join(root, 'cargo', 'home');
  const npmBin = path.join(root, 'npm', 'bin');
  const npmModules = path.join(root, 'npm', 'lib', 'node_modules');
  const pipBin = path.join(root, 'pip', 'venv', 'bin');
  fs.mkdirSync(aptRoot, { recursive: true });
  fs.mkdirSync(cargoRoot, { recursive: true });
  fs.mkdirSync(cargoHome, { recursive: true });
  fs.mkdirSync(npmBin, { recursive: true });
  fs.mkdirSync(npmModules, { recursive: true });
  fs.mkdirSync(pipBin, { recursive: true });

  const session = {
    environment: {
      config: {
        packages: {
          apt: ['ripgrep'],
          cargo: ['bat'],
          npm: ['typescript'],
          pip: ['ruff==0.8.0'],
        },
      },
    },
    environment_artifact: {
      compatibility: {
        os: process.platform,
        arch: process.arch === 'x64' ? 'amd64' : process.arch,
      },
      assets: {
        apt: { mount_path: path.join(root, 'apt') },
        cargo: { mount_path: path.join(root, 'cargo') },
        gem: { mount_path: path.join(root, 'gem') },
        go: { mount_path: path.join(root, 'go') },
        npm: { mount_path: path.join(root, 'npm') },
        pip: { mount_path: path.join(root, 'pip') },
      },
    },
    engine: {
      env: {
        PATH: '/usr/bin',
      },
    },
  };

  await materializeSessionEnvironment(session);

  assert.equal(session.engine.env.CARGO_HOME, path.join(root, 'cargo', 'home'));
  assert.equal(session.engine.env.NODE_PATH, path.join(root, 'npm', 'lib', 'node_modules'));
  assert.equal(session.engine.env.VIRTUAL_ENV, path.join(root, 'pip', 'venv'));
  assert.equal(
    session.engine.env.PATH,
    [
      path.join(root, 'apt', 'rootfs', 'usr', 'bin'),
      path.join(root, 'cargo', 'root', 'bin'),
      path.join(root, 'npm', 'bin'),
      path.join(root, 'pip', 'venv', 'bin'),
      '/usr/bin',
    ].join(':'),
  );
});

test('materializeSessionEnvironment rejects incompatible artifacts', async () => {
  const session = {
    environment: {
      config: {
        packages: {
          pip: ['ruff'],
        },
      },
    },
    environment_artifact: {
      compatibility: {
        os: process.platform === 'linux' ? 'darwin' : 'linux',
      },
      assets: {
        pip: { mount_path: '/tmp/managed-env/pip' },
      },
    },
    engine: { env: {} },
  };

  await assert.rejects(
    materializeSessionEnvironment(session),
    /environment artifact requires/,
  );
});

test('materializeSessionEnvironment rejects missing mount metadata', async () => {
  const session = {
    environment: {
      config: {
        packages: {
          pip: ['ruff'],
        },
      },
    },
    environment_artifact: {
      compatibility: {
        os: process.platform,
        arch: process.arch === 'x64' ? 'amd64' : process.arch,
      },
      assets: {},
    },
    engine: { env: {} },
  };

  await assert.rejects(
    materializeSessionEnvironment(session),
    /environment artifact is missing pip mount metadata/,
  );
});
