const http = require("node:http");
const { URL } = require("node:url");
const { WebSocketServer } = require("ws");
const {
  setupRelay,
  getRelayStats,
  resolvePairingCode,
  resolveTrustedMacSession,
} = require("../vendor/remodex/relay/relay");
const { FileStore } = require("./store");
const { ManagedAgentsClient } = require("./managed-agents-client");
const { RemodexBridgeManager } = require("./remodex/bridge-manager");

const DEFAULT_ADDR = "127.0.0.1:8787";

function createAppGatewayServer({
  addr = process.env.APP_GATEWAY_HTTP_ADDR || DEFAULT_ADDR,
  stateDir = process.env.APP_GATEWAY_STATE_DIR || ".app-gateway-state",
  publicRelayURL = process.env.APP_GATEWAY_PUBLIC_RELAY_URL,
  localRelayURL = process.env.APP_GATEWAY_LOCAL_RELAY_URL,
  authToken = process.env.APP_GATEWAY_AUTH_TOKEN || "",
  managedBaseURL = process.env.MANAGED_AGENT_BASE_URL || "http://127.0.0.1:8080",
  managedAPIKey = process.env.MANAGED_AGENT_API_KEY || process.env.ANTHROPIC_API_KEY || "not-needed",
  managedAuthToken = process.env.MANAGED_AGENT_AUTH_TOKEN || "",
  managedClient = null,
  store = null,
} = {}) {
  const resolvedAddr = parseAddr(addr);
  const relayBase = publicRelayURL || `ws://${resolvedAddr.host}:${resolvedAddr.port}/relay`;
  const localRelayBase = localRelayURL || `ws://${resolvedAddr.listenHost}:${resolvedAddr.port}/relay`;
  const resolvedStore = store || new FileStore({ stateDir });
  const resolvedManagedClient = managedClient || new ManagedAgentsClient({
    baseURL: managedBaseURL,
    apiKey: managedAPIKey,
    authToken: managedAuthToken,
  });
  const remodex = new RemodexBridgeManager({
    store: resolvedStore,
    managedClient: resolvedManagedClient,
    publicRelayURL: relayBase,
    localRelayURL: localRelayBase,
  });

  const server = http.createServer((req, res) => {
    handleHTTPRequest(req, res, {
      authToken,
      store: resolvedStore,
      remodex,
    }).catch((error) => writeError(res, error));
  });

  const wss = new WebSocketServer({ noServer: true });
  setupRelay(wss);
  server.on("upgrade", (req, socket, head) => {
    const pathname = safePathname(req.url);
    if (!pathname.startsWith("/relay/")) {
      socket.destroy();
      return;
    }
    wss.handleUpgrade(req, socket, head, (ws) => {
      wss.emit("connection", ws, req);
    });
  });

  server.on("close", () => {
    remodex.stopAll();
    wss.close();
  });

  return {
    server,
    wss,
    store: resolvedStore,
    remodex,
    publicRelayURL: relayBase,
    localRelayURL: localRelayBase,
    close: () => closeGateway({ server, wss, remodex }),
  };
}

async function handleHTTPRequest(req, res, context) {
  const pathname = safePathname(req.url);
  if (req.method === "GET" && (pathname === "/healthz" || pathname === "/readyz")) {
    return writeJSON(res, 200, { ok: true, relay: getRelayStats() });
  }
  if (req.method === "POST" && pathname === "/v1/pairing/code/resolve") {
    return handleJSONRoute(req, res, resolvePairingCode);
  }
  if (req.method === "POST" && pathname === "/v1/trusted/session/resolve") {
    return handleJSONRoute(req, res, resolveTrustedMacSession);
  }

  requireAuth(req, context.authToken);

  if (req.method === "POST" && pathname === "/v1/app-bindings") {
    const body = await readJSONBody(req);
    return writeJSON(res, 201, { binding: context.store.createBinding(body) });
  }
  if (req.method === "GET" && pathname === "/v1/app-bindings") {
    return writeJSON(res, 200, { bindings: context.store.listBindings() });
  }

  const bindingMatch = pathname.match(/^\/v1\/app-bindings\/([^/]+)(?:\/(.+))?$/);
  if (bindingMatch) {
    const bindingID = decodeURIComponent(bindingMatch[1]);
    const action = bindingMatch[2] || "";
    return await handleBindingRoute(req, res, bindingID, action, context);
  }

  return writeJSON(res, 404, { ok: false, error: "Not found" });
}

