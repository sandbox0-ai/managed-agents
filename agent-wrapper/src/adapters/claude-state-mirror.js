import fsSync from 'node:fs';
import fs from 'node:fs/promises';
import path from 'node:path';

const CURRENT_SNAPSHOT_FILE = 'current.json';
const SNAPSHOTS_DIR = 'snapshots';

export class ClaudeStateMirror {
  constructor() {
    this.locks = new Map();
  }

  async hydrate(paths) {
    const normalized = normalizeMirrorPaths(paths);
    if (!normalized) {
      return { restored: false };
    }
    return this.#withLock(normalized.mirrorDir, async () => {
      const current = await readCurrentSnapshot(normalized.mirrorDir);
      if (!current?.generation) {
        return { restored: false };
      }
      const snapshotDir = path.join(normalized.mirrorDir, SNAPSHOTS_DIR, current.generation);
      if (!(await pathExists(snapshotDir))) {
        return { restored: false };
      }
      await fs.rm(normalized.localDir, { recursive: true, force: true });
      await fs.mkdir(normalized.localDir, { recursive: true });
      await copyDirectoryContents(snapshotDir, normalized.localDir);
      return { restored: true, generation: current.generation };
    });
  }

  async flush(paths) {
    const normalized = normalizeMirrorPaths(paths);
    if (!normalized) {
      return { flushed: false };
    }
    return this.#withLock(normalized.mirrorDir, async () => {
      const current = await readCurrentSnapshot(normalized.mirrorDir);
      const snapshotsRoot = path.join(normalized.mirrorDir, SNAPSHOTS_DIR);
      const generation = `gen-${Date.now()}-${Math.random().toString(36).slice(2, 10)}`;
      const stagingDir = path.join(snapshotsRoot, `${generation}.tmp`);
      const snapshotDir = path.join(snapshotsRoot, generation);

      await fs.mkdir(snapshotsRoot, { recursive: true });
      await fs.rm(stagingDir, { recursive: true, force: true });
      await fs.mkdir(stagingDir, { recursive: true });
      if (await pathExists(normalized.localDir)) {
        await copyDirectoryContents(normalized.localDir, stagingDir);
      }
      await fs.writeFile(path.join(stagingDir, 'manifest.json'), `${JSON.stringify({
        generation,
        local_dir: normalized.localDir,
        mirror_dir: normalized.mirrorDir,
        updated_at: new Date().toISOString(),
      }, null, 2)}\n`, 'utf8');
      await fs.rename(stagingDir, snapshotDir);
      await writeCurrentSnapshot(normalized.mirrorDir, { generation, updated_at: new Date().toISOString() });
      if (current?.generation && current.generation !== generation) {
        await fs.rm(path.join(snapshotsRoot, current.generation), { recursive: true, force: true });
      }
      return { flushed: true, generation };
    });
  }

  async #withLock(mirrorDir, operation) {
    const previous = this.locks.get(mirrorDir) ?? Promise.resolve();
    const next = previous.catch(() => {}).then(operation);
    const tracked = next.finally(() => {
      if (this.locks.get(mirrorDir) === tracked) {
        this.locks.delete(mirrorDir);
      }
    });
    this.locks.set(mirrorDir, tracked);
    return next;
  }
}

export function claudeMirrorDir(env, agentWrapperStateDir) {
  const explicit = normalizePath(env.AGENT_WRAPPER_CLAUDE_MIRROR_DIR);
  if (explicit) {
    return explicit;
  }
  const stateDir = normalizePath(agentWrapperStateDir);
  if (!stateDir) {
    return null;
  }
  const requiredBaseDir = stateDir.startsWith(`/workspace${path.sep}`)
    ? '/workspace'
    : path.dirname(stateDir);
  if (!fsSync.existsSync(requiredBaseDir)) {
    return null;
  }
  return path.join(stateDir, 'claude-mirror');
}

function normalizeMirrorPaths(paths) {
  const localDir = normalizePath(paths?.localDir);
  const mirrorDir = normalizePath(paths?.mirrorDir);
  if (!localDir || !mirrorDir) {
    return null;
  }
  if (localDir === mirrorDir || localDir.startsWith(`${mirrorDir}${path.sep}`) || mirrorDir.startsWith(`${localDir}${path.sep}`)) {
    return null;
  }
  return { localDir, mirrorDir };
}

function normalizePath(value) {
  const text = String(value ?? '').trim();
  return text ? path.resolve(text) : null;
}

async function readCurrentSnapshot(mirrorDir) {
  const currentFile = path.join(mirrorDir, CURRENT_SNAPSHOT_FILE);
  if (!(await pathExists(currentFile))) {
    return null;
  }
  const raw = await fs.readFile(currentFile, 'utf8');
  if (!raw.trim()) {
    return null;
  }
  return JSON.parse(raw);
}

async function writeCurrentSnapshot(mirrorDir, snapshot) {
  await fs.mkdir(mirrorDir, { recursive: true });
  const currentFile = path.join(mirrorDir, CURRENT_SNAPSHOT_FILE);
  const stagingFile = `${currentFile}.tmp`;
  await fs.writeFile(stagingFile, `${JSON.stringify(snapshot, null, 2)}\n`, 'utf8');
  await fs.rename(stagingFile, currentFile);
}

async function copyDirectoryContents(sourceDir, targetDir) {
  const entries = await fs.readdir(sourceDir, { withFileTypes: true });
  await Promise.all(entries.map(async (entry) => {
    const sourcePath = path.join(sourceDir, entry.name);
    const targetPath = path.join(targetDir, entry.name);
    if (entry.isDirectory()) {
      await fs.mkdir(targetPath, { recursive: true });
      await copyDirectoryContents(sourcePath, targetPath);
      return;
    }
    if (entry.isSymbolicLink()) {
      const linkTarget = await fs.readlink(sourcePath);
      await fs.symlink(linkTarget, targetPath);
      return;
    }
    await fs.copyFile(sourcePath, targetPath);
  }));
}

async function pathExists(target) {
  try {
    await fs.access(target);
    return true;
  } catch {
    return false;
  }
}
