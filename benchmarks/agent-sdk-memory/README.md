# Agent SDK Memory Benchmark

This benchmark compares the local memory footprint of Claude Agent SDK and Codex SDK integration paths used by managed agent runtimes.

It is intentionally a cold-start idle benchmark. It does not call model APIs, send prompts, or measure token-dependent workload peaks. Use it to measure the fixed local process cost before adding workload profiles.

## What It Measures

- Node.js baseline RSS
- Import-only RSS for `@anthropic-ai/claude-agent-sdk`
- Import-only RSS for `@openai/codex-sdk`
- Idle RSS for the Codex `exec --experimental-json` process
- Idle RSS for the Codex `app-server` process
- Idle RSS for the bundled Claude CLI process used by the Claude Agent SDK
- Idle RSS for Sandbox0's own `agent-wrapper` HTTP runtime process

For child processes, the script records both direct RSS and process-tree RSS. Process-tree RSS is the primary value to compare because SDKs may spawn helper processes on some platforms. After the warmup delay, the script samples a short stability window and records the highest RSS observed in that window to avoid startup race artifacts.

## Usage

```bash
cd benchmarks/agent-sdk-memory
npm install

cd ../../agent-wrapper
npm install

cd ../benchmarks/agent-sdk-memory
npm run bench
```

Useful options:

```bash
npm run bench -- --samples 10 --warmup-ms 1500 --stability-probes 8 --output results/linux-arm64.json
```

The script prints a Markdown table and writes a JSON report with host metadata, package versions, raw samples, and summary statistics.

## Notes

- The benchmark currently supports macOS and Linux hosts with `ps` and `pgrep` available.
- Child processes run with temporary HOME-like directories so local user agent configuration is not part of the benchmark.
- The Claude CLI measurement uses `ANTHROPIC_API_KEY=dummy` unless an explicit value is already present. The process is sampled before any prompt is sent.
- The Sandbox0 wrapper measurement starts `agent-wrapper/src/index.js` on a temporary loopback port, waits for `/healthz`, and samples the idle HTTP process before any session or run is created.
- Real production memory depends on prompt size, repository size, tool output, MCP servers, shell commands, and session lifetime.
