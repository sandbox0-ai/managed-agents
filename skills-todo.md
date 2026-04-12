# Managed Agents Skills TODO

Reviewed on 2026-04-12 against:

- https://platform.claude.com/docs/en/managed-agents/skills
- https://platform.claude.com/docs/en/agents-and-tools/agent-skills/overview
- https://platform.claude.com/docs/en/build-with-claude/skills-guide
- local `tmp-claude-managed-agent-sdk`

## Contract Summary

Official Managed Agents skills are workspace-level reusable filesystem resources. Agents reference skills with `{type, skill_id, version}`. Custom skill uploads are versioned snapshots of one top-level directory that contains a root `SKILL.md`. Claude initially sees skill metadata, then loads `SKILL.md` and additional files only when relevant.

The local implementation already has the main shape: `/v1/skills` routes, custom skill upload parsing, version snapshots, agent `skills` normalization, runtime materialization into `.claude/skills`, and wrapper forwarding to Claude Agent SDK `skills: string[]`. The items below are the remaining contract and semantic gaps.

## P0 Gaps

### 1. Accept the official Skills API beta header

Current state:

- `internal/managedagents/beta.go` only accepts `managed-agents-2026-04-01` for `/v1/skills`.
- Official SDK calls `client.beta.skills.*` with `skills-2025-10-02`.
- `internal/managedagents/anthropic_skills.go` proxies upstream skill catalog requests with the managed-agents beta header instead of the skills beta header.

Required work:

- Accept `skills-2025-10-02` for `/v1/skills` and `/v1/skills/{id}/versions` routes.
- Keep `managed-agents-2026-04-01` accepted for compatibility if existing clients rely on it.
- Use `skills-2025-10-02` when proxying Anthropic `/v1/skills` upstream catalog requests.
- Add contract tests with the official beta header and negative tests for unrelated beta headers.

### 2. Support official multipart file field semantics

Current state:

- `internal/managedagents/skill_http.go` only reads multipart files from the `files` field.
- Unknown multipart file fields are rejected, so official clients that send `files[]` may fail.

Required work:

- Accept both `files` and `files[]` as aliases for skill upload files.
- Keep rejecting unrelated multipart fields.
- Add create-skill and create-version tests for `files[]`.

### 3. Enforce official `SKILL.md` metadata validation

Current state:

- `internal/managedagents/skill_parser.go` falls back from missing frontmatter to heading/paragraph/default values.
- It does not enforce official `name` and `description` constraints.

Required work:

- Require YAML frontmatter in root `SKILL.md`.
- Require `name` and `description` in frontmatter.
- Enforce `name`: max 64 chars, lowercase letters/numbers/hyphens only, no XML tags, no reserved words.
- Enforce `description`: non-empty, max 1024 chars, no XML tags.
- Return `invalid_request_error` for invalid uploads.
- Add parser tests for missing frontmatter, bad name chars, overlong name/description, XML tags, and valid folded YAML descriptions.

### 4. Enforce official upload size limits

Current state:

- Skill upload files are read with `io.ReadAll` and stored without an explicit total-size cap.
- Official Skills API caps total skill upload size at 30 MB.

Required work:

- Enforce a 30 MB combined file-size limit before storing files.
- Return HTTP 413 with `request_too_large` when exceeded.
- Avoid reading unbounded multipart payloads into memory.
- Add HTTP tests for exact limit, over-limit, and multiple-file combined-size behavior.

### 5. Keep pre-built Anthropic skill capability without storing skill content

Current state:

- The fallback Anthropic skill registry is metadata-only and lists built-ins such as `pptx`, `xlsx`, `docx`, and `pdf`.
- Runtime handles `type: "anthropic"` by passing only the skill ID into `skill_names`.
- No pre-built skill files are materialized by our runtime.
- Wrapper passes `skills: string[]` to Claude Agent SDK, so explicit version pinning is not preserved for pre-built skills.

Decision:

- Do not vendor, mirror, redistribute, or store Anthropic pre-built skill contents in Sandbox0.
- Keep the public `type: "anthropic"` skill capability as a provider-delegated feature.
- Treat Anthropic pre-built skill execution as supported only when the configured Claude provider/runtime can load those skills itself.

Required work:

- Continue resolving `latest` to an exact version during agent normalization and preserve that version in the agent/session snapshot for API compatibility.
- Add an explicit runtime capability gate, for example `supports_anthropic_prebuilt_skills`, so sessions fail before bootstrap if pre-built skills are requested on a provider/runtime that cannot load them.
- Verify whether Claude Agent SDK `skills: string[]` can load Anthropic pre-built skills by name in our wrapper environment.
- Verify whether that SDK path can honor pinned versions. If it cannot, document pre-built version pinning as catalog-level only for the delegated path and reject non-latest explicit versions unless we later get a structured SDK option.
- Add e2e tests for supported provider delegation and unsupported-provider fail-closed behavior.

## P1 Gaps

### 6. Preserve skill version identity through the wrapper boundary

Current state:

- Custom skill version pinning is mostly preserved because the runtime materializes the exact files before calling wrapper.
- Pre-built skill version identity is lost because wrapper only receives skill names.

Required work:

