#!/usr/bin/env node
import { spawn, spawnSync } from "node:child_process";
import { createRequire } from "node:module";
import fs from "node:fs";
import os from "node:os";
import path from "node:path";
import process from "node:process";
import { fileURLToPath } from "node:url";

const require = createRequire(import.meta.url);
const benchmarkRoot = path.dirname(fileURLToPath(import.meta.url));
const repoRoot = path.resolve(benchmarkRoot, "../..");

const defaultOptions = {
  samples: 5,
  warmupMs: 1200,
  stabilityProbes: 5,
  stabilityProbeIntervalMs: 150,
  output: path.join(benchmarkRoot, "results", "latest.json"),
  childTimeoutMs: 5000,
};

const CODEX_PLATFORM_PACKAGE_BY_TARGET = {
  "x86_64-unknown-linux-musl": "@openai/codex-linux-x64",
  "aarch64-unknown-linux-musl": "@openai/codex-linux-arm64",
  "x86_64-apple-darwin": "@openai/codex-darwin-x64",
  "aarch64-apple-darwin": "@openai/codex-darwin-arm64",
  "x86_64-pc-windows-msvc": "@openai/codex-win32-x64",
  "aarch64-pc-windows-msvc": "@openai/codex-win32-arm64",
};

function parseArgs(argv) {
  const options = { ...defaultOptions };
  for (let i = 0; i < argv.length; i += 1) {
    const arg = argv[i];
    if (arg === "--samples") {
      options.samples = parsePositiveInt(argv[++i], "--samples");
    } else if (arg === "--warmup-ms") {
      options.warmupMs = parsePositiveInt(argv[++i], "--warmup-ms");
    } else if (arg === "--stability-probes") {
      options.stabilityProbes = parsePositiveInt(argv[++i], "--stability-probes");
    } else if (arg === "--stability-probe-interval-ms") {
      options.stabilityProbeIntervalMs = parsePositiveInt(argv[++i], "--stability-probe-interval-ms");
    } else if (arg === "--output") {
      const value = argv[++i];
      if (!value) throw new Error("--output requires a path");
      options.output = path.resolve(process.cwd(), value);
    } else if (arg === "--child-timeout-ms") {
      options.childTimeoutMs = parsePositiveInt(argv[++i], "--child-timeout-ms");
    } else if (arg === "--help" || arg === "-h") {
      printHelp();
      process.exit(0);
    } else {
      throw new Error(`Unknown argument: ${arg}`);
    }
  }
  return options;
}

function parsePositiveInt(value, flag) {
  const parsed = Number.parseInt(value, 10);
  if (!Number.isInteger(parsed) || parsed <= 0) {
    throw new Error(`${flag} must be a positive integer`);
  }
  return parsed;
}

function printHelp() {
  console.log(`Usage: npm run bench -- [options]\n\nOptions:\n  --samples N                      Number of samples per benchmark. Default: ${defaultOptions.samples}\n  --warmup-ms N                    Milliseconds to wait before sampling child process RSS. Default: ${defaultOptions.warmupMs}\n  --stability-probes N             RSS probes after warmup. Default: ${defaultOptions.stabilityProbes}\n  --stability-probe-interval-ms N  Milliseconds between stability probes. Default: ${defaultOptions.stabilityProbeIntervalMs}\n  --child-timeout-ms N             Max milliseconds to wait for child process cleanup. Default: ${defaultOptions.childTimeoutMs}\n  --output PATH                    JSON output path. Default: results/latest.json\n`);
}

function runChecked(command, args, options = {}) {
  const result = spawnSync(command, args, {
    cwd: options.cwd ?? benchmarkRoot,
    env: options.env ?? process.env,
    encoding: "utf8",
    input: options.input,
  });
  if (result.error) throw result.error;
  if (result.status !== 0) {
    throw new Error(`${command} ${args.join(" ")} failed with status ${result.status}: ${result.stderr}`);
  }
  return result.stdout;
}

