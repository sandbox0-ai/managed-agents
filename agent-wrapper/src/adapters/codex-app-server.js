import { spawn } from 'node:child_process';
import { EventEmitter } from 'node:events';
import readline from 'node:readline';

export class CodexAppServerClient extends EventEmitter {
  constructor({
    command = process.env.CODEX_EXECUTABLE ?? 'codex',
    args = ['app-server', '--listen', 'stdio://'],
    cwd = process.cwd(),
    env = process.env,
    requestTimeoutMs = 120000,
    spawnProcess = spawn,
  } = {}) {
    super();
    this.command = command;
    this.args = args;
    this.cwd = cwd;
    this.env = env;
    this.requestTimeoutMs = requestTimeoutMs;
    this.spawnProcess = spawnProcess;
    this.nextID = 1;
    this.pending = new Map();
    this.proc = null;
    this.stderrTail = [];
    this.ready = null;
    this.closed = false;
  }

  async start() {
    if (this.ready) {
      return this.ready;
    }
    this.ready = this.#start();
    return this.ready;
  }

  async #start() {
    this.proc = this.spawnProcess(this.command, this.args, {
      cwd: this.cwd,
      env: this.env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.proc.on('exit', (code, signal) => {
      this.closed = true;
      const error = new Error(`codex app-server exited with code ${code ?? 'null'} signal ${signal ?? 'null'}`);
      this.#rejectAll(error);
      this.emit('close', { code, signal });
    });
    this.proc.on('error', (error) => {
      this.closed = true;
      this.#rejectAll(error);
      this.emit('error', error);
    });
    readline.createInterface({ input: this.proc.stdout }).on('line', (line) => this.#handleLine(line));
    readline.createInterface({ input: this.proc.stderr }).on('line', (line) => {
      this.stderrTail.push(line);
      if (this.stderrTail.length > 80) {
        this.stderrTail.shift();
      }
      this.emit('stderr', line);
    });

    await this.request('initialize', {
      clientInfo: {
        name: 'sandbox0_managed_agents',
        title: 'Sandbox0 Managed Agents',
        version: '1.0.0',
      },
      capabilities: {
        experimentalApi: true,
      },
    });
    this.notify('initialized', {});
  }

  request(method, params = {}) {
    if (this.closed) {
      return Promise.reject(new Error('codex app-server is closed'));
    }
    const id = this.nextID++;
    const payload = { id, method, params };
    const promise = new Promise((resolve, reject) => {
      const timeout = setTimeout(() => {
        this.pending.delete(id);
        reject(new Error(`codex app-server request timed out: ${method}`));
      }, this.requestTimeoutMs);
      this.pending.set(id, { method, resolve, reject, timeout });
    });
    this.#write(payload);
    return promise;
  }

  notify(method, params = {}) {
    this.#write({ method, params });
  }

  respond(id, result) {
    this.#write({ id, result });
  }

  respondError(id, error) {
    const message = error instanceof Error ? error.message : String(error ?? 'request failed');
    this.#write({ id, error: { code: -32000, message } });
  }

  close() {
    this.closed = true;
    if (this.proc && !this.proc.killed) {
      this.proc.kill('SIGTERM');
    }
    this.#rejectAll(new Error('codex app-server client closed'));
  }

  #write(payload) {
    if (!this.proc?.stdin || this.closed) {
      throw new Error('codex app-server stdin is not available');
    }
    this.proc.stdin.write(`${JSON.stringify(payload)}\n`);
  }

  #handleLine(line) {
    const trimmed = String(line ?? '').trim();
    if (!trimmed) {
      return;
    }
    let message;
    try {
      message = JSON.parse(trimmed);
    } catch (error) {
      this.emit('error', new Error(`invalid codex app-server JSON: ${trimmed}`));
      return;
    }
    if (Object.prototype.hasOwnProperty.call(message, 'id') && !message.method) {
      const pending = this.pending.get(message.id);
      if (!pending) {
        return;
      }
      clearTimeout(pending.timeout);
      this.pending.delete(message.id);
      if (message.error) {
        const detail = message.error?.message ?? JSON.stringify(message.error);
        pending.reject(new Error(`${pending.method} failed: ${detail}`));
        return;
      }
      pending.resolve(message.result ?? {});
      return;
    }
    if (Object.prototype.hasOwnProperty.call(message, 'id') && message.method) {
      this.emit('serverRequest', message);
      return;
    }
    if (message.method) {
      this.emit('notification', message);
    }
  }

  #rejectAll(error) {
    for (const [id, pending] of this.pending.entries()) {
      clearTimeout(pending.timeout);
      pending.reject(error);
      this.pending.delete(id);
    }
  }
}