- Extend the runtime-to-wrapper bootstrap payload with structured skill references, for example `{type, skill_id, version, directory}`.
- Continue passing `skills: string[]` to Claude Agent SDK only when that is sufficient for the SDK path.
- For custom skills, materialize the exact files and pass the corresponding directory/name.
- For pre-built Anthropic skills, do not materialize files; pass the skill ID only through the provider-delegated path, and fail closed if the runtime cannot support it.
- Add wrapper tests that confirm structured skill refs are stored and translated deterministically.

### 7. Revisit custom skill file storage backend

Current state:

- Custom skill files are stored in PostgreSQL JSONB bytes in `managed_agent_skill_versions.files`.
- This works for small fixtures, but conflicts with large upload limits and creates DB bloat risk.

Required work:

- Move skill version file payloads to a workspace/team-level volume or object store.
- Keep PostgreSQL as metadata/index state only: skill ID, latest version, version metadata, file manifest, content hashes, sizes.
- Preserve immutable version snapshots.
- Add migration/backfill for existing DB-backed skill files if any production data exists.

### 8. Align delete semantics with official version lifecycle

Current state:

- `DeleteSkill` cascades and deletes all versions through `ON DELETE CASCADE`.
- Official docs guide users to delete all versions first, then delete the skill.

Required work:

- Confirm official API behavior with SDK/API tests if possible.
- If official delete rejects skills with existing versions, change `DeleteSkill` to return conflict until all versions are removed.
- If official delete cascades despite the docs wording, document our behavior and add tests.

## P2 Gaps

### 9. Clarify runtime filesystem path compatibility

Current state:

- Custom skills are materialized under `<working_directory>/.claude/skills/{directory}`.
- This matches Claude Agent SDK / Claude Code local skill discovery conventions.

Required work:

- Verify whether Managed Agents users or tools can rely on a `/skills/{directory}` container path.
- If yes, also materialize or symlink skills under `/skills/{directory}` while keeping `.claude/skills` for Claude Agent SDK discovery.
- Add runtime tests for both paths if both are supported.

### 10. Add end-to-end skill activation coverage

Current state:

- Unit tests cover parser, resource path normalization, and wrapper `skill_names` plumbing.
- There is no full session-level test proving Claude Agent SDK discovers and activates a custom skill from our materialized files.

Required work:

- Create a deterministic custom skill fixture with a unique instruction.
- Start a managed-agent session with that skill attached.
- Verify the wrapper launches Claude Agent SDK with the expected `skills` option.
- Verify the model/runtime can read the skill's `SKILL.md` and use a supporting file when relevant.

## Pre-built Skill Implementation Plan

Goal: keep official `type: "anthropic"` skill references in the API contract without storing or redistributing Anthropic pre-built skill contents in Sandbox0. Users can reference `pptx`, `xlsx`, `docx`, or `pdf`; execution is delegated to a supported Claude provider/runtime.

Preferred design:

1. Catalog source of truth
   - In production, use Anthropic `/v1/skills?source=anthropic` with `skills-2025-10-02` to discover available pre-built skills and latest versions.
   - Keep the local fallback registry only as a degraded metadata fallback, not as proof that the runtime can execute the skill.
   - Keep storing resolved versions in agent snapshots for SDK/API compatibility even if runtime execution is delegated.

2. Runtime delegation
   - Do not store pre-built skill files in PostgreSQL, volumes, object storage, or template images.
   - Pass pre-built skill IDs to Claude Agent SDK through `skills: string[]` only for runtimes that are verified to support Anthropic pre-built skills.
   - Add a capability check before runtime bootstrap. If the provider/runtime cannot support delegated pre-built skills, reject the session with a terminal skill capability error.

3. Version behavior
   - Resolve `latest` during agent create/update and store the exact version in the agent snapshot.
   - If Claude Agent SDK only accepts skill names and cannot pin a pre-built version, reject explicit non-latest versions for `type: "anthropic"` or document the limitation as provider-delegated latest semantics.
   - Do not silently claim exact version execution unless the delegated provider path exposes and verifies that exact version.

4. Session bootstrap
   - Custom skills: materialize exact version files into `.claude/skills/{directory}`.
   - Pre-built Anthropic skills: do not materialize files; pass the `skill_id` to Claude Agent SDK.
   - Store structured skill refs in the runtime bootstrap payload so logs/events can explain exactly what was requested and what path handled it.

5. Upgrade behavior
   - New agent creation without `version` uses current latest.
   - Existing agent/session snapshots remain pinned to the resolved version.
   - Updating an agent with `version: "latest"` re-resolves to the current latest and creates a new agent version.

6. Failure behavior
   - If catalog metadata resolves but the provider/runtime does not support delegated pre-built skills, fail session bootstrap with a terminal skill capability error instead of silently starting without the skill.
   - If upstream catalog is unavailable, allow already-resolved agent snapshots to continue only when the runtime capability gate passes.

7. Verification
   - Unit test latest resolution and explicit version resolution.
   - Unit test runtime capability gating.
   - Wrapper test that pre-built skill IDs are passed through without attempting file materialization.
   - E2E test that a supported Claude provider/runtime accepts a pre-built skill by name.
   - E2E test that unsupported providers fail before the agent run starts.
