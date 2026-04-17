import "dotenv/config";

import Anthropic, { toFile } from "@anthropic-ai/sdk";
import type {
  BetaManagedAgentsSkillParams,
  AgentCreateParams,
} from "@anthropic-ai/sdk/resources/beta/agents";
import type { BetaManagedAgentsStreamSessionEvents } from "@anthropic-ai/sdk/resources/beta/sessions/events";
import type { CredentialCreateParams } from "@anthropic-ai/sdk/resources/beta/vaults/credentials";
import { createHash } from "node:crypto";
import { constants as fsConstants } from "node:fs";
import { access, mkdir, readdir, readFile, writeFile } from "node:fs/promises";
import path from "node:path";
import process, { stdin as input, stdout as output } from "node:process";
import { createInterface } from "node:readline/promises";
import { fileURLToPath } from "node:url";

const DEFAULT_BASE_URL = "https://agents.sandbox0.ai";
const DEFAULT_MODEL = "glm-5.1";
const METADATA_FILE = "example-metadata.json";

const EXAMPLE_ROOT = path.dirname(path.dirname(fileURLToPath(import.meta.url)));
const SKILLS_DIR = path.join(EXAMPLE_ROOT, "example-skills");

const SYSTEM_PROMPT = [
  "You are a concise coding copilot running inside a Sandbox0 Managed Agents sandbox.",
  "Use the attached skills when they match the user's request.",
  "Prefer small, verifiable changes and mention the commands or files that matter.",
].join("\n");

interface Config {
  apiKey: string;
  baseURL: string;
  model: string;
  llmAPIKey?: string;
  llmBaseURL?: string;
  verbose: boolean;
}

interface SkillMetadata {
  skillID: string;
  version: string;
  sourceHash: string;
}

interface ExampleMetadata {
  baseURL?: string;
  model?: string;
  environmentID?: string;
  agentID?: string;
  agentVersion?: number;
  vaultID?: string;
  skills?: Record<string, SkillMetadata>;
  updatedAt?: string;
}

interface SkillSource {
  name: string;
  displayTitle: string;
  files: Array<{ relativePath: string; content: Buffer }>;
  sourceHash: string;
}

interface TurnResult {
  canContinue: boolean;
  requiresAction: boolean;
  streamComplete: boolean;
  lastEventID?: string;
}

function optionalEnv(name: string): string | undefined {
  const value = process.env[name]?.trim();
  return value || undefined;
}

function readConfig(): Config {
  const apiKey = optionalEnv("SANDBOX0_API_KEY");
  if (!apiKey) {
    throw new Error("Set SANDBOX0_API_KEY before running this example.");
  }
  return {
    apiKey,
    baseURL: optionalEnv("MANAGED_AGENTS_BASE_URL") ?? DEFAULT_BASE_URL,
    model: optionalEnv("MANAGED_AGENTS_MODEL") ?? DEFAULT_MODEL,
    llmAPIKey: optionalEnv("MANAGED_AGENTS_LLM_API_KEY"),
    llmBaseURL: optionalEnv("MANAGED_AGENTS_LLM_BASE_URL"),
    verbose: optionalEnv("MANAGED_AGENTS_VERBOSE") === "1",
  };
}

function createClient(config: Config): Anthropic {
  return new Anthropic({
    apiKey: config.apiKey,
    authToken: null,
    baseURL: config.baseURL,
    timeout: 10 * 60 * 1000,
  });
}

async function readMetadata(metadataPath: string): Promise<ExampleMetadata> {
  try {
    const raw = await readFile(metadataPath, "utf8");
    return JSON.parse(raw) as ExampleMetadata;
  } catch (error) {
    if (error && typeof error === "object" && "code" in error && error.code === "ENOENT") {
      return {};
    }
    throw error;
  }
}

async function saveMetadata(metadataPath: string, metadata: ExampleMetadata): Promise<void> {
  const next = {
    ...metadata,
    updatedAt: new Date().toISOString(),
  };
  await writeFile(metadataPath, `${JSON.stringify(next, null, 2)}\n`, "utf8");
}

async function idExists(load: () => Promise<unknown>): Promise<boolean> {
  try {
    await load();
    return true;
  } catch (error) {
    if (error instanceof Anthropic.APIError && error.status === 404) {
      return false;
    }
    throw error;
  }
}

async function ensureEnvironment(client: Anthropic, metadata: ExampleMetadata): Promise<string> {
  if (metadata.environmentID && await idExists(() => client.beta.environments.retrieve(metadata.environmentID!))) {
    console.log(`environment: ${metadata.environmentID}`);
    return metadata.environmentID;
  }

  const environment = await client.beta.environments.create({
    name: "copilot-example",
    description: "Reusable environment for the Sandbox0 Managed Agents copilot example.",
    config: {
      type: "cloud",
      networking: { type: "unrestricted" },
      packages: { type: "packages" },
    },
    metadata: { example: "copilot" },
  });
  metadata.environmentID = environment.id;
  console.log(`environment: ${environment.id} created`);
  return environment.id;
}

