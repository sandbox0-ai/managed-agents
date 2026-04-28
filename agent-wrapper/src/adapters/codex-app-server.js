import { spawn } from 'node:child_process';
import { EventEmitter } from 'node:events';
import readline from 'node:readline';

function elapsedMsSince(startedAtMs) {
  if (typeof startedAtMs !== 'number' || !Number.isFinite(startedAtMs)) {
    return null;
  }
  return Math.max(0, Date.now() - startedAtMs);
}

function byteLengthForChunk(chunk, encoding) {
  if (chunk == null) {
    return 0;
  }
  if (typeof chunk === 'string') {
    return Buffer.byteLength(chunk, typeof encoding === 'string' ? encoding : 'utf8');
  }
  if (typeof chunk === 'object' && typeof chunk.byteLength === 'number') {
    return chunk.byteLength;
  }
  return Buffer.byteLength(String(chunk));
}

function summarizeJSONRPCMessage(message) {
  const method = typeof message?.method === 'string' ? message.method : null;
  const hasID = Object.prototype.hasOwnProperty.call(message ?? {}, 'id');
  return {
    rpc_kind: method
      ? (hasID ? 'request' : 'notification')
      : (hasID ? 'response' : 'unknown'),
    rpc_method: method,
    rpc_has_id: hasID,
    rpc_has_error: Boolean(message?.error),
  };
}

function summarizeJSONRPCLine(line) {
  const trimmed = String(line ?? '').trim();
  if (trimmed === '') {
    return {
      line_is_json: false,
      rpc_kind: null,
      rpc_method: null,
      rpc_has_id: false,
      rpc_has_error: false,
    };
  }
  try {
    return {
      line_is_json: true,
      ...summarizeJSONRPCMessage(JSON.parse(trimmed)),
    };
  } catch {
    return {
      line_is_json: false,
      rpc_kind: null,
      rpc_method: null,
      rpc_has_id: false,
      rpc_has_error: false,
    };
  }
}

export class CodexAppServerClient extends EventEmitter {
  constructor({
    command = process.env.CODEX_EXECUTABLE ?? 'codex',
    args = ['app-server'],
    cwd = process.cwd(),
    env = process.env,
    requestTimeoutMs = 120000,
    spawnProcess = spawn,
    observe = null,
  } = {}) {
    super();
    this.command = command;
    this.args = args;
    this.cwd = cwd;
    this.env = env;
    this.requestTimeoutMs = requestTimeoutMs;
    this.spawnProcess = spawnProcess;
    this.observe = observe;
    this.nextID = 1;
    this.pending = new Map();
    this.proc = null;
    this.stderrTail = [];
    this.ready = null;
    this.closed = false;
    this.startedAtMs = null;
    this.processStartedAtMs = null;
    this.stdinFirstWriteLogged = false;
    this.stdoutFirstByteLogged = false;
    this.stdoutFirstLineLogged = false;
    this.stderrFirstLineLogged = false;
  }

  async start() {
    if (this.ready) {
      return this.ready;
    }
    this.ready = this.#start();
    return this.ready;
  }

  async #start() {
    this.startedAtMs = Date.now();
    const spawnStartedAtMs = Date.now();
    this.#observe('codex app-server spawn starting', {
      command: this.command,
      args_count: Array.isArray(this.args) ? this.args.length : 0,
      cwd: this.cwd,
    });
    this.proc = this.spawnProcess(this.command, this.args, {
      cwd: this.cwd,
      env: this.env,
      stdio: ['pipe', 'pipe', 'pipe'],
    });
    this.processStartedAtMs = Date.now();
    this.#observe('codex app-server spawn returned', {
      pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
      spawn_elapsed_ms: Math.max(0, this.processStartedAtMs - spawnStartedAtMs),
    });
    this.proc.on('exit', (code, signal) => {
      this.closed = true;
      this.#observe('codex app-server process exited', {
        pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
        code,
        signal,
        process_elapsed_ms: elapsedMsSince(this.processStartedAtMs),
      });
      const error = new Error(`codex app-server exited with code ${code ?? 'null'} signal ${signal ?? 'null'}`);
      this.#rejectAll(error);
      this.emit('close', { code, signal });
    });
    this.proc.on('error', (error) => {
      this.closed = true;
      this.#observe('codex app-server process error', {
        pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
        error: error instanceof Error ? error.message : String(error),
        process_elapsed_ms: elapsedMsSince(this.processStartedAtMs),
      });
      this.#rejectAll(error);
      this.emit('error', error);
    });
    this.proc.stdout.on('data', (chunk) => {
      if (this.stdoutFirstByteLogged) {
        return;
      }
      this.stdoutFirstByteLogged = true;
      this.#observe('codex app-server stdout first byte', {
        pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
        process_elapsed_ms: elapsedMsSince(this.processStartedAtMs),
        chunk_bytes: byteLengthForChunk(chunk),
      });
    });
    readline.createInterface({ input: this.proc.stdout }).on('line', (line) => {
      if (!this.stdoutFirstLineLogged) {
        this.stdoutFirstLineLogged = true;
        this.#observe('codex app-server stdout first line', {
          pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
          process_elapsed_ms: elapsedMsSince(this.processStartedAtMs),
          line_bytes: Buffer.byteLength(String(line ?? ''), 'utf8'),
          ...summarizeJSONRPCLine(line),
        });
      }
      this.#handleLine(line);
    });
    readline.createInterface({ input: this.proc.stderr }).on('line', (line) => {
      this.stderrTail.push(line);
      if (this.stderrTail.length > 80) {
        this.stderrTail.shift();
      }
      if (!this.stderrFirstLineLogged) {
        this.stderrFirstLineLogged = true;
        this.#observe('codex app-server stderr first line', {
          pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
          process_elapsed_ms: elapsedMsSince(this.processStartedAtMs),
          line_bytes: Buffer.byteLength(String(line ?? ''), 'utf8'),
        });
      }
      this.emit('stderr', line);
    });

    const initializeStartedAtMs = Date.now();
    this.#observe('codex app-server initialize starting', {
      elapsed_ms: elapsedMsSince(this.startedAtMs),
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
    this.#observe('codex app-server initialize returned', {
      elapsed_ms: elapsedMsSince(this.startedAtMs),
      request_elapsed_ms: elapsedMsSince(initializeStartedAtMs),
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
    const line = `${JSON.stringify(payload)}\n`;
    if (!this.stdinFirstWriteLogged) {
      this.stdinFirstWriteLogged = true;
      this.#observe('codex app-server stdin first write', {
        pid: Number.isFinite(this.proc?.pid) ? this.proc.pid : null,
        process_elapsed_ms: elapsedMsSince(this.processStartedAtMs),
        input_bytes: Buffer.byteLength(line, 'utf8'),
        ...summarizeJSONRPCMessage(payload),
      });
    }
    this.proc.stdin.write(line);
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

  #observe(msg, fields = {}) {
    const payload = {
      msg,
      fields: {
        elapsed_ms: elapsedMsSince(this.startedAtMs),
        ...fields,
      },
    };
    this.emit('observe', payload);
    if (typeof this.observe !== 'function') {
      return;
    }
    try {
      this.observe(msg, payload.fields);
    } catch {
      // Observability must not break the app-server transport.
    }
  }
}