function packageRoot(packageName) {
  const packagePathParts = packageName.startsWith("@") ? packageName.split("/") : [packageName];
  let current = benchmarkRoot;
  for (;;) {
    const candidate = path.join(current, "node_modules", ...packagePathParts, "package.json");
    if (fs.existsSync(candidate)) return path.dirname(candidate);
    const parent = path.dirname(current);
    if (parent === current) break;
    current = parent;
  }

  try {
    const packageJsonPath = require.resolve(`${packageName}/package.json`);
    return path.dirname(packageJsonPath);
  } catch {
    const entrypoint = require.resolve(packageName);
    let currentDir = path.dirname(entrypoint);
    for (;;) {
      const candidate = path.join(currentDir, "package.json");
      if (fs.existsSync(candidate)) return currentDir;
      const parent = path.dirname(currentDir);
      if (parent === currentDir) break;
      currentDir = parent;
    }
  }

  throw new Error(`Could not find package root for ${packageName}`);
}

function packageVersion(packageName) {
  const packageJsonPath = path.join(packageRoot(packageName), "package.json");
  const packageJson = JSON.parse(fs.readFileSync(packageJsonPath, "utf8"));
  return packageJson.version;
}

function detectTargetTriple() {
  const { platform, arch } = process;
  if ((platform === "linux" || platform === "android") && arch === "x64") return "x86_64-unknown-linux-musl";
  if ((platform === "linux" || platform === "android") && arch === "arm64") return "aarch64-unknown-linux-musl";
  if (platform === "darwin" && arch === "x64") return "x86_64-apple-darwin";
  if (platform === "darwin" && arch === "arm64") return "aarch64-apple-darwin";
  if (platform === "win32" && arch === "x64") return "x86_64-pc-windows-msvc";
  if (platform === "win32" && arch === "arm64") return "aarch64-pc-windows-msvc";
  throw new Error(`Unsupported platform for Codex binary resolution: ${platform}/${arch}`);
}

function resolveCodexBinary() {
  const targetTriple = detectTargetTriple();
  const platformPackage = CODEX_PLATFORM_PACKAGE_BY_TARGET[targetTriple];
  if (!platformPackage) throw new Error(`No Codex platform package mapping for ${targetTriple}`);

  const platformPackageRoot = packageRoot(platformPackage);
  const vendorRoot = path.join(platformPackageRoot, "vendor");
  const binaryName = process.platform === "win32" ? "codex.exe" : "codex";
  return path.join(vendorRoot, targetTriple, "codex", binaryName);
}

function resolveClaudeCli() {
  return path.join(packageRoot("@anthropic-ai/claude-agent-sdk"), "cli.js");
}

function measureNodeEval(name, code) {
  const marker = "__SANDBOX0_RSS__";
  const stdout = runChecked(process.execPath, ["--input-type=module", "-e", `${code}\nconsole.log(${JSON.stringify(marker)} + process.memoryUsage().rss);`]);
  const line = stdout.split(/\r?\n/).find((candidate) => candidate.startsWith(marker));
  if (!line) throw new Error(`Could not parse RSS output for ${name}: ${stdout}`);
  return {
    name,
    rssKb: Math.round(Number(line.slice(marker.length)) / 1024),
  };
}

function listChildPids(pid) {
  if (process.platform === "win32") return [];
  const result = spawnSync("pgrep", ["-P", String(pid)], { encoding: "utf8" });
  if (result.error || result.status !== 0) return [];
  const direct = result.stdout.trim().split(/\s+/).filter(Boolean).map((value) => Number(value));
  return direct.flatMap((child) => [child, ...listChildPids(child)]);
}

function rssForPids(pids) {
  const livePids = pids.filter((pid) => Number.isInteger(pid) && pid > 0);
  if (livePids.length === 0) return 0;
  const stdout = runChecked("ps", ["-o", "rss=", "-p", livePids.join(",")]);
  return stdout
    .split(/\r?\n/)
    .map((line) => Number.parseInt(line.trim(), 10))
    .filter((value) => Number.isFinite(value))
    .reduce((sum, value) => sum + value, 0);
}

async function sampleProcessTreeRss(pid, options) {
  let best = {
    childPids: [],
    directRssKb: 0,
    treeRssKb: 0,
  };

  for (let i = 0; i < options.stabilityProbes; i += 1) {
    if (i > 0) await sleep(options.stabilityProbeIntervalMs);
    const childPids = listChildPids(pid);
    const directRssKb = rssForPids([pid]);
    const treeRssKb = rssForPids([pid, ...childPids]);
    if (treeRssKb > best.treeRssKb) {
      best = { childPids, directRssKb, treeRssKb };
    }
  }

  return best;
}

