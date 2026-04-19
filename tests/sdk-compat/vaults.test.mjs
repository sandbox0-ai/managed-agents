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
  assert.equal((await client.beta.vaults.retrieve(vault.id)).id, vault.id);

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
  assert.equal((await client.beta.vaults.credentials.retrieve(staticCredential.id, { vault_id: vault.id })).id, staticCredential.id);

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

  const oauthNoneCredential = await client.beta.vaults.credentials.create(vault.id, {
    display_name: `oauth none ${runID}`,
    auth: {
      type: 'mcp_oauth',
      access_token: 'access-token-none',
      mcp_server_url: 'https://oauth-none-mcp.example.com/sse',
      refresh: {
        client_id: 'client-id-none',
        refresh_token: 'refresh-token-none',
        token_endpoint: 'https://oauth-none.example.com/token',
        token_endpoint_auth: { type: 'none' },
      },
    },
    metadata: { kind: 'oauth-none' },
  });
  assert.equal(oauthNoneCredential.auth.refresh.token_endpoint_auth.type, 'none');

  const updatedStatic = await client.beta.vaults.credentials.update(staticCredential.id, {
    vault_id: vault.id,
    display_name: 'updated static bearer',
    auth: { type: 'static_bearer', token: 'updated-secret-token' },
    metadata: { updated: 'true' },
  });
  assert.equal(updatedStatic.display_name, 'updated static bearer');
  assert.equal(updatedStatic.auth.mcp_server_url, 'https://mcp.example.com/sse');
  assert.equal(updatedStatic.metadata.updated, 'true');

  await assert.rejects(
    () => client.beta.vaults.credentials.update(staticCredential.id, {
      vault_id: vault.id,
      auth: {
        type: 'static_bearer',
        token: 'updated-secret-token',
        mcp_server_url: 'https://different.example.com/sse',
      },
    }),
    /mcp_server_url|unknown|immutable|invalid/i,
  );

  const updatedOAuth = await client.beta.vaults.credentials.update(oauthCredential.id, {
    vault_id: vault.id,
    auth: {
      type: 'mcp_oauth',
      access_token: 'updated-access-token',
      expires_at: '2030-02-01T00:00:00Z',
      refresh: {
        refresh_token: 'updated-refresh-token',
        scope: null,
        token_endpoint_auth: {
          type: 'client_secret_basic',
          client_secret: 'updated-client-secret',
        },
      },
    },
    metadata: {
      kind: null,
      updated: 'oauth',
    },
  });
  assert.equal(updatedOAuth.auth.type, 'mcp_oauth');
  assert.equal(updatedOAuth.auth.expires_at, '2030-02-01T00:00:00Z');
  assert.equal(updatedOAuth.auth.refresh.scope, undefined);
  assert.equal(updatedOAuth.auth.refresh.token_endpoint_auth.type, 'client_secret_basic');
  assert.equal(updatedOAuth.auth.refresh.token_endpoint_auth.client_secret, undefined);
  assert.equal(updatedOAuth.metadata.kind, undefined);
  assert.equal(updatedOAuth.metadata.updated, 'oauth');

  const listedCredentials = await collectAsyncIterable(client.beta.vaults.credentials.list(vault.id, { limit: 10 }), 20);
  assertFound(listedCredentials, (item) => item.id === staticCredential.id, 'static credential');
  assertFound(listedCredentials, (item) => item.id === oauthCredential.id, 'oauth credential');

  const updatedVault = await client.beta.vaults.update(vault.id, {
    display_name: `${vault.display_name}-updated`,
    metadata: {
      run: null,
      sdk_compat_updated: 'true',
    },
  });
  assert.equal(updatedVault.display_name, `${vault.display_name}-updated`);
  assert.equal(updatedVault.metadata.run, undefined);
  assert.equal(updatedVault.metadata.sdk_compat_updated, 'true');

  const listedVaults = await collectAsyncIterable(client.beta.vaults.list({ limit: 5 }), 20);
  assertFound(listedVaults, (item) => item.id === vault.id, 'created vault');

  const archivedCredential = await client.beta.vaults.credentials.archive(oauthCredential.id, { vault_id: vault.id });
  assert.equal(archivedCredential.id, oauthCredential.id);
  assert(archivedCredential.archived_at);

  const archivedCredentials = await collectAsyncIterable(client.beta.vaults.credentials.list(vault.id, {
    include_archived: true,
    limit: 10,
  }), 20);
  assertFound(archivedCredentials, (item) => item.id === oauthCredential.id && item.archived_at, 'archived credential');

  const deletedCredential = await client.beta.vaults.credentials.delete(staticCredential.id, { vault_id: vault.id });
  assert.equal(deletedCredential.id, staticCredential.id);
  assert.equal(deletedCredential.type, 'vault_credential_deleted');

  const archivedVault = await client.beta.vaults.archive(vault.id);
  assert.equal(archivedVault.id, vault.id);
  assert(archivedVault.archived_at);

  const archivedVaults = await collectAsyncIterable(client.beta.vaults.list({
    include_archived: true,
    limit: 10,
  }), 20);
  assertFound(archivedVaults, (item) => item.id === vault.id && item.archived_at, 'archived vault');
});
