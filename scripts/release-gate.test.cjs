'use strict';

// Static policy regressions for our actual workflow definitions. Actionlint
// separately validates full YAML and GitHub expressions. These tests exercise
// the small expression subset below, not a general GitHub Actions emulator.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const root = path.join(__dirname, '..');
const read = (name) => fs.readFileSync(path.join(root, '.github/workflows', name), 'utf8').replace(/\r\n/g, '\n');
const windows = read('windows-release.yml');
const ci = read('ci.yml');
const security = read('security.yml');

function job(source, name) {
  const marker = `\n  ${name}:\n`;
  const start = source.indexOf(marker);
  assert.ok(start >= 0, `missing job ${name}`);
  const remainder = source.slice(start + marker.length);
  const next = remainder.search(/^  [a-zA-Z0-9_-]+:\n/m);
  return next < 0 ? remainder : remainder.slice(0, next);
}

const publish = job(windows, 'publish');
const expressionMatch = publish.match(/^    if: >-\n((?:      .+\n)+)/m);
assert.ok(expressionMatch, 'publication uses an explicit, inspectable folded gate');
const expression = expressionMatch[1].trim().replace(/\n\s*/g, ' ').replace(/(?<!=)==(?!=)/g, '===');
const gate = new vm.Script(`Boolean(${expression})`);

function permits(event, ref, ciResult = 'success', securityResult = 'success', packageResult = 'success') {
  return gate.runInNewContext({
    github: { event_name: event, ref },
    needs: { quality_ci: { result: ciResult }, quality_security: { result: securityResult }, package: { result: packageResult } },
    startsWith: (value, prefix) => value.startsWith(prefix),
  }, { timeout: 100 });
}

test('publication needs all three jobs and cannot override failure handling', () => {
  const dependencies = publish.match(/^    needs: \[([^\]]+)\]$/m);
  assert.ok(dependencies);
  assert.deepEqual(dependencies[1].split(',').map((x) => x.trim()).sort(), ['package', 'quality_ci', 'quality_security']);
  assert.doesNotMatch(publish, /always\(|continue-on-error:/);
  assert.match(publish, /^      contents: write$/m);
});

test('every failed, cancelled, skipped or missing required outcome prevents publication', () => {
  for (const state of ['failure', 'cancelled', 'skipped', 'timed_out', 'action_required', 'neutral', '']) {
    assert.equal(permits('push', 'refs/heads/master', state), false, `CI ${state}`);
    assert.equal(permits('push', 'refs/heads/master', 'success', state), false, `security ${state}`);
    assert.equal(permits('push', 'refs/heads/master', 'success', 'success', state), false, `package ${state}`);
  }
});

test('only successful master/version-tag push or dispatch runs may publish', () => {
  for (const event of ['push', 'workflow_dispatch']) {
    for (const ref of ['refs/heads/master', 'refs/tags/v0.11.0']) assert.equal(permits(event, ref), true);
    for (const ref of ['refs/heads/codex/test', 'refs/tags/windows-latest', 'refs/pull/30/merge', '']) assert.equal(permits(event, ref), false);
  }
  for (const event of ['pull_request', 'pull_request_target', 'workflow_run', 'schedule', '']) {
    assert.equal(permits(event, 'refs/heads/master'), false);
    assert.equal(permits(event, 'refs/tags/v0.11.0'), false);
  }
});

test('quality jobs unconditionally call the same-commit definitions with read-only permissions and no secrets', () => {
  for (const [name, filename, core] of [['quality_ci', 'ci.yml', 'test'], ['quality_security', 'security.yml', 'security']]) {
    const caller = job(windows, name), source = read(filename), check = job(source, core);
    assert.match(caller, new RegExp(`^    uses: \\./\\.github/workflows/${filename.replace('.', '\\.')}$`, 'm'));
    assert.match(caller, /^    permissions:\n      contents: read$/m);
    assert.doesNotMatch(caller, /^    (if|secrets|continue-on-error):/m);
    assert.doesNotMatch(caller, /: write|@master|@main|secrets: inherit/);
    assert.match(source, /^on:\n  workflow_call:/m);
    assert.doesNotMatch(check, /^    if:|continue-on-error:/m);
    assert.match(check, /gosec@v[^\s]+[^\n]* \.\/\.\.\./);
    assert.match(check, /govulncheck@v[^\s]+ \.\/\.\.\./);
    for (const checkout of source.matchAll(/- uses: actions\/checkout@[^\n]+\n([^]*?)(?=\n      -|$)/g)) {
      assert.doesNotMatch(checkout[1], /\bref:/, 'checkout must use the caller event commit');
    }
  }
});

test('core CI still requires coverage, race, UI, workflow, and explicitly discovered helper tests', () => {
  assert.match(ci, /go test \.\/\.\.\. -coverprofile=coverage\.out/);
  assert.match(ci, /total \+ 0 < 70\.0/);
  assert.match(ci, /go test -race \.\/\.\.\./);
  assert.match(ci, /go test -race \.\/scripts\/testdata/);
  assert.match(ci, /node --test scripts\/desktop-review\.test\.cjs/);
  assert.match(ci, /node --test scripts\/release-gate\.test\.cjs/);
  assert.match(ci, /actionlint/);
  for (const name of ['FuzzEchoReplayNDJSON', 'FuzzEchoReplayZIP', 'FuzzEchoSessionMapping', 'FuzzLegacyJSONReplay', 'FuzzRegressionManifest']) assert.ok(security.includes(name));
});

test('workflow and helper edits trigger Windows integration and upload names cannot collide', () => {
  assert.match(windows, /- "\.github\/workflows\/\*\*"/);
  assert.match(windows, /- "scripts\/\*\*"/);
  const names = [windows, ci, security].flatMap((source) => [...source.matchAll(/uses: actions\/upload-artifact@[^]*?\n          name: ([^\n]+)/g)].map((x) => x[1]));
  assert.ok(names.length >= 4);
  assert.equal(new Set(names).size, names.length, 'called workflows share one run artifact namespace');
});

test('installed first launch is tested before preservation fixtures can mask missing data directories', () => {
  const packageJob = job(windows, 'package');
  const startup = packageJob.indexOf('& .\\scripts\\test-installed-startup.ps1 -InstallRoot $installRoot');
  const fixtures = packageJob.indexOf('$evidenceMarker = ');
  assert.ok(startup >= 0 && fixtures > startup, 'actual installed startup must precede data fixtures');
  assert.doesNotMatch(packageJob.slice(0, fixtures), /New-Item[^\n]*\$dataRoot/);
  const installer = fs.readFileSync(path.join(root, 'packaging/NEVR-Anticheat.iss'), 'utf8');
  assert.match(installer, /Name: "\{localappdata\}\\NEVR-Anticheat"; Flags: uninsneveruninstall/);
});