async function sleep(ms) {
  await new Promise((resolve) => setTimeout(resolve, ms));
}

async function terminateProcessTree(child, timeoutMs) {
  const pids = [child.pid, ...listChildPids(child.pid)].filter(Boolean);
  for (const pid of [...pids].reverse()) {
    try {
      process.kill(pid, "SIGTERM");
    } catch {
      // Process already exited.
    }
  }

  const exited = await new Promise((resolve) => {
    const timer = setTimeout(() => resolve(false), timeoutMs);
    child.once("exit", () => {
      clearTimeout(timer);
      resolve(true);
    });
  });

  if (!exited) {
    for (const pid of [...pids].reverse()) {
      try {
        process.kill(pid, "SIGKILL");
      } catch {
        // Process already exited.
      }
    }
  }
}

async function measureChildProcess({ name, command, args, env, warmupMs, timeoutMs, stabilityProbes, stabilityProbeIntervalMs }) {
  const tmpRoot = fs.mkdtempSync(path.join(os.tmpdir(), "sandbox0-agent-sdk-memory-"));
  const home = path.join(tmpRoot, "home");
  const claudeConfig = path.join(tmpRoot, "claude");
  const codexHome = path.join(tmpRoot, "codex");
  fs.mkdirSync(home, { recursive: true });
  fs.mkdirSync(claudeConfig, { recursive: true });
  fs.mkdirSync(codexHome, { recursive: true });

  const childEnv = {
    ...process.env,
    HOME: home,
    CLAUDE_CONFIG_DIR: claudeConfig,
    CODEX_HOME: codexHome,
    NO_COLOR: "1",
    CI: "1",
    ...env,
  };

  const child = spawn(command, args, {
    cwd: repoRoot,
    env: childEnv,
    stdio: ["pipe", "pipe", "pipe"],
  });

  let stderr = "";
  let stdout = "";
  child.stderr.on("data", (chunk) => {
    stderr += chunk.toString("utf8");
  });
  child.stdout.on("data", (chunk) => {
    stdout += chunk.toString("utf8");
  });

  let exitInfo = null;
  child.once("exit", (code, signal) => {
    exitInfo = { code, signal };
  });
  child.once("error", (error) => {
    exitInfo = { error: error.message };
  });

  await sleep(warmupMs);

  const rss = child.pid
    ? await sampleProcessTreeRss(child.pid, { stabilityProbes, stabilityProbeIntervalMs })
    : { childPids: [], directRssKb: 0, treeRssKb: 0 };

  if (child.pid) await terminateProcessTree(child, timeoutMs);
  fs.rmSync(tmpRoot, { recursive: true, force: true });

  return {
    name,
    pid: child.pid ?? null,
    childPids: rss.childPids,
    directRssKb: rss.directRssKb || null,
    treeRssKb: rss.treeRssKb || null,
    exitInfo,
    stderrPreview: stderr.slice(0, 500),
    stdoutPreview: stdout.slice(0, 500),
  };
}

function summarize(values) {
  const numeric = values.filter((value) => Number.isFinite(value));
  if (numeric.length === 0) return null;
  const sorted = [...numeric].sort((a, b) => a - b);
  const total = sorted.reduce((sum, value) => sum + value, 0);
  return {
    samples: numeric.length,
    minKb: sorted[0],
    maxKb: sorted[sorted.length - 1],
    avgKb: Math.round(total / sorted.length),
    minMb: roundMb(sorted[0]),
    maxMb: roundMb(sorted[sorted.length - 1]),
    avgMb: roundMb(total / sorted.length),
  };
}

function roundMb(kb) {
  return Math.round((kb / 1024) * 10) / 10;
}

function markdownTable(report) {
  const rows = report.results.map((result) => {
    const summary = result.summary;
    return [
      result.label,
      summary ? String(summary.avgMb) : "n/a",
      summary ? String(summary.minMb) : "n/a",
      summary ? String(summary.maxMb) : "n/a",
      result.primaryMetric,
    ];
  });
  const lines = [
    "| Benchmark | Avg MB | Min MB | Max MB | Primary metric |",
    "|---|---:|---:|---:|---|",
    ...rows.map((row) => `| ${row.join(" | ")} |`),
  ];
  return lines.join("\n");
}

