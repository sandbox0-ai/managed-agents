# Sandbox0 Managed Agents Copilot Example

A small TypeScript REPL for Sandbox0 Managed Agents.

The example creates reusable resources once, stores their IDs in `example-metadata.json` in the directory where you run the CLI, and reuses them on later runs:

- environment
- LLM vault
- three custom skills from `example-skills`
- agent configured with those skills

Each CLI run starts a fresh session for the REPL.

## Setup

```bash
cd /Users/huangzhihao/sandbox0/workspace/managed-agents/examples/copilot
npm install
cp .env.example .env
```

Set:

```bash
SANDBOX0_API_KEY=s0_...
MANAGED_AGENTS_LLM_API_KEY=provider-token
MANAGED_AGENTS_LLM_BASE_URL=https://api.z.ai/api/anthropic
MANAGED_AGENTS_MODEL=glm-5.1
```

`SANDBOX0_API_KEY` authenticates requests to Sandbox0 Managed Agents. The LLM key is stored in a reusable Sandbox0 vault and is not written to `example-metadata.json`.

## Run

```bash
npm run dev
```

REPL commands:

```text
/help
/exit
```

Delete `example-metadata.json` if you want the example to create a new environment, vault, skills, and agent.
