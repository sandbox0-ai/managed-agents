function nowSeconds() {
  return Math.floor(Date.now() / 1000);
}

function threadObject({ id, name = null, preview = "", status = "idle", turns = [] }) {
  const now = nowSeconds();
  return {
    id,
    forkedFromId: null,
    preview,
    ephemeral: false,
    modelProvider: "sandbox0-managed-agents",
    createdAt: now,
    updatedAt: now,
    status,
    path: null,
    cwd: "/workspace",
    cliVersion: "sandbox0-app-gateway",
    source: "app_server",
    agentNickname: null,
    agentRole: null,
    gitInfo: null,
    name,
    turns,
  };
}

function turnObject({ id, status = "running", items = [], startedAt = nowSeconds(), completedAt = null, error = null }) {
  return {
    id,
    items,
    status,
    error,
    startedAt,
    completedAt,
    durationMs: null,
  };
}

function threadStartResponse(thread) {
  return {
    thread,
    model: "managed-agent",
    modelProvider: "sandbox0-managed-agents",
    serviceTier: null,
    cwd: "/workspace",
    approvalPolicy: "onRequest",
    approvalsReviewer: "codex",
    sandbox: {
      mode: "workspaceWrite",
      writableRoots: ["/workspace"],
      networkAccess: true,
      readOnlyAccess: { mode: "fullAccess" },
    },
    reasoningEffort: null,
  };
}

function agentMessageItem(id, text, phase = "final_answer") {
  return {
    type: "agentMessage",
    id,
    text,
    phase,
    memoryCitation: null,
  };
}

function reasoningItem(id, summary = []) {
  return {
    type: "reasoning",
    id,
    summary,
    content: [],
  };
}

function commandExecutionItem(id, input = {}, status = "completed") {
  return {
    type: "commandExecution",
    id,
    command: String(input.command || input.cmd || input.description || input.name || "tool"),
    cwd: String(input.cwd || "/workspace"),
    processId: null,
    source: "exec",
    status,
    commandActions: [],
    aggregatedOutput: null,
    exitCode: status === "failed" ? 1 : 0,
    durationMs: null,
  };
}

function fileChangeItem(id, input = {}, status = "completed") {
  return {
    type: "fileChange",
    id,
    changes: Array.isArray(input.changes) ? input.changes : [],
    status,
  };
}

function dynamicToolCallItem(id, tool, input = {}, status = "completed", success = true) {
  return {
    type: "dynamicToolCall",
    id,
    tool,
    arguments: input,
    status,
    contentItems: null,
    success,
    durationMs: null,
  };
}

module.exports = {
  agentMessageItem,
  commandExecutionItem,
  dynamicToolCallItem,
  fileChangeItem,
  nowSeconds,
  reasoningItem,
  threadObject,
  threadStartResponse,
  turnObject,
};
