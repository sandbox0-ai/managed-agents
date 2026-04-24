import test from 'node:test';
import assert from 'node:assert/strict';
import {
  assertFound,
  collectAsyncIterable,
  createCleanup,
  environmentBody,
  sdkClient,
  skipReason,
  suffix,
} from './helpers.mjs';

test('environments support cloud config, package updates, list, archive, and delete', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const environment = await client.beta.environments.create(environmentBody(runID, {
    config: {
      type: 'cloud',
      networking: {
        type: 'limited',
        allowed_hosts: ['api.example.com'],
        allow_package_managers: true,
        allow_mcp_servers: false,
      },
      packages: {
        type: 'packages',
        pip: ['six==1.17.0'],
        cargo: [],
        gem: [],
        go: [],
      },
    },
  }));
  cleanup.add(() => client.beta.environments.delete(environment.id));

  assert.equal(environment.type, 'environment');
  assert.equal(environment.config.type, 'cloud');
  assert.equal(environment.config.networking.type, 'limited');
  assert.deepEqual(environment.config.packages.pip, ['six==1.17.0']);

  const retrieved = await client.beta.environments.retrieve(environment.id);
  assert.equal(retrieved.id, environment.id);

  const updated = await client.beta.environments.update(environment.id, {
    name: `${environment.name}-updated`,
    config: {
      type: 'cloud',
      packages: {
        pip: ['colorama==0.4.6'],
      },
    },
    metadata: { sdk_compat_updated: 'true' },
  });
  assert.equal(updated.name, `${environment.name}-updated`);
  assert.equal(updated.metadata.sdk_compat_updated, 'true');
  assert.deepEqual(updated.config.packages.pip, ['colorama==0.4.6']);
  assert.deepEqual(updated.config.networking.allowed_hosts, ['api.example.com']);

  const listed = await collectAsyncIterable(client.beta.environments.list({ limit: 5 }), 20);
  assertFound(listed, (item) => item.id === environment.id, 'created environment');

  const archived = await client.beta.environments.archive(environment.id);
  assert.equal(archived.id, environment.id);
  assert(archived.archived_at);

  const archivedList = await collectAsyncIterable(client.beta.environments.list({ limit: 5, include_archived: true }), 20);
  assertFound(archivedList, (item) => item.id === environment.id && item.archived_at, 'archived environment');

  const deletable = await client.beta.environments.create(environmentBody(`${runID}-delete`));
  const deleted = await client.beta.environments.delete(deletable.id);
  assert.equal(deleted.id, deletable.id);
  assert.equal(deleted.type, 'environment_deleted');
});

test('environments preserve defaults and support partial clears through the SDK', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const environment = await client.beta.environments.create({
    name: `sdk-compat-env-defaults-${runID}`,
    description: 'Temporary description',
    metadata: {
      e2e: 'sdk-compat',
      remove_me: 'delete',
      keep_me: 'original',
      run: runID,
    },
  });
  cleanup.add(() => client.beta.environments.delete(environment.id));

  assert.equal(environment.config.type, 'cloud');
  assert.equal(environment.config.networking.type, 'unrestricted');
  assert.deepEqual(environment.config.packages.pip, []);
  assert.deepEqual(environment.config.packages.npm, []);

  const limited = await client.beta.environments.update(environment.id, {
    config: {
      type: 'cloud',
      networking: {
        type: 'limited',
        allowed_hosts: ['api.example.com'],
      },
      packages: {
        pip: ['six==1.17.0'],
      },
    },
  });
  assert.equal(limited.config.networking.type, 'limited');
  assert.deepEqual(limited.config.networking.allowed_hosts, ['api.example.com']);
  assert.equal(limited.config.networking.allow_mcp_servers, false);
  assert.equal(limited.config.networking.allow_package_managers, false);
  assert.deepEqual(limited.config.packages.pip, ['six==1.17.0']);

  const cleared = await client.beta.environments.update(environment.id, {
    description: '',
    metadata: {
      remove_me: null,
      keep_me: 'updated',
    },
    config: {
      type: 'cloud',
      networking: { type: 'unrestricted' },
      packages: {
        pip: [],
        npm: [],
      },
    },
  });
  assert.equal(cleared.description, '');
  assert.equal(cleared.metadata.keep_me, 'updated');
  assert.equal(cleared.metadata.remove_me, undefined);
  assert.equal(cleared.config.networking.type, 'unrestricted');
  assert.deepEqual(cleared.config.packages.pip, []);
  assert.deepEqual(cleared.config.packages.npm, []);
});