async function main() {
  const options = parseArgs(process.argv.slice(2));
  const codexBinary = resolveCodexBinary();
  const claudeCli = resolveClaudeCli();

  const definitions = [
    {
      key: "node-baseline",
      label: "Node.js baseline",
      primaryMetric: "process.memoryUsage().rss",
      run: () => measureNodeEval("node-baseline", ""),
      value: (sample) => sample.rssKb,
    },
    {
      key: "codex-sdk-import",
      label: "Codex SDK import",
      primaryMetric: "process.memoryUsage().rss",
      run: () => measureNodeEval("codex-sdk-import", 'import { Codex } from "@openai/codex-sdk"; new Codex();'),
      value: (sample) => sample.rssKb,
    },
    {
      key: "claude-agent-sdk-import",
      label: "Claude Agent SDK import",
      primaryMetric: "process.memoryUsage().rss",
      run: () => measureNodeEval("claude-agent-sdk-import", 'import * as sdk from "@anthropic-ai/claude-agent-sdk"; void sdk;'),
      value: (sample) => sample.rssKb,
    },
    {
      key: "codex-exec-idle",
      label: "Codex exec idle",
      primaryMetric: "process tree RSS",
      run: () => measureChildProcess({
        name: "codex-exec-idle",
        command: codexBinary,
        args: ["exec", "--experimental-json"],
        warmupMs: options.warmupMs,
        timeoutMs: options.childTimeoutMs,
        stabilityProbes: options.stabilityProbes,
        stabilityProbeIntervalMs: options.stabilityProbeIntervalMs,
      }),
      value: (sample) => sample.treeRssKb,
    },
    {
      key: "codex-app-server-idle",
      label: "Codex app-server idle",
      primaryMetric: "process tree RSS",
      run: () => measureChildProcess({
        name: "codex-app-server-idle",
        command: codexBinary,
        args: ["app-server"],
        warmupMs: options.warmupMs,
        timeoutMs: options.childTimeoutMs,
        stabilityProbes: options.stabilityProbes,
        stabilityProbeIntervalMs: options.stabilityProbeIntervalMs,
      }),
      value: (sample) => sample.treeRssKb,
    },
    {
      key: "claude-cli-idle",
      label: "Claude bundled CLI idle",
      primaryMetric: "process tree RSS",
      run: () => measureChildProcess({
        name: "claude-cli-idle",
        command: process.execPath,
        args: [claudeCli],
        env: { ANTHROPIC_API_KEY: process.env.ANTHROPIC_API_KEY || "dummy" },
        warmupMs: options.warmupMs,
        timeoutMs: options.childTimeoutMs,
        stabilityProbes: options.stabilityProbes,
        stabilityProbeIntervalMs: options.stabilityProbeIntervalMs,
      }),
      value: (sample) => sample.treeRssKb,
    },
  ];

  const results = [];
  for (const definition of definitions) {
    const samples = [];
    for (let i = 0; i < options.samples; i += 1) {
      process.stderr.write(`Running ${definition.label} sample ${i + 1}/${options.samples}\n`);
      samples.push(await definition.run());
    }
    const values = samples.map(definition.value).filter((value) => value !== null && value !== undefined);
    results.push({
      key: definition.key,
      label: definition.label,
      primaryMetric: definition.primaryMetric,
      summary: summarize(values),
      samples,
    });
  }

  const report = {
    benchmark: "agent-sdk-memory",
    createdAt: new Date().toISOString(),
    options,
    host: {
      platform: process.platform,
      arch: process.arch,
      node: process.version,
      uname: process.platform === "win32" ? null : runChecked("uname", ["-a"]).trim(),
      totalMemoryKb: Math.round(os.totalmem() / 1024),
    },
    packages: {
      "@anthropic-ai/claude-agent-sdk": packageVersion("@anthropic-ai/claude-agent-sdk"),
      "@openai/codex-sdk": packageVersion("@openai/codex-sdk"),
      "@openai/codex": packageVersion("@openai/codex"),
    },
    binaries: {
      codexBinary,
      claudeCli,
    },
    results,
  };

  fs.mkdirSync(path.dirname(options.output), { recursive: true });
  fs.writeFileSync(options.output, `${JSON.stringify(report, null, 2)}\n`);

  console.log(markdownTable(report));
  console.log(`\nJSON report written to ${path.relative(process.cwd(), options.output)}`);
}

main().catch((error) => {
  console.error(error instanceof Error ? error.stack : error);
  process.exit(1);
});
