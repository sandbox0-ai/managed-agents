const Anthropic = require("@anthropic-ai/sdk");

class ManagedAgentsClient {
  constructor({ baseURL, apiKey, authToken, client } = {}) {
    this.client = client || new Anthropic({
      baseURL,
      ...(authToken ? { authToken } : { apiKey }),
    });
  }

  async createSession({ agentID, environmentID, title, vaultIDs = [], metadata = {} }) {
    return await this.client.beta.sessions.create({
      agent: agentID,
      environment_id: environmentID,
      title,
      vault_ids: vaultIDs,
      metadata,
    });
  }

  async retrieveSession(sessionID) {
    return await this.client.beta.sessions.retrieve(sessionID);
  }

  async listSessions(params = {}) {
    const sessions = [];
    for await (const session of this.client.beta.sessions.list(params)) {
      sessions.push(session);
    }
    return sessions;
  }

  async listEvents(sessionID, params = {}) {
    const events = [];
    for await (const event of this.client.beta.sessions.events.list(sessionID, params)) {
      events.push(event);
    }
    return events;
  }

  async streamEvents(sessionID) {
    return await this.client.beta.sessions.events.stream(sessionID);
  }

  async sendUserMessage(sessionID, text) {
    return await this.client.beta.sessions.events.send(sessionID, {
      events: [{
        type: "user.message",
        content: [{ type: "text", text }],
      }],
    });
  }

  async sendInterrupt(sessionID) {
    return await this.client.beta.sessions.events.send(sessionID, {
      events: [{ type: "user.interrupt" }],
    });
  }

  async sendToolConfirmation(sessionID, toolUseID, result, denyMessage = "") {
    const event = {
      type: "user.tool_confirmation",
      tool_use_id: toolUseID,
      result,
    };
    if (result === "deny" && denyMessage) {
      event.deny_message = denyMessage;
    }
    return await this.client.beta.sessions.events.send(sessionID, {
      events: [event],
    });
  }
}

module.exports = { ManagedAgentsClient };
