const { RemodexBridgeSession } = require("./bridge-session");

class RemodexBridgeManager {
  constructor({ store, managedClient, publicRelayURL, localRelayURL, machineName }) {
    this.store = store;
    this.managedClient = managedClient;
    this.publicRelayURL = publicRelayURL;
    this.localRelayURL = localRelayURL;
    this.machineName = machineName;
    this.sessionsByBindingID = new Map();
  }

  start(bindingID) {
    const binding = this.store.getBinding(bindingID);
    if (!binding) {
      throw Object.assign(new Error("app binding not found"), { status: 404 });
    }
    if (binding.app !== "remodex") {
      throw Object.assign(new Error("binding is not a Remodex binding"), { status: 400 });
    }

    const existing = this.sessionsByBindingID.get(binding.id);
    if (existing && existing.status().connection_state !== "stopped") {
      return existing.refreshPairingSession();
    }

    const session = new RemodexBridgeSession({
      binding,
      store: this.store,
      managedClient: this.managedClient,
      publicRelayURL: this.publicRelayURL,
      localRelayURL: this.localRelayURL,
      machineName: this.machineName,
    });
    session.on("error", () => {});
    this.sessionsByBindingID.set(binding.id, session);
    return session.start();
  }

  status(bindingID) {
    const binding = this.store.getBinding(bindingID);
    if (!binding) {
      throw Object.assign(new Error("app binding not found"), { status: 404 });
    }
    const session = this.sessionsByBindingID.get(binding.id);
    if (!session) {
      return {
        binding_id: binding.id,
        connection_state: "not_started",
        secure_channel_ready: false,
        pairing_expires_at: null,
        last_error: "",
      };
    }
    return session.status();
  }

  stop(bindingID) {
    const session = this.sessionsByBindingID.get(String(bindingID || "").trim());
    if (!session) {
      return false;
    }
    session.stop();
    this.sessionsByBindingID.delete(String(bindingID || "").trim());
    return true;
  }

  stopAll() {
    for (const session of this.sessionsByBindingID.values()) {
      session.stop();
    }
    this.sessionsByBindingID.clear();
  }
}

module.exports = { RemodexBridgeManager };