async function handleBindingRoute(req, res, bindingID, action, context) {
  if (!action && req.method === "GET") {
    const binding = context.store.getBinding(bindingID);
    if (!binding) {
      return writeJSON(res, 404, { ok: false, error: "app binding not found" });
    }
    return writeJSON(res, 200, { binding });
  }
  if (!action && req.method === "PUT") {
    const body = await readJSONBody(req);
    const binding = context.store.updateBinding(bindingID, body);
    if (!binding) {
      return writeJSON(res, 404, { ok: false, error: "app binding not found" });
    }
    return writeJSON(res, 200, { binding });
  }
  if (!action && req.method === "DELETE") {
    context.remodex.stop(bindingID);
    const deleted = context.store.deleteBinding(bindingID);
    return writeJSON(res, deleted ? 200 : 404, { ok: deleted });
  }
  if (action === "remodex/start" && req.method === "POST") {
    return writeJSON(res, 200, context.remodex.start(bindingID));
  }
  if (action === "remodex/status" && req.method === "GET") {
    return writeJSON(res, 200, context.remodex.status(bindingID));
  }
  if (action === "remodex/stop" && req.method === "POST") {
    return writeJSON(res, 200, { ok: context.remodex.stop(bindingID) });
  }
  return writeJSON(res, 404, { ok: false, error: "Not found" });
}

function requireAuth(req, expectedToken) {
  if (!expectedToken) {
    return;
  }
  const authorization = String(req.headers.authorization || "");
  if (authorization !== `Bearer ${expectedToken}`) {
    throw Object.assign(new Error("Unauthorized"), { status: 401 });
  }
}

async function handleJSONRoute(req, res, handler) {
  try {
    const body = await readJSONBody(req);
    return writeJSON(res, 200, await handler(body));
  } catch (error) {
    return writeError(res, error);
  }
}

function readJSONBody(req) {
  return new Promise((resolve, reject) => {
    const chunks = [];
    let totalSize = 0;
    req.on("data", (chunk) => {
      totalSize += chunk.length;
      if (totalSize > 1024 * 1024) {
        reject(Object.assign(new Error("Request body too large"), { status: 413 }));
        req.destroy();
        return;
      }
      chunks.push(chunk);
    });
    req.on("end", () => {
      const rawBody = Buffer.concat(chunks).toString("utf8");
      if (!rawBody.trim()) {
        resolve({});
        return;
      }
      try {
        resolve(JSON.parse(rawBody));
      } catch {
        reject(Object.assign(new Error("Invalid JSON body"), { status: 400 }));
      }
    });
    req.on("error", reject);
  });
}

function writeJSON(res, status, body) {
  res.statusCode = status;
  res.setHeader("content-type", "application/json");
  res.end(JSON.stringify(body));
}

function writeError(res, error) {
  return writeJSON(res, error?.status || 500, {
    ok: false,
    error: error?.message || "Internal server error",
    code: error?.code || "request_failed",
  });
}

function safePathname(rawURL) {
  try {
    return new URL(rawURL || "/", "http://app-gateway.local").pathname;
  } catch {
    return "/";
  }
}

function parseAddr(addr) {
  const raw = String(addr || DEFAULT_ADDR).trim();
  const match = raw.match(/^(.*):(\d+)$/);
  const hostPart = match ? match[1] : "127.0.0.1";
  const host = hostPart.replace(/^\[|\]$/g, "") || "0.0.0.0";
  const port = Number(match?.[2] || 8787);
  const publicHost = host === "0.0.0.0" || host === "::" ? "127.0.0.1" : host;
  return {
    host: publicHost,
    listenHost: publicHost,
    rawHost: host,
    port,
  };
}

function closeGateway({ server, wss, remodex }) {
  remodex.stopAll();
  for (const client of wss.clients) {
    client.terminate();
  }
  return new Promise((resolve) => {
    wss.close(() => {
      if (!server.listening) {
        resolve();
        return;
      }
      server.close(() => resolve());
    });
  });
}

if (require.main === module) {
  const addr = parseAddr(process.env.APP_GATEWAY_HTTP_ADDR || DEFAULT_ADDR);
  const gateway = createAppGatewayServer();
  gateway.server.listen(addr.port, addr.rawHost, () => {
    console.log(`[app-gateway] listening on ${addr.rawHost}:${addr.port}`);
    console.log(`[app-gateway] remodex relay ${gateway.publicRelayURL}`);
  });
}

module.exports = {
  closeGateway,
  createAppGatewayServer,
  parseAddr,
  readJSONBody,
};
