import "dotenv/config";

import Anthropic from "@anthropic-ai/sdk";
import type { BetaManagedAgentsStreamSessionEvents } from "@anthropic-ai/sdk/resources/beta/sessions/events";
import type { CredentialCreateParams } from "@anthropic-ai/sdk/resources/beta/vaults/credentials";
import process, { stdin as input, stdout as output } from "node:process";
import { createInterface } from "node:readline/promises";

const DEFAULT_BASE_URL = "https://agents.sandbox0.ai";
const DEFAULT_MODEL = "glm-5.1";

const COPILOT_SYSTEM_PROMPT = [
  "You are a concise coding copilot running inside a Sandbox0 Managed Agents sandbox.",
  "Help the user inspect, create, and modify files in the session workspace.",
  "Prefer small, verifiable changes. Mention file paths and commands when they matter.",
  "Before a destructive action, explain the risk and ask for confirmation.",
].join("\n");

interface Config {
  apiKey: string;
  baseURL: string;
  model: string;
  agentID?: string;
  environmentID?: string;
  vaultID?: string;
  llmAPIKey?: string;
  llmBaseURL?: string;
  verbose: boolean;
}

interface CLIArgs {
  help: boolean;
  task?: string;
}

interface TurnResult {
  canContinue: boolean;
  requiresAction: boolean;
  streamComplete: boolean;
  lastEventID?: string;
}

function printHelp(): void {
  console.log(`Sandbox0 Managed Agents Copilot Demo

Usage:
  npm run start
  npm run start -- --task "Create a Python script that writes fibonacci.txt"

Environment:
  SANDBOX0_API_KEY              API key for the Sandbox0 Managed Agents gateway
  MANAGED_AGENTS_BASE_URL       Defaults to ${DEFAULT_BASE_URL}
  MANAGED_AGENTS_MODEL          Defaults to ${DEFAULT_MODEL}
  MANAGED_AGENTS_LLM_API_KEY    LLM provider key stored in a Sandbox0 LLM vault
  MANAGED_AGENTS_LLM_BASE_URL   Anthropic-compatible LLM provider endpoint

Interactive commands:
  /help                         Show this help
  /exit                         Close the local CLI`);
}

function parseArgs(argv: string[]): CLIArgs {
  const positional: string[] = [];
  for (let index = 0; index < argv.length; index += 1) {
    const arg = argv[index];
    if (arg === "--help" || arg === "-h") {
      return { help: true };
    }
    if (arg === "--task" || arg === "-t") {
      const value = argv[index + 1]?.trim();
      if (!value) {
        throw new Error(`${arg} requires a value`);
      }
      index += 1;
      return { help: false, task: value };
    }
    positional.push(arg);
  }
  const task = positional.join(" ").trim();
  return { help: false, task: task || undefined };
}

function optionalEnv(name: string): string | undefined {
  const value = process.env[name]?.trim();
  return value || undefined;
}

function readConfig(): Config {
  const apiKey = optionalEnv("SANDBOX0_API_KEY");
  if (!apiKey) {
    throw new Error("Set SANDBOX0_API_KEY for the Sandbox0 Managed Agents gateway.");
  }

  const vaultID = optionalEnv("MANAGED_AGENTS_VAULT_ID");
  const llmAPIKey = optionalEnv("MANAGED_AGENTS_LLM_API_KEY");
  const llmBaseURL = optionalEnv("MANAGED_AGENTS_LLM_BASE_URL");
  if (!vaultID && (!llmAPIKey || !llmBaseURL)) {
    throw new Error("Set MANAGED_AGENTS_LLM_API_KEY and MANAGED_AGENTS_LLM_BASE_URL, or set MANAGED_AGENTS_VAULT_ID to reuse an existing LLM vault.");
  }

  return {
    apiKey,
    baseURL: optionalEnv("MANAGED_AGENTS_BASE_URL") ?? DEFAULT_BASE_URL,
    model: optionalEnv("MANAGED_AGENTS_MODEL") ?? DEFAULT_MODEL,
    agentID: optionalEnv("MANAGED_AGENTS_AGENT_ID"),
    environmentID: optionalEnv("MANAGED_AGENTS_ENVIRONMENT_ID"),
    vaultID,
    llmAPIKey,
    llmBaseURL,
    verbose: optionalEnv("MANAGED_AGENTS_VERBOSE") === "1",
  };
}

function createClient(config: Config): Anthropic {
  return new Anthropic({
    apiKey: config.apiKey,
    // The SDK otherwise reads ANTHROPIC_AUTH_TOKEN and sends Authorization.
    // Sandbox0 Managed Agents expects the gateway key in X-Api-Key.
    authToken: null,
    baseURL: config.baseURL,
    timeout: 10 * 60 * 1000,
  });
}

