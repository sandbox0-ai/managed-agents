const os = require("node:os");
const { EventEmitter } = require("node:events");
const { randomBytes, randomUUID, generateKeyPairSync } = require("node:crypto");
const WebSocket = require("ws");
const { createBridgeSecureTransport } = require("../../vendor/remodex/bridge-src/secure-transport");
const {
  SHORT_PAIRING_CODE_LENGTH,
  createShortPairingCode,
} = require("../../vendor/remodex/bridge-src/qr");
const { ManagedCodexTransport } = require("./managed-codex-transport");

const RECONNECT_BASE_DELAY_MS = 500;
const RECONNECT_MAX_DELAY_MS = 10_000;

class RemodexBridgeSession extends EventEmitter {
  constructor({
    binding,
    store,
    managedClient,
    publicRelayURL,
    localRelayURL,
    machineName = os.hostname(),
  }) {
    super();
    this.binding = binding;
    this.store = store;
    this.managedClient = managedClient;
    this.publicRelayURL = trimTrailingSlash(publicRelayURL);
    this.localRelayURL = trimTrailingSlash(localRelayURL || publicRelayURL);
    this.machineName = machineName || "Sandbox0 Managed Agent";
    this.sessionID = `remodex_${randomUUID()}`;
    this.notificationSecret = randomBytes(24).toString("hex");
    this.stopped = false;
    this.reconnectAttempt = 0;
    this.reconnectTimer = null;
    this.socket = null;
    this.lastError = "";
    this.connectionState = "starting";

    this.deviceState = loadOrCreateDeviceState(store, binding.id);
    this.pairingSession = null;
    this.secureTransport = createBridgeSecureTransport({
      sessionId: this.sessionID,
      relayUrl: this.publicRelayURL,
      deviceState: this.deviceState,
      rememberTrustedPhoneImpl: (currentDeviceState, phoneDeviceID, phonePublicKey) => {
        return rememberTrustedPhone(this.store, this.binding.id, currentDeviceState, phoneDeviceID, phonePublicKey);
      },
      onTrustedPhoneUpdate: (nextDeviceState) => {
        this.deviceState = this.store.setRemodexDevice(this.binding.id, nextDeviceState);
        this.sendRelayRegistrationUpdate();
      },
    });

    this.codexTransport = new ManagedCodexTransport({
      managedClient,
      store,
      binding,
    });
    this.codexTransport.on("message", (message) => {
      this.secureTransport.queueOutboundApplicationMessage(message, (wireMessage) => this.sendRelayWireMessage(wireMessage));
    });
    this.codexTransport.on("started", (event) => this.emit("managed-started", event));
  }

  start() {
    if (this.stopped) {
      throw new Error("Remodex bridge session has already been stopped");
    }
    this.refreshPairingSession();
    this.connect();
    return this.describePairingSession();
  }

  refreshPairingSession() {
    const pairingPayload = this.secureTransport.createPairingPayload();
    this.pairingSession = {
      pairingPayload,
      pairingCode: createShortPairingCode({ length: SHORT_PAIRING_CODE_LENGTH }),
    };
    if (this.socket?.readyState === WebSocket.OPEN) {
      this.sendRelayRegistrationUpdate();
    }
    return this.describePairingSession();
  }

  status() {
    return {
      binding_id: this.binding.id,
      relay_session_id: this.sessionID,
      connection_state: this.connectionState,
      secure_channel_ready: this.secureTransport.isSecureChannelReady(),
      pairing_expires_at: this.pairingSession?.pairingPayload?.expiresAt || null,
      last_error: this.lastError,
    };
  }

