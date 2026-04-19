import fs from 'node:fs';
import path from 'node:path';
import { fileURLToPath } from 'node:url';

const scriptDir = path.dirname(fileURLToPath(import.meta.url));
const sdkCompatDir = path.resolve(scriptDir, '..');
const repoRoot = path.resolve(sdkCompatDir, '..', '..');
const specPath = path.join(repoRoot, 'internal/apicontract/generated/managed-agent-openapi.codegen.yaml');
const requirementsPath = path.join(sdkCompatDir, 'coverage-manifest/requirements.manual.json');
const manifestPath = path.join(sdkCompatDir, 'coverage-manifest/manifest.json');
const jsonReportPath = path.join(sdkCompatDir, 'coverage.generated.json');
const markdownReportPath = path.join(sdkCompatDir, 'coverage.generated.md');

const mode = parseMode(process.argv.slice(2));
const operationTargets = readOperationTargets(specPath);
const manualTargets = readJSON(requirementsPath).targets ?? [];
const manifest = readJSON(manifestPath);
const expectedTargets = buildExpectedTargets(operationTargets, manualTargets);
const testIndex = buildTestIndex(sdkCompatDir);
const coveredByTarget = new Map();

validateManifest(manifest, expectedTargets, testIndex, coveredByTarget);

const report = buildReport(expectedTargets, coveredByTarget, manifest.minimum_coverage_percent);
const jsonReport = `${JSON.stringify(report, null, 2)}\n`;
const markdownReport = renderMarkdown(report);

if (mode === 'write') {
  fs.writeFileSync(jsonReportPath, jsonReport);
  fs.writeFileSync(markdownReportPath, markdownReport);
} else {
  assertFileMatches(jsonReportPath, jsonReport);
  assertFileMatches(markdownReportPath, markdownReport);
}

if (report.summary.coverage_percent < report.summary.minimum_coverage_percent) {
  fail(`SDK compatibility coverage ${report.summary.coverage_percent.toFixed(2)}% is below the ${report.summary.minimum_coverage_percent}% minimum`);
}

if (report.uncovered_critical_targets.length > 0) {
  fail(`Uncovered critical SDK compatibility targets:\n${report.uncovered_critical_targets.map((target) => `- ${target.id}`).join('\n')}`);
}

console.log(`SDK compatibility coverage: ${report.summary.covered_targets}/${report.summary.total_targets} (${report.summary.coverage_percent.toFixed(2)}%)`);

function parseMode(args) {
  if (args.includes('--write')) {
    return 'write';
  }
  if (args.includes('--check')) {
    return 'check';
  }
  fail('usage: node scripts/sdk-compat-coverage.mjs --write|--check');
}

function readJSON(filePath) {
  return JSON.parse(fs.readFileSync(filePath, 'utf8'));
}

function readOperationTargets(filePath) {
  const text = fs.readFileSync(filePath, 'utf8');
  const operationIDs = [...text.matchAll(/operationId:\s*([A-Za-z0-9_]+)/g)].map((match) => match[1]);
  if (operationIDs.length === 0) {
    fail(`No operationId entries found in ${filePath}`);
  }
  return operationIDs.map((operationID) => ({
    id: `operation:${operationID}`,
    category: 'operation',
    source: 'openapi',
    description: `OpenAPI operation ${operationID}`,
    critical: false,
  }));
}

function buildExpectedTargets(operationTargets, manualTargets) {
  const targets = new Map();
  for (const target of [...operationTargets, ...manualTargets]) {
    if (!target.id || !target.category) {
      fail(`Invalid coverage target: ${JSON.stringify(target)}`);
    }
    if (targets.has(target.id)) {
      fail(`Duplicate coverage target: ${target.id}`);
    }
    targets.set(target.id, {
      source: 'manual',
      description: '',
      critical: false,
      ...target,
    });
  }
  return targets;
}

function buildTestIndex(baseDir) {
  const index = new Map();
  for (const fileName of fs.readdirSync(baseDir).filter((name) => name.endsWith('.test.mjs')).sort()) {
    const relativeFile = `tests/sdk-compat/${fileName}`;
    const source = fs.readFileSync(path.join(baseDir, fileName), 'utf8');
    index.set(relativeFile, extractTestNames(source));
  }
  return index;
}

