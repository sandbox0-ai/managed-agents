import test from 'node:test';
import assert from 'node:assert/strict';
import {
  assertFound,
  collectAsyncIterable,
  createCleanup,
  llmVaultBody,
  sdkClient,
  skipReason,
  suffix,
} from './helpers.mjs';

test('vaults and credentials cover static bearer and MCP OAuth credential lifecycles', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const vault = await client.beta.vaults.create(llmVaultBody(runID));
  cleanup.add(() => client.beta.vaults.delete(vault.id));
  assert.equal(vault.type, 'vault');

  const staticCredential = await client.beta.vaults.credentials.create(vault.id, {
    display_name: `static bearer ${runID}`,
    auth: {
      type: 'static_bearer',
      token: 'secret-token',
      mcp_server_url: 'https://mcp.example.com/sse',
    },
    metadata: { kind: 'static' },
  });
  assert.equal(staticCredential.type, 'vault_credential');
  assert.equal(staticCredential.auth.type, 'static_bearer');
  assert.equal(staticCredential.auth.mcp_server_url, 'https://mcp.example.com/sse');

  const oauthCredential = await client.beta.vaults.credentials.create(vault.id, {
    display_name: `oauth ${runID}`,
    auth: {
      type: 'mcp_oauth',
      access_token: 'access-token',
      mcp_server_url: 'https://oauth-mcp.example.com/sse',
      expires_at: '2030-01-01T00:00:00Z',
      refresh: {
        client_id: 'client-id',
        refresh_token: 'refresh-token',
        token_endpoint: 'https://oauth.example.com/token',
        token_endpoint_auth: {
          type: 'client_secret_post',
          client_secret: 'client-secret',
        },
        scope: 'read',
      },
    },
    metadata: { kind: 'oauth' },
  });
  assert.equal(oauthCredential.auth.type, 'mcp_oauth');
  assert.equal(oauthCredential.auth.refresh.token_endpoint_auth.type, 'client_secret_post');
  assert.equal(oauthCredential.auth.refresh.token_endpoint_auth.client_secret, undefined);

  const updatedStatic = await client.beta.vaults.credentials.update(staticCredential.id, {
    vault_id: vault.id,
    display_name: 'updated static bearer',
    auth: { type: 'static_bearer', token: 'updated-secret-token' },
    metadata: { updated: 'true' },
  });
  assert.equal(updatedStatic.display_name, 'updated static bearer');
  assert.equal(updatedStatic.auth.mcp_server_url, 'https://mcp.example.com/sse');

  const listedCredentials = await collectAsyncIterable(client.beta.vaults.credentials.list(vault.id, { limit: 10 }), 20);
  assertFound(listedCredentials, (item) => item.id === staticCredential.id, 'static credential');
  assertFound(listedCredentials, (item) => item.id === oauthCredential.id, 'oauth credential');

  const updatedVault = await client.beta.vaults.update(vault.id, {
    display_name: `${vault.display_name}-updated`,
    metadata: { sdk_compat_updated: 'true' },
  });
  assert.equal(updatedVault.display_name, `${vault.display_name}-updated`);

  const listedVaults = await collectAsyncIterable(client.beta.vaults.list({ limit: 5 }), 20);
  assertFound(listedVaults, (item) => item.id === vault.id, 'created vault');

  const archivedCredential = await client.beta.vaults.credentials.archive(oauthCredential.id, { vault_id: vault.id });
  assert.equal(archivedCredential.id, oauthCredential.id);
  assert(archivedCredential.archived_at);

  const deletedCredential = await client.beta.vaults.credentials.delete(staticCredential.id, { vault_id: vault.id });
  assert.equal(deletedCredential.id, staticCredential.id);
  assert.equal(deletedCredential.type, 'vault_credential_deleted');

  const archivedVault = await client.beta.vaults.archive(vault.id);
  assert.equal(archivedVault.id, vault.id);
  assert(archivedVault.archived_at);
});
