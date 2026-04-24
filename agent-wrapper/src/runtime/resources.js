import fs from 'node:fs/promises';
import path from 'node:path';
import { execFile } from 'node:child_process';
import { promisify } from 'node:util';

const execFileAsync = promisify(execFile);

export async function materializeSessionResources(session) {
  for (const resource of session?.resources ?? []) {
    if (!resource || typeof resource !== 'object') {
      continue;
    }
    if (resource.type === 'github_repository') {
      await materializeGitHubRepository(resource);
    }
  }
}

async function materializeGitHubRepository(resource) {
  const repositoryURL = String(resource.url ?? '').trim();
  const mountPath = String(resource.mount_path ?? '').trim();
  if (!repositoryURL || !mountPath || !path.isAbsolute(mountPath)) {
    throw new Error('github_repository resource requires absolute mount_path and url');
  }

  await fs.mkdir(path.dirname(mountPath), { recursive: true });
  if (await isGitRepository(mountPath)) {
    await refreshRepository(repositoryURL, mountPath, resource.checkout ?? null);
    return;
  }

  await ensureEmptyDirectory(mountPath);
  await cloneRepository(repositoryURL, mountPath, resource.checkout ?? null);
}

async function cloneRepository(repositoryURL, mountPath, checkout) {
  const args = ['clone', '--depth=1'];
  const branch = String(checkout?.branch ?? '').trim();
  if (branch) {
    args.push('--branch', branch, '--single-branch');
  }
  args.push(repositoryURL, mountPath);
  await runGit(args);

  const commit = String(checkout?.commit ?? '').trim();
  if (commit) {
    await runGit(['-C', mountPath, 'fetch', '--depth=1', 'origin', commit]);
    await runGit(['-C', mountPath, 'checkout', '--detach', 'FETCH_HEAD']);
  }
}

async function refreshRepository(repositoryURL, mountPath, checkout) {
  await runGit(['-C', mountPath, 'remote', 'set-url', 'origin', repositoryURL]);

  const branch = String(checkout?.branch ?? '').trim();
  if (branch) {
    await runGit(['-C', mountPath, 'fetch', '--depth=1', 'origin', branch]);
    await runGit(['-C', mountPath, 'checkout', '-B', branch, 'FETCH_HEAD']);
    return;
  }

  const commit = String(checkout?.commit ?? '').trim();
  if (commit) {
    await runGit(['-C', mountPath, 'fetch', '--depth=1', 'origin', commit]);
    await runGit(['-C', mountPath, 'checkout', '--detach', 'FETCH_HEAD']);
  }
}

async function ensureEmptyDirectory(targetPath) {
  try {
    const stat = await fs.stat(targetPath);
    if (!stat.isDirectory()) {
      throw new Error(`${targetPath} already exists and is not a directory`);
    }
    const entries = await fs.readdir(targetPath);
    if (entries.length > 0) {
      throw new Error(`${targetPath} already exists and is not an initialized git repository`);
    }
  } catch (error) {
    if (error?.code === 'ENOENT') {
      await fs.mkdir(targetPath, { recursive: true });
      return;
    }
    throw error;
  }
}

async function isGitRepository(targetPath) {
  try {
    const stat = await fs.stat(path.join(targetPath, '.git'));
    return stat.isDirectory() || stat.isFile();
  } catch {
    return false;
  }
}

async function runGit(args) {
  try {
    await execFileAsync('git', args, {
      env: {
        ...process.env,
        GIT_TERMINAL_PROMPT: '0',
      },
    });
  } catch (error) {
    const stderr = String(error?.stderr ?? '').trim();
    const stdout = String(error?.stdout ?? '').trim();
    const detail = stderr || stdout || error?.message || 'git command failed';
    throw new Error(detail);
  }
}