async function ensureVault(client: Anthropic, config: Config, metadata: ExampleMetadata): Promise<string> {
  if (metadata.vaultID && await idExists(() => client.beta.vaults.retrieve(metadata.vaultID!))) {
    console.log(`vault: ${metadata.vaultID}`);
    return metadata.vaultID;
  }
  if (!config.llmAPIKey || !config.llmBaseURL) {
    throw new Error(`Set MANAGED_AGENTS_LLM_API_KEY and MANAGED_AGENTS_LLM_BASE_URL, or keep a reusable vault in ${METADATA_FILE}.`);
  }

  const vault = await client.beta.vaults.create({
    display_name: "Copilot Example LLM",
    metadata: {
      "sandbox0.managed_agents.role": "llm",
      "sandbox0.managed_agents.engine": "claude",
      "sandbox0.managed_agents.llm_base_url": config.llmBaseURL,
      example: "copilot",
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
  metadata.vaultID = vault.id;
  console.log(`vault: ${vault.id} created`);
  return vault.id;
}

async function loadSkillSources(): Promise<SkillSource[]> {
  await access(SKILLS_DIR, fsConstants.R_OK);
  const entries = await readdir(SKILLS_DIR, { withFileTypes: true });
  const skills: SkillSource[] = [];

  for (const entry of entries) {
    if (!entry.isDirectory()) {
      continue;
    }
    const name = entry.name;
    const root = path.join(SKILLS_DIR, name);
    const files = await readSkillFiles(root, name);
    const skillFile = files.find((file) => file.relativePath === `${name}/SKILL.md`);
    if (!skillFile) {
      throw new Error(`Skill ${name} is missing SKILL.md`);
    }
    skills.push({
      name,
      displayTitle: titleFromSkillName(name),
      files,
      sourceHash: hashSkillFiles(files),
    });
  }

  if (skills.length === 0) {
    throw new Error(`No skills found in ${SKILLS_DIR}`);
  }
  return skills.sort((left, right) => left.name.localeCompare(right.name));
}

async function readSkillFiles(root: string, skillName: string, current = root): Promise<Array<{ relativePath: string; content: Buffer }>> {
  const entries = await readdir(current, { withFileTypes: true });
  const files: Array<{ relativePath: string; content: Buffer }> = [];
  for (const entry of entries) {
    const fullPath = path.join(current, entry.name);
    if (entry.isDirectory()) {
      files.push(...await readSkillFiles(root, skillName, fullPath));
      continue;
    }
    if (!entry.isFile()) {
      continue;
    }
    const insideSkill = path.relative(root, fullPath).split(path.sep).join("/");
    files.push({
      relativePath: `${skillName}/${insideSkill}`,
      content: await readFile(fullPath),
    });
  }
  return files.sort((left, right) => left.relativePath.localeCompare(right.relativePath));
}

function hashSkillFiles(files: Array<{ relativePath: string; content: Buffer }>): string {
  const hash = createHash("sha256");
  for (const file of files) {
    hash.update(file.relativePath);
    hash.update("\0");
    hash.update(file.content);
    hash.update("\0");
  }
  return hash.digest("hex");
}

function titleFromSkillName(name: string): string {
  return name.split("-").map((part) => part.slice(0, 1).toUpperCase() + part.slice(1)).join(" ");
}

async function uploadablesForSkill(skill: SkillSource) {
  return Promise.all(skill.files.map((file) => toFile(file.content, file.relativePath, { type: "text/markdown" })));
}

async function ensureSkills(client: Anthropic, metadata: ExampleMetadata): Promise<BetaManagedAgentsSkillParams[]> {
  const sources = await loadSkillSources();
  metadata.skills ??= {};
  const params: BetaManagedAgentsSkillParams[] = [];

  for (const source of sources) {
    const cached = metadata.skills[source.name];
    if (cached?.skillID && cached.version && cached.sourceHash === source.sourceHash) {
      const exists = await idExists(() => client.beta.skills.versions.retrieve(cached.version, { skill_id: cached.skillID }));
      if (exists) {
        params.push({ type: "custom", skill_id: cached.skillID, version: cached.version });
        console.log(`skill: ${source.name} ${cached.version}`);
        continue;
      }
    }

    const files = await uploadablesForSkill(source);
    if (cached?.skillID && await idExists(() => client.beta.skills.retrieve(cached.skillID))) {
      const version = await client.beta.skills.versions.create(cached.skillID, { files });
      metadata.skills[source.name] = {
        skillID: cached.skillID,
        version: version.version,
        sourceHash: source.sourceHash,
      };
      params.push({ type: "custom", skill_id: cached.skillID, version: version.version });
      console.log(`skill: ${source.name} ${version.version} created`);
      continue;
    }

    const skill = await client.beta.skills.create({
      display_title: source.displayTitle,
      files,
    });
    if (!skill.latest_version) {
      throw new Error(`Created skill ${skill.id} without a latest version`);
    }
    metadata.skills[source.name] = {
      skillID: skill.id,
      version: skill.latest_version,
      sourceHash: source.sourceHash,
    };
    params.push({ type: "custom", skill_id: skill.id, version: skill.latest_version });
    console.log(`skill: ${source.name} ${skill.latest_version} created`);
  }

  return params;
}

async function ensureAgent(
  client: Anthropic,
  config: Config,
  metadata: ExampleMetadata,
  skills: BetaManagedAgentsSkillParams[],
): Promise<string> {
  const tools: NonNullable<AgentCreateParams["tools"]> = [{
    type: "agent_toolset_20260401",
    default_config: {
      enabled: true,
      permission_policy: { type: "always_allow" },
    },
  }];
  const params = {
    name: "Copilot Example",
    model: config.model,
    system: SYSTEM_PROMPT,
    tools,
    skills,
    metadata: { example: "copilot" },
  };

  if (metadata.agentID && await idExists(() => client.beta.agents.retrieve(metadata.agentID!))) {
    const current = await client.beta.agents.retrieve(metadata.agentID);
    const currentSkills = current.skills.map((skill) => `${skill.type}:${skill.skill_id}:${skill.version}`).sort();
    const nextSkills = skills.map((skill) => `${skill.type}:${skill.skill_id}:${skill.version ?? ""}`).sort();
    if (
      current.model.id === config.model
      && current.system === SYSTEM_PROMPT
      && JSON.stringify(currentSkills) === JSON.stringify(nextSkills)
    ) {
      metadata.agentVersion = current.version;
      console.log(`agent: ${metadata.agentID} v${current.version}`);
      return metadata.agentID;
    }

    const updated = await client.beta.agents.update(metadata.agentID, {
      version: current.version,
      name: params.name,
      model: params.model,
      system: params.system,
      tools: params.tools,
      skills: params.skills,
      metadata: params.metadata,
    });
    metadata.agentVersion = updated.version;
    console.log(`agent: ${updated.id} v${updated.version} updated`);
    return updated.id;
  }

  const agent = await client.beta.agents.create(params);
  metadata.agentID = agent.id;
  metadata.agentVersion = agent.version;
  console.log(`agent: ${agent.id} v${agent.version} created`);
  return agent.id;
}

async function createSession(client: Anthropic, agentID: string, environmentID: string, vaultID: string): Promise<string> {
  const session = await client.beta.sessions.create({
    agent: agentID,
    environment_id: environmentID,
    title: "Copilot example REPL",
    vault_ids: [vaultID],
    metadata: { example: "copilot" },
  });
  console.log(`session: ${session.id}`);
  return session.id;
}

async function sendAndStreamTurn(
  client: Anthropic,
  sessionID: string,
  text: string,
  verbose: boolean,
): Promise<TurnResult> {
  const sent = await client.beta.sessions.events.send(sessionID, {
    events: [{
      type: "user.message",
      content: [{ type: "text", text }],
    }],
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
      case "span.model_request_end":
        if (verbose) {
          const usage = event.model_usage;
          output.write(`\n[model usage] input=${usage.input_tokens} output=${usage.output_tokens}\n`);
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

async function runRepl(client: Anthropic, sessionID: string, verbose: boolean): Promise<void> {
  const rl = createInterface({ input, output });
  console.log("Type a request, /help, or /exit.");

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
        console.log("Commands: /help, /exit");
        console.log(`Reusable IDs are stored in ${path.join(process.cwd(), METADATA_FILE)}.`);
        continue;
      }

      console.log("Copilot>");
      const result = await sendAndStreamTurn(client, sessionID, text, verbose);
      if (!result.canContinue) {
        if (result.requiresAction) {
          console.error("The session requires an action this simple REPL does not resolve.");
        }
        return;
      }
    }
  } finally {
    rl.close();
  }
}

async function main(): Promise<void> {
  const config = readConfig();
  const client = createClient(config);
  const metadataPath = path.join(process.cwd(), METADATA_FILE);
  await mkdir(path.dirname(metadataPath), { recursive: true });
  const metadata = await readMetadata(metadataPath);
  metadata.baseURL = config.baseURL;
  metadata.model = config.model;

  console.log(`metadata: ${metadataPath}`);
  console.log(`api: ${config.baseURL}`);

  const environmentID = await ensureEnvironment(client, metadata);
  await saveMetadata(metadataPath, metadata);
  const vaultID = await ensureVault(client, config, metadata);
  await saveMetadata(metadataPath, metadata);
  const skills = await ensureSkills(client, metadata);
  await saveMetadata(metadataPath, metadata);
  const agentID = await ensureAgent(client, config, metadata, skills);
  await saveMetadata(metadataPath, metadata);

  const sessionID = await createSession(client, agentID, environmentID, vaultID);
  await runRepl(client, sessionID, config.verbose);
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
