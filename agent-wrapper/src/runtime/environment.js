import fs from 'node:fs/promises';
import path from 'node:path';

const managerOrder = ['apt', 'cargo', 'gem', 'go', 'npm', 'pip'];

export async function materializeSessionEnvironment(session) {
  return activateSessionEnvironment(session);
}

export async function activateSessionEnvironment(session) {
  const packages = normalizePackages(session?.environment?.config?.packages);
  if (!hasPackages(packages)) {
    return session;
  }

  const artifact = normalizeEnvironmentArtifact(session?.environment_artifact);
  if (!artifact) {
    throw new Error('environment artifact is required when packages are configured');
  }
  assertCompatibility(artifact.compatibility);

  const existingEngine = session?.engine && typeof session.engine === 'object' ? session.engine : {};
  const existingEnv = existingEngine.env && typeof existingEngine.env === 'object' ? existingEngine.env : {};
  const nextEnv = { ...existingEnv };
  const pathEntries = [];

  for (const manager of managerOrder) {
    if (packages[manager].length === 0) {
      continue;
    }
    const asset = artifact.assets[manager];
    if (!asset?.mountPath) {
      throw new Error(`environment artifact is missing ${manager} mount metadata`);
    }
    await activateManager(manager, asset.mountPath, nextEnv, pathEntries);
  }

  nextEnv.PATH = joinPathEntries(pathEntries, existingEnv.PATH ?? process.env.PATH ?? '');
  session.engine = {
    ...existingEngine,
    env: nextEnv,
  };
  return session;
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

function normalizeEnvironmentArtifact(raw) {
  if (!raw || typeof raw !== 'object') {
    return null;
  }
  const assets = {};
  for (const manager of managerOrder) {
    const asset = raw.assets?.[manager];
    assets[manager] = {
      volumeID: stringValue(asset?.volume_id),
      mountPath: stringValue(asset?.mount_path),
    };
  }
  return {
    compatibility: raw.compatibility && typeof raw.compatibility === 'object' ? raw.compatibility : {},
    assets,
  };
}

async function activateManager(manager, mountPath, env, pathEntries) {
  switch (manager) {
    case 'apt':
      await activateApt(mountPath, env, pathEntries);
      return;
    case 'cargo':
      await activateCargo(mountPath, env, pathEntries);
      return;
    case 'gem':
      await activateGem(mountPath, env, pathEntries);
      return;
    case 'go':
      await activateGo(mountPath, env, pathEntries);
      return;
    case 'npm':
      await activateNPM(mountPath, env, pathEntries);
      return;
    case 'pip':
      await activatePip(mountPath, env, pathEntries);
      return;
    default:
      throw new Error(`unsupported environment package manager: ${manager}`);
  }
}

async function activateApt(mountPath, env, pathEntries) {
  const rootfs = path.join(mountPath, 'rootfs');
  await requirePath(rootfs, 'apt rootfs');
  for (const dir of ['usr/local/sbin', 'usr/local/bin', 'usr/sbin', 'usr/bin', 'sbin', 'bin']) {
    await pushDirIfExists(pathEntries, path.join(rootfs, dir));
  }
  const libDirs = [
    'usr/local/lib',
    'usr/lib',
    'usr/lib/x86_64-linux-gnu',
    'usr/lib/aarch64-linux-gnu',
    'lib',
    'lib/x86_64-linux-gnu',
    'lib/aarch64-linux-gnu',
  ];
  for (const dir of libDirs) {
    const absolute = path.join(rootfs, dir);
    if (await pathExists(absolute)) {
      appendDelimitedEnv(env, 'LD_LIBRARY_PATH', absolute);
    }
  }
  for (const dir of ['usr/lib/pkgconfig', 'usr/lib/x86_64-linux-gnu/pkgconfig', 'usr/lib/aarch64-linux-gnu/pkgconfig']) {
    const absolute = path.join(rootfs, dir);
    if (await pathExists(absolute)) {
      appendDelimitedEnv(env, 'PKG_CONFIG_PATH', absolute);
    }
  }
}

async function activateCargo(mountPath, env, pathEntries) {
  const root = path.join(mountPath, 'root');
  const home = path.join(mountPath, 'home');
  await requirePath(root, 'cargo root');
  env.CARGO_HOME = home;
  await pushDirIfExists(pathEntries, path.join(root, 'bin'));
}

async function activateGem(mountPath, env, pathEntries) {
  const home = path.join(mountPath, 'home');
  const bin = path.join(mountPath, 'bin');
  await requirePath(home, 'gem home');
  env.GEM_HOME = home;
  env.GEM_PATH = home;
  await pushDirIfExists(pathEntries, bin);
}

async function activateGo(mountPath, env, pathEntries) {
  const bin = path.join(mountPath, 'bin');
  await requirePath(mountPath, 'go mount');
  env.GOBIN = bin;
  await pushDirIfExists(pathEntries, bin);
}

async function activateNPM(mountPath, env, pathEntries) {
  await requirePath(mountPath, 'npm mount');
  await pushDirIfExists(pathEntries, path.join(mountPath, 'bin'));
  const nodePath = path.join(mountPath, 'lib', 'node_modules');
  if (await pathExists(nodePath)) {
    appendDelimitedEnv(env, 'NODE_PATH', nodePath);
  }
}

async function activatePip(mountPath, env, pathEntries) {
  const venv = path.join(mountPath, 'venv');
  await requirePath(venv, 'pip virtualenv');
  env.VIRTUAL_ENV = venv;
  await pushDirIfExists(pathEntries, path.join(venv, 'bin'));
}

function assertCompatibility(compatibility) {
  if (!compatibility || typeof compatibility !== 'object') {
    return;
  }
  const expectedOS = stringValue(compatibility.os);
  if (expectedOS && expectedOS !== process.platform) {
    throw new Error(`environment artifact requires ${expectedOS}, current runtime is ${process.platform}`);
  }
  const expectedArch = stringValue(compatibility.arch);
  const currentArch = currentGoArch();
  if (expectedArch && expectedArch !== currentArch) {
    throw new Error(`environment artifact requires ${expectedArch}, current runtime is ${currentArch}`);
  }
}

function currentGoArch() {
  switch (process.arch) {
    case 'x64':
      return 'amd64';
    case 'arm64':
      return 'arm64';
    default:
      return process.arch;
  }
}

async function requirePath(target, label) {
  if (!(await pathExists(target))) {
    throw new Error(`${label} is missing at ${target}`);
  }
}

async function pushDirIfExists(entries, target) {
  if (await pathExists(target)) {
    entries.push(target);
  }
}

async function pathExists(target) {
  try {
    await fs.access(target);
    return true;
  } catch (error) {
    if (error?.code === 'ENOENT') {
      return false;
    }
    throw error;
  }
}

function appendDelimitedEnv(env, key, value) {
  const trimmed = String(value ?? '').trim();
  if (!trimmed) {
    return;
  }
  const current = String(env[key] ?? '').trim();
  env[key] = current ? `${trimmed}:${current}` : trimmed;
}

function joinPathEntries(entries, basePath) {
  const seen = new Set();
  const ordered = [];
  for (const entry of [...entries, ...String(basePath ?? '').split(':')]) {
    const trimmed = String(entry ?? '').trim();
    if (!trimmed || seen.has(trimmed)) {
      continue;
    }
    seen.add(trimmed);
    ordered.push(trimmed);
  }
  return ordered.join(':');
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

function stringValue(value) {
  return String(value ?? '').trim();
}