async function ensureAgent(client: Anthropic, config: Config, suffix: string): Promise<string> {
  if (config.agentID) {
    console.log(`Using existing agent ${config.agentID}`);
    return config.agentID;
  }

  const agent = await client.beta.agents.create({
    name: `Copilot Demo ${suffix}`,
    model: config.model,
    system: COPILOT_SYSTEM_PROMPT,
    tools: [{ type: "agent_toolset_20260401" }],
    metadata: {
      demo: "copilot",
      source: "managed-agents-examples",
    },
  });
  console.log(`Created agent ${agent.id} v${agent.version}`);
  return agent.id;
}

async function ensureEnvironment(client: Anthropic, config: Config, suffix: string): Promise<string> {
  if (config.environmentID) {
    console.log(`Using existing environment ${config.environmentID}`);
    return config.environmentID;
  }

  const environment = await client.beta.environments.create({
    name: `copilot-demo-env-${suffix}`,
    description: "Reusable cloud environment for the TypeScript SDK copilot demo.",
    config: {
      type: "cloud",
      networking: { type: "unrestricted" },
      packages: { type: "packages" },
    },
    metadata: {
      demo: "copilot",
      source: "managed-agents-examples",
    },
  });
  console.log(`Created environment ${environment.id}`);
  return environment.id;
}

async function ensureVaultIDs(client: Anthropic, config: Config, suffix: string): Promise<string[]> {
  if (config.vaultID) {
    console.log(`Using existing vault ${config.vaultID}`);
    return [config.vaultID];
  }
  if (!config.llmAPIKey || !config.llmBaseURL) {
    throw new Error("LLM key and base URL are required when MANAGED_AGENTS_VAULT_ID is not set.");
  }

  const vault = await client.beta.vaults.create({
    display_name: `Copilot Demo LLM ${suffix}`,
    metadata: {
      "sandbox0.managed_agents.role": "llm",
      "sandbox0.managed_agents.engine": "claude",
      "sandbox0.managed_agents.llm_base_url": config.llmBaseURL,
    },
  });

  const auth = {
    type: "static_bearer",
    token: config.llmAPIKey,
  } as CredentialCreateParams["auth"];

  await client.beta.vaults.credentials.create(vault.id, {
    display_name: "Anthropic-compatible LLM API key",
    auth,
  });

  console.log(`Created Sandbox0 LLM vault ${vault.id}`);
  return [vault.id];
}

async function createSession(
  client: Anthropic,
  config: Config,
  agentID: string,
  environmentID: string,
  vaultIDs: string[],
  suffix: string,
): Promise<string> {
  const metadata: Record<string, string> = {
    demo: "copilot",
    source: "managed-agents-examples",
  };

  const session = await client.beta.sessions.create({
    agent: agentID,
    environment_id: environmentID,
    title: `Copilot demo ${suffix}`,
    metadata,
    vault_ids: vaultIDs,
  });

  console.log(`Started session ${session.id}`);
  return session.id;
}

async function sendAndStreamTurn(
  client: Anthropic,
  sessionID: string,
  text: string,
  verbose: boolean,
): Promise<TurnResult> {
  const sent = await client.beta.sessions.events.send(sessionID, {
    events: [
      {
        type: "user.message",
        content: [{ type: "text", text }],
      },
    ],
  });

  let lastEventID = sent.data?.at(-1)?.id;

  for (;;) {
    const stream = await client.beta.sessions.events.stream(
      sessionID,
      {},
      lastEventID ? { headers: { "Last-Event-ID": lastEventID } } : undefined,
    );
    const result = await consumeEventStream(stream, verbose);
    if (result.lastEventID) {
      lastEventID = result.lastEventID;
    }
    if (result.streamComplete) {
      return result;
    }
    if (verbose) {
      output.write("\n[stream reconnect]\n");
    }
  }
}

