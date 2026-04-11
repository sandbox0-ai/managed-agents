import crypto from 'node:crypto';
import fs from 'node:fs/promises';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);

export async function materializeSessionEnvironment(session, options = {}) {
  const packages = normalizePackages(session?.environment?.config?.packages);
  if (!hasPackages(packages)) {
    return;
  }

  const stateDir = options.stateDir ?? process.env.AGENT_WRAPPER_STATE_DIR ?? '/var/lib/agent-wrapper';
  const run = options.runCommand ?? runCommand;
  const environmentID = safeEnvironmentID(session?.environment_id ?? session?.environment?.id ?? 'default');
  const markerDir = path.join(stateDir, 'environment');
  const markerPath = path.join(markerDir, `${environmentID}.sha256`);
  const digest = hashPackages(packages);
  if (await markerMatches(markerPath, digest)) {
    return;
  }

  await installPackages(packages, run);
  await fs.mkdir(markerDir, { recursive: true });
  await fs.writeFile(markerPath, digest, 'utf8');
}

export function normalizePackages(raw) {
  const source = raw && typeof raw === 'object' ? raw : {};
  return {
    apt: stringList(source.apt),
    cargo: stringList(source.cargo),
    gem: stringList(source.gem),
    go: stringList(source.go),
    npm: stringList(source.npm),
    pip: stringList(source.pip),
  };
}

async function installPackages(packages, run) {
  const aptPackages = [...packages.apt];
  if (packages.pip.length > 0) {
    aptPackages.push('python3', 'python3-pip');
  }
  if (packages.gem.length > 0) {
    aptPackages.push('ruby-full');
  }
  if (packages.cargo.length > 0) {
    aptPackages.push('cargo');
  }
  if (packages.go.length > 0) {
    aptPackages.push('golang-go');
  }
  const uniqueAptPackages = unique(aptPackages);
  if (uniqueAptPackages.length > 0) {
    await run('apt-get', ['update']);
    await run('apt-get', ['install', '-y', '--no-install-recommends', ...uniqueAptPackages]);
  }
  if (packages.pip.length > 0) {
    await run('python3', ['-m', 'pip', 'install', '--break-system-packages', ...packages.pip]);
  }
  if (packages.npm.length > 0) {
    await run('npm', ['install', '-g', ...packages.npm]);
  }
  if (packages.gem.length > 0) {
    await run('gem', ['install', ...packages.gem]);
  }
  for (const item of packages.cargo) {
    await run('cargo', ['install', item]);
  }
  for (const item of packages.go) {
    await run('go', ['install', normalizeGoInstallTarget(item)]);
  }
}

async function markerMatches(markerPath, digest) {
  try {
    const existing = await fs.readFile(markerPath, 'utf8');
    return existing.trim() === digest;
  } catch (error) {
    if (error?.code === 'ENOENT') {
      return false;
    }
    throw error;
  }
}

function hashPackages(packages) {
  return crypto.createHash('sha256').update(JSON.stringify(packages)).digest('hex');
}

function stringList(value) {
  if (!Array.isArray(value)) {
    return [];
  }
  return unique(value.map((item) => String(item ?? '').trim()).filter(Boolean));
}

function hasPackages(packages) {
  return Object.values(packages).some((items) => items.length > 0);
}

function unique(items) {
  return Array.from(new Set(items));
}

function safeEnvironmentID(value) {
  const normalized = String(value ?? '').trim().toLowerCase().replace(/[^a-z0-9_-]+/g, '-').replace(/^-+|-+$/g, '');
  return normalized || 'default';
}

function normalizeGoInstallTarget(value) {
  const target = String(value ?? '').trim();
  if (target.includes('@')) {
    return target;
  }
  return `${target}@latest`;
}

async function runCommand(command, args) {
  try {
    await execFileAsync(command, args, {
      env: process.env,
      maxBuffer: 10 * 1024 * 1024,
    });
  } catch (error) {
    const stderr = String(error?.stderr ?? '').trim();
    const stdout = String(error?.stdout ?? '').trim();
    const detail = stderr || stdout || error?.message || `${command} failed`;
    throw new Error(detail);
  }
}