function extractTestNames(source) {
  const names = new Set();
  const testPattern = /(?:^|\n)\s*test\(\s*(['"`])([\s\S]*?)\1\s*,/g;
  for (const match of source.matchAll(testPattern)) {
    names.add(match[2]);
  }
  return names;
}

function validateManifest(manifest, expectedTargets, testIndex, coveredByTarget) {
  if (!Number.isFinite(manifest.minimum_coverage_percent)) {
    fail('coverage manifest must define minimum_coverage_percent');
  }
  if (!Array.isArray(manifest.tests)) {
    fail('coverage manifest must define tests');
  }

  const manifestTestKeys = new Set();
  for (const entry of manifest.tests) {
    if (!entry.file || !entry.name || !Array.isArray(entry.covers)) {
      fail(`Invalid manifest test entry: ${JSON.stringify(entry)}`);
    }
    if (!testIndex.has(entry.file)) {
      fail(`Manifest references unknown test file: ${entry.file}`);
    }
    if (!testIndex.get(entry.file).has(entry.name)) {
      fail(`Manifest references missing test "${entry.name}" in ${entry.file}`);
    }
    manifestTestKeys.add(`${entry.file}\0${entry.name}`);
    for (const targetID of entry.covers) {
      if (!expectedTargets.has(targetID)) {
        fail(`Manifest test "${entry.name}" references unknown coverage target: ${targetID}`);
      }
      const tests = coveredByTarget.get(targetID) ?? [];
      tests.push({ file: entry.file, name: entry.name });
      coveredByTarget.set(targetID, tests);
    }
  }

  const missingTests = [];
  for (const [file, testNames] of testIndex.entries()) {
    for (const name of testNames) {
      if (!manifestTestKeys.has(`${file}\0${name}`)) {
        missingTests.push(`${file}: ${name}`);
      }
    }
  }
  if (missingTests.length > 0) {
    fail(`SDK compat tests missing from coverage manifest:\n${missingTests.map((item) => `- ${item}`).join('\n')}`);
  }
}

function buildReport(expectedTargets, coveredByTarget, minimumCoveragePercent) {
  const targets = [...expectedTargets.values()].sort((a, b) => a.id.localeCompare(b.id));
  const coveredTargets = targets.filter((target) => coveredByTarget.has(target.id));
  const uncoveredTargets = targets.filter((target) => !coveredByTarget.has(target.id));
  const categories = new Map();

  for (const target of targets) {
    const stats = categories.get(target.category) ?? { total: 0, covered: 0 };
    stats.total += 1;
    if (coveredByTarget.has(target.id)) {
      stats.covered += 1;
    }
    categories.set(target.category, stats);
  }

  return {
    summary: {
      minimum_coverage_percent: minimumCoveragePercent,
      total_targets: targets.length,
      covered_targets: coveredTargets.length,
      uncovered_targets: uncoveredTargets.length,
      coverage_percent: percent(coveredTargets.length, targets.length),
    },
    categories: Object.fromEntries([...categories.entries()].sort(([a], [b]) => a.localeCompare(b)).map(([category, stats]) => [
      category,
      {
        ...stats,
        uncovered: stats.total - stats.covered,
        coverage_percent: percent(stats.covered, stats.total),
      },
    ])),
    uncovered_critical_targets: uncoveredTargets.filter((target) => target.critical),
    uncovered_targets: uncoveredTargets,
    covered_targets: coveredTargets.map((target) => ({
      ...target,
      tests: coveredByTarget.get(target.id),
    })),
  };
}

function renderMarkdown(report) {
  const lines = [
    '# SDK Compatibility Coverage',
    '',
    `Minimum coverage: ${report.summary.minimum_coverage_percent}%`,
    '',
    `Overall coverage: ${report.summary.covered_targets}/${report.summary.total_targets} (${report.summary.coverage_percent.toFixed(2)}%)`,
    '',
    '## Categories',
    '',
    '| Category | Covered | Total | Coverage |',
    '| --- | ---: | ---: | ---: |',
  ];

  for (const [category, stats] of Object.entries(report.categories)) {
    lines.push(`| ${category} | ${stats.covered} | ${stats.total} | ${stats.coverage_percent.toFixed(2)}% |`);
  }

  lines.push('', '## Uncovered Targets', '');
  if (report.uncovered_targets.length === 0) {
    lines.push('None.');
  } else {
    for (const target of report.uncovered_targets) {
      const marker = target.critical ? ' critical' : '';
      lines.push(`- \`${target.id}\`${marker}: ${target.description}`);
    }
  }

  lines.push('');
  return `${lines.join('\n')}`;
}

function assertFileMatches(filePath, expected) {
  if (!fs.existsSync(filePath)) {
    fail(`${path.relative(repoRoot, filePath)} is missing; run npm run coverage`);
  }
  const actual = fs.readFileSync(filePath, 'utf8');
  if (actual !== expected) {
    fail(`${path.relative(repoRoot, filePath)} is stale; run npm run coverage`);
  }
}

function percent(covered, total) {
  return total === 0 ? 100 : (covered / total) * 100;
}

function fail(message) {
  console.error(message);
  process.exit(1);
}