async function consumeEventStream(
  stream: AsyncIterable<BetaManagedAgentsStreamSessionEvents>,
  verbose: boolean,
): Promise<TurnResult> {
  let wroteAgentText = false;
  let lastEventID: string | undefined;

  for await (const event of stream) {
    if (typeof event.id === "string" && event.id) {
      lastEventID = event.id;
    }
    switch (event.type) {
      case "agent.message": {
        const text = event.content.map((block) => block.text).join("");
        if (text) {
          output.write(text);
          wroteAgentText = true;
        }
        break;
      }
      case "agent.tool_use":
        output.write(`\n[tool] ${event.name}\n`);
        break;
      case "agent.mcp_tool_use":
        output.write(`\n[mcp:${event.mcp_server_name}] ${event.name}\n`);
        break;
      case "agent.custom_tool_use":
        output.write(`\n[custom tool] ${event.name}\n`);
        break;
      case "agent.tool_result":
        if (event.is_error) {
          output.write(`\n[tool error] ${event.tool_use_id}\n`);
        }
        break;
      case "agent.mcp_tool_result":
        if (event.is_error) {
          output.write(`\n[mcp tool error] ${event.mcp_tool_use_id}\n`);
        }
        break;
      case "agent.thinking":
      case "agent.thread_context_compacted":
      case "session.status_running":
      case "session.status_rescheduled":
      case "span.model_request_start":
        if (verbose) {
          output.write(`\n[${event.type}]\n`);
        }
        break;
      case "span.model_request_end":
        if (verbose) {
          const usage = event.model_usage;
          output.write(`\n[model usage] input=${usage.input_tokens} output=${usage.output_tokens}\n`);
        }
        break;
      case "session.error": {
        const retryStatus = event.error.retry_status.type;
        output.write(`\n[session error:${retryStatus}] ${event.error.message}\n`);
        if (retryStatus !== "retrying") {
          return { canContinue: retryStatus === "exhausted", requiresAction: false, streamComplete: true, lastEventID };
        }
        break;
      }
      case "session.status_idle": {
        if (wroteAgentText) {
          output.write("\n");
        }
        if (event.stop_reason.type === "requires_action") {
          output.write(`[requires action] ${event.stop_reason.event_ids.join(", ")}\n`);
          return { canContinue: false, requiresAction: true, streamComplete: true, lastEventID };
        }
        if (event.stop_reason.type === "retries_exhausted") {
          output.write("[idle] retries exhausted\n");
        }
        return { canContinue: true, requiresAction: false, streamComplete: true, lastEventID };
      }
      case "session.status_terminated":
      case "session.deleted":
        output.write(`\n[${event.type}]\n`);
        return { canContinue: false, requiresAction: false, streamComplete: true, lastEventID };
      case "user.message":
      case "user.interrupt":
      case "user.tool_confirmation":
      case "user.custom_tool_result":
        break;
    }
  }

  if (wroteAgentText) {
    output.write("\n");
  }
  return { canContinue: true, requiresAction: false, streamComplete: false, lastEventID };
}

async function runOneShot(client: Anthropic, sessionID: string, task: string, verbose: boolean): Promise<void> {
  console.log("Copilot>");
  const result = await sendAndStreamTurn(client, sessionID, task, verbose);
  if (result.requiresAction) {
    process.exitCode = 2;
  }
}

async function runInteractive(client: Anthropic, sessionID: string, verbose: boolean): Promise<void> {
  const rl = createInterface({ input, output });
  console.log("Type a task, /help, or /exit.");

  try {
    for (;;) {
      const text = (await rl.question("\nYou> ")).trim();
      if (!text) {
        continue;
      }
      if (text === "/exit" || text === "/quit") {
        return;
      }
      if (text === "/help") {
        printHelp();
        continue;
      }

      console.log("Copilot>");
      const result = await sendAndStreamTurn(client, sessionID, text, verbose);
      if (!result.canContinue) {
        if (result.requiresAction) {
          console.error("This demo does not yet implement custom action resolution events.");
        }
        return;
      }
    }
  } finally {
    rl.close();
  }
}

async function main(): Promise<void> {
  const args = parseArgs(process.argv.slice(2));
  if (args.help) {
    printHelp();
    return;
  }

  const config = readConfig();
  const client = createClient(config);
  const suffix = `${Date.now().toString(36)}-${Math.random().toString(36).slice(2, 8)}`;

  console.log(`Managed Agents API: ${config.baseURL}`);
  const [agentID, environmentID, vaultIDs] = await Promise.all([
    ensureAgent(client, config, suffix),
    ensureEnvironment(client, config, suffix),
    ensureVaultIDs(client, config, suffix),
  ]);
  const sessionID = await createSession(client, config, agentID, environmentID, vaultIDs, suffix);

  if (args.task) {
    await runOneShot(client, sessionID, args.task, config.verbose);
    return;
  }

  await runInteractive(client, sessionID, config.verbose);
}

main().catch((error: unknown) => {
  if (error instanceof Anthropic.APIError) {
    console.error(`Anthropic API error ${error.status ?? "unknown"}: ${error.message}`);
    if (error.requestID) {
      console.error(`request-id: ${error.requestID}`);
    }
  } else if (error instanceof Error) {
    console.error(error.message);
  } else {
    console.error(error);
  }
  process.exitCode = 1;
});
