# Sandbox0 Managed Agents Copilot Demo

Small TypeScript SDK demo for Sandbox0 Managed Agents. It creates an agent, creates an environment, starts a session, sends user events, and streams session events until the agent goes idle.

The demo runs as a terminal copilot. It keeps one session alive, so follow-up prompts continue in the same managed-agent workspace.

## Setup

```bash
cd /Users/huangzhihao/sandbox0/workspace/managed-agents/examples/copilot
npm install
cp .env.example .env
```

Set the Sandbox0 gateway key and the LLM provider key separately:

```bash
SANDBOX0_API_KEY=s0_...
MANAGED_AGENTS_LLM_API_KEY=provider-token
MANAGED_AGENTS_LLM_BASE_URL=https://api.z.ai/api/anthropic
MANAGED_AGENTS_MODEL=glm-5.1
```

`SANDBOX0_API_KEY` authenticates requests to Sandbox0 Managed Agents. `MANAGED_AGENTS_LLM_API_KEY` creates a Sandbox0 LLM vault whose credential is mounted into the runtime for Anthropic-compatible model calls.

The demo explicitly disables the SDK's `ANTHROPIC_AUTH_TOKEN` fallback so an inherited model-provider token cannot override `SANDBOX0_API_KEY` on gateway requests.

## Run

Interactive mode:

```bash
npm run start
```

One-shot mode:

```bash
npm run start -- --task "Create a Python script that writes the first 20 Fibonacci numbers to fibonacci.txt"
```

Useful commands in interactive mode:

```text
/exit   close the local CLI
/help   print local commands
```

## Notes

- The TypeScript SDK requires Node.js 20+ and TypeScript 4.9+.
- Managed Agents requests use the `managed-agents-2026-04-01` beta header; the SDK sets it automatically for `client.beta.*` Managed Agents calls.
- The built-in `agent_toolset_20260401` enables the agent tools used by the quickstart flow.
- To avoid creating new agent, environment, or vault records on each run, set `MANAGED_AGENTS_AGENT_ID`, `MANAGED_AGENTS_ENVIRONMENT_ID`, and `MANAGED_AGENTS_VAULT_ID`.

Reference docs: https://platform.claude.com/docs/en/managed-agents/quickstart and https://platform.claude.com/docs/en/api/sdks/typescript.