  stop() {
    this.stopped = true;
    this.connectionState = "stopped";
    if (this.reconnectTimer) {
      clearTimeout(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.socket?.readyState === WebSocket.OPEN || this.socket?.readyState === WebSocket.CONNECTING) {
      this.socket.close(1000, "App Gateway stopped");
    }
    this.socket = null;
    this.codexTransport.shutdown();
  }

  connect() {
    if (this.stopped) {
      return;
    }
    const url = `${this.localRelayURL}/${encodeURIComponent(this.sessionID)}`;
    const socket = new WebSocket(url, {
      headers: {
        "x-role": "mac",
        "x-notification-secret": this.notificationSecret,
        ...buildMacRegistrationHeaders(this.deviceState, this.pairingSession, this.machineName),
      },
    });
    this.socket = socket;
    this.connectionState = "connecting";

    socket.on("open", () => {
      this.reconnectAttempt = 0;
      this.connectionState = "connected";
      this.lastError = "";
      this.secureTransport.bindLiveSendWireMessage((wireMessage) => this.sendRelayWireMessage(wireMessage));
      this.sendRelayRegistrationUpdate();
      this.emit("connected", this.status());
    });

    socket.on("message", (data) => {
      const rawMessage = typeof data === "string" ? data : data.toString("utf8");
      const handled = this.secureTransport.handleIncomingWireMessage(rawMessage, {
        sendControlMessage: (message) => this.sendRelayWireMessage(JSON.stringify(message)),
        onApplicationMessage: (message) => this.codexTransport.send(message),
      });
      if (!handled) {
        this.lastError = "Received an unsupported Remodex relay message.";
      }
    });

    socket.on("close", () => {
      if (this.socket === socket) {
        this.socket = null;
      }
      if (!this.stopped) {
        this.connectionState = "disconnected";
        this.scheduleReconnect();
      }
    });

    socket.on("error", (error) => {
      this.lastError = error.message || String(error);
      this.connectionState = "error";
      this.emit("error", error);
    });
  }

  sendRelayWireMessage(wireMessage) {
    if (this.socket?.readyState !== WebSocket.OPEN) {
      return false;
    }
    this.socket.send(wireMessage);
    return true;
  }

  sendRelayRegistrationUpdate() {
    if (this.socket?.readyState !== WebSocket.OPEN || !this.pairingSession) {
      return;
    }
    this.socket.send(JSON.stringify({
      kind: "relayMacRegistration",
      registration: buildMacRegistration(this.deviceState, this.pairingSession, this.machineName),
    }));
  }

  scheduleReconnect() {
    if (this.reconnectTimer || this.stopped) {
      return;
    }
    const delay = Math.min(
      RECONNECT_MAX_DELAY_MS,
      RECONNECT_BASE_DELAY_MS * (2 ** Math.min(this.reconnectAttempt, 5))
    );
    this.reconnectAttempt += 1;
    this.reconnectTimer = setTimeout(() => {
      this.reconnectTimer = null;
      this.connect();
    }, delay);
    this.reconnectTimer.unref?.();
  }

  describePairingSession() {
    return {
      binding: this.binding,
      pairing_payload: this.pairingSession?.pairingPayload || null,
      pairing_code: this.pairingSession?.pairingCode || "",
      relay_session_id: this.sessionID,
      relay_url: this.publicRelayURL,
    };
  }
}

function loadOrCreateDeviceState(store, bindingID) {
  const existing = store.getRemodexDevice(bindingID);
  if (isValidDeviceState(existing)) {
    return existing;
  }
  const { publicKey, privateKey } = generateKeyPairSync("ed25519");
  const publicJwk = publicKey.export({ format: "jwk" });
  const privateJwk = privateKey.export({ format: "jwk" });
  const deviceState = {
    version: 1,
    macDeviceId: randomUUID(),
    macIdentityPublicKey: base64UrlToBase64(publicJwk.x),
    macIdentityPrivateKey: base64UrlToBase64(privateJwk.d),
    trustedPhones: {},
    lastSeenPhoneAppVersion: null,
  };
  return store.setRemodexDevice(bindingID, deviceState);
}

function rememberTrustedPhone(store, bindingID, currentDeviceState, phoneDeviceID, phonePublicKey) {
  const next = {
    ...currentDeviceState,
    trustedPhones: {
      ...(currentDeviceState.trustedPhones || {}),
      [phoneDeviceID]: phonePublicKey,
    },
  };
  return store.setRemodexDevice(bindingID, next);
}

function isValidDeviceState(value) {
  return Boolean(
    value
      && typeof value === "object"
      && typeof value.macDeviceId === "string"
      && typeof value.macIdentityPublicKey === "string"
      && typeof value.macIdentityPrivateKey === "string"
  );
}

function buildMacRegistrationHeaders(deviceState, pairingSession, machineName) {
  const registration = buildMacRegistration(deviceState, pairingSession, machineName);
  const headers = {
    "x-mac-device-id": registration.macDeviceId,
    "x-mac-identity-public-key": registration.macIdentityPublicKey,
    "x-machine-name": registration.displayName,
    "x-pairing-code": registration.pairingCode,
    "x-pairing-version": registration.pairingVersion ? String(registration.pairingVersion) : "",
    "x-pairing-expires-at": registration.pairingExpiresAt ? String(registration.pairingExpiresAt) : "",
  };
  if (registration.trustedPhoneDeviceId && registration.trustedPhonePublicKey) {
    headers["x-trusted-phone-device-id"] = registration.trustedPhoneDeviceId;
    headers["x-trusted-phone-public-key"] = registration.trustedPhonePublicKey;
  }
  return headers;
}

function buildMacRegistration(deviceState, pairingSession, machineName) {
  const trustedPhoneEntry = Object.entries(deviceState?.trustedPhones || {})[0] || null;
  return {
    macDeviceId: normalizeNonEmptyString(deviceState?.macDeviceId),
    macIdentityPublicKey: normalizeNonEmptyString(deviceState?.macIdentityPublicKey),
    displayName: normalizeNonEmptyString(machineName),
    trustedPhoneDeviceId: normalizeNonEmptyString(trustedPhoneEntry?.[0]),
    trustedPhonePublicKey: normalizeNonEmptyString(trustedPhoneEntry?.[1]),
    pairingCode: normalizeNonEmptyString(pairingSession?.pairingCode),
    pairingVersion: Number.isInteger(pairingSession?.pairingPayload?.v) ? pairingSession.pairingPayload.v : 0,
    pairingExpiresAt: Number.isFinite(pairingSession?.pairingPayload?.expiresAt)
      ? pairingSession.pairingPayload.expiresAt
      : 0,
  };
}

function trimTrailingSlash(value) {
  return String(value || "").trim().replace(/\/+$/g, "");
}

function normalizeNonEmptyString(value) {
  return typeof value === "string" && value.trim() ? value.trim() : "";
}

function base64UrlToBase64(value) {
  const normalized = String(value || "").replaceAll("-", "+").replaceAll("_", "/");
  return normalized.padEnd(normalized.length + ((4 - (normalized.length % 4)) % 4), "=");
}

module.exports = {
  RemodexBridgeSession,
  buildMacRegistration,
  buildMacRegistrationHeaders,
  loadOrCreateDeviceState,
};
