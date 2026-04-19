import test from 'node:test';
import assert from 'node:assert/strict';
import { toFile } from '@anthropic-ai/sdk';
import {
  agentBody,
  assertFound,
  collectAsyncIterable,
  createCleanup,
  sdkClient,
  skipReason,
  suffix,
} from './helpers.mjs';

async function skillFile(path, body) {
  return toFile(Buffer.from(body, 'utf8'), path, { type: 'text/markdown' });
}

test('skills support custom upload, versions, listing, deletion, and agent attachment', { skip: skipReason }, async (t) => {
  const client = sdkClient();
  const runID = suffix();
  const cleanup = createCleanup();
  t.after(() => cleanup.run());

  const skill = await client.beta.skills.create({
    display_title: `SDK Compat Skill ${runID}`,
    files: [
      await skillFile(`sdk-skill-${runID}/SKILL.md`, [
        '---',
        'name: sdk-compat-skill',
        'description: Use this skill in SDK compatibility tests.',
        '---',
        '',
        'Follow the SDK compatibility test instructions.',
        '',
      ].join('\n')),
      await skillFile(`sdk-skill-${runID}/examples/example.md`, 'Example input for the SDK compatibility skill.\n'),
    ],
  });
  cleanup.add(() => client.beta.skills.delete(skill.id));

  assert.equal(skill.type, 'skill');
  assert.equal(skill.source, 'custom');
  assert(skill.latest_version);

  const retrieved = await client.beta.skills.retrieve(skill.id);
  assert.equal(retrieved.id, skill.id);
  assert.equal(retrieved.latest_version, skill.latest_version);

  const version = await client.beta.skills.versions.retrieve(skill.latest_version, { skill_id: skill.id });
  assert.equal(version.skill_id, skill.id);
  assert.equal(version.version, skill.latest_version);

  const secondVersion = await client.beta.skills.versions.create(skill.id, {
    files: [
      await skillFile(`sdk-skill-${runID}/SKILL.md`, [
        '---',
        'name: sdk-compat-skill',
        'description: Use this updated skill in SDK compatibility tests.',
        '---',
        '',
        'Follow the updated SDK compatibility test instructions.',
        '',
      ].join('\n')),
    ],
  });
  assert.equal(secondVersion.skill_id, skill.id);
  assert.notEqual(secondVersion.version, skill.latest_version);

  const versions = await collectAsyncIterable(client.beta.skills.versions.list(skill.id, { limit: 10 }), 20);
  assertFound(versions, (item) => item.version === skill.latest_version, 'initial skill version');
  assertFound(versions, (item) => item.version === secondVersion.version, 'second skill version');

  const listed = await collectAsyncIterable(client.beta.skills.list({ limit: 10, source: 'custom' }), 20);
  assertFound(listed, (item) => item.id === skill.id, 'created skill');

  const anthropicSkills = await collectAsyncIterable(client.beta.skills.list({ limit: 10, source: 'anthropic' }), 20);
  assert(Array.isArray(anthropicSkills));

  const agent = await client.beta.agents.create(agentBody(runID, {
    skills: [{ type: 'custom', skill_id: skill.id, version: secondVersion.version }],
  }));
  cleanup.add(() => client.beta.agents.archive(agent.id));
  assert.equal(agent.skills[0].type, 'custom');
  assert.equal(agent.skills[0].skill_id, skill.id);
  assert.equal(agent.skills[0].version, secondVersion.version);

  const deletedVersion = await client.beta.skills.versions.delete(skill.latest_version, { skill_id: skill.id });
  assert.equal(deletedVersion.id, skill.latest_version);
  assert.equal(deletedVersion.type, 'skill_version_deleted');
});
