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

// GitHub expression -> JavaScript for the operators these workflows use.
const toJS = (source) => source.trim().replace(/\n\s*/g, ' ').replace(/(?<![=!])==(?!=)/g, '===').replace(/!=(?!=)/g, '!==');

// The steps of one job, in order: { text, name, condition, run }. `run` is the
// dedented script of a block-scalar "run: |" step.
function steps(jobSource) {
  const start = jobSource.search(/^    steps:\n/m);
  assert.ok(start >= 0, 'job has steps');
  const body = jobSource.slice(start).replace(/^    steps:\n/, '');
  return body.split(/^      - /m).slice(1).map((text) => {
    const field = (key) => (text.match(new RegExp(`^(?:        )?${key}: (.+)$`, 'm')) || [])[1];
    const block = text.match(/^        run: \|\n((?:(?:          .*)?\n)+)/m);
    return {
      text,
      name: field('name'),
      condition: field('if'),
      run: block ? block[1].replace(/^ {10}/gm, '') : '',
    };
  });
}

const publish = job(windows, 'publish');
const expressionMatch = publish.match(/^    if: >-\n((?:      .+\n)+)/m);
assert.ok(expressionMatch, 'publication uses an explicit, inspectable folded gate');
const expression = toJS(expressionMatch[1]);
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
  const needed = dependencies[1].split(',').map((x) => x.trim());
  assert.deepEqual([...needed].sort(), ['package', 'quality_ci', 'quality_security']);
  // A job added to needs later must be gated too: a bare "needs" entry lets
  // publish run (with a custom if:) even when that job failed or was skipped.
  // Each must be a top-level conjunct, not one side of an "||".
  const conjuncts = [];
  let depth = 0, current = '';
  for (let i = 0; i < expression.length; i++) {
    if (expression[i] === '(') depth++;
    if (expression[i] === ')') depth--;
    if (depth === 0 && expression.startsWith('&&', i)) { conjuncts.push(current.trim()); current = ''; i++; continue; }
    current += expression[i];
  }
  conjuncts.push(current.trim());
  for (const name of needed) assert.ok(conjuncts.includes(`needs.${name}.result === 'success'`), `gate does not require ${name} to succeed`);
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

test('scheduled fuzzing stays deterministic: bounded minimisation on a Go release with the fuzz deadline fix', () => {
  // The default -fuzzminimizetime (60s) exceeds the 20s run, so workers that
  // start minimising a large input idle at 0 execs/sec until the deadline.
  const fuzzRuns = security.split('\n').filter((line) => /^\s*run:/.test(line) && line.includes('-fuzz '));
  assert.ok(fuzzRuns.length >= 7, 'every parser fuzz target still runs');
  for (const line of fuzzRuns) {
    assert.match(line, /-fuzztime \d+s\b/, line.trim());
    assert.match(line, /-fuzzminimizetime \d+s\b/, `unbounded minimisation: ${line.trim()}`);
  }
  // All fuzz steps live in the fuzz job, which pins its own Go toolchain:
  // releases before 1.27 can fail a clean run with "context deadline exceeded"
  // (go.dev/issue/75804). go.mod is deliberately not consulted there.
  const fuzz = job(security, 'fuzz');
  for (const line of fuzzRuns) assert.ok(fuzz.includes(line), `fuzz step outside the fuzz job: ${line.trim()}`);
  assert.doesNotMatch(fuzz, /go-version-file:/);
  assert.doesNotMatch(fuzz, /^    if:|continue-on-error:/m);
  const version = fuzz.match(/^          go-version: ["']?(\d+)\.(\d+)(?:\.(?:x|\d+))?["']?$/m);
  assert.ok(version, 'fuzz job pins an explicit Go version');
  const [major, minor] = [Number(version[1]), Number(version[2])];
  assert.ok(major > 1 || (major === 1 && minor >= 27), `fuzz job Go ${major}.${minor} lacks the fuzz deadline fix`);
  assert.ok(fuzz.indexOf('go-version:') < fuzz.indexOf('-fuzz '), 'toolchain is installed before the first fuzz step');
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

// ---------------------------------------------------------------------------
// Rolling-release safety: one publisher at a time, and never backwards.

test('publish is serialised per release and a running publisher is never cancelled', () => {
  const block = publish.match(/^    concurrency:\n      group: (.+)\n      cancel-in-progress: (\S+)$/m);
  assert.ok(block, 'publish job declares a concurrency group');
  assert.equal(block[2], 'false', 'cancelling a publisher mid-upload leaves assets from two builds side by side');
  assert.doesNotMatch(windows, /cancel-in-progress: true/);
  const template = block[1].match(/^release-publish-\$\{\{ (.+) \}\}$/);
  assert.ok(template, 'group is release-publish-<release>');
  const group = (ref) => 'release-publish-' + new vm.Script(toJS(template[1])).runInNewContext({ github: { ref } }, { timeout: 100 });
  // Every master run publishes the same rolling release, so they must share
  // one group whatever else differs; other releases must not wait for it.
  assert.equal(group('refs/heads/master'), 'release-publish-windows-latest');
  assert.equal(group('refs/tags/v1.2.3'), 'release-publish-refs/tags/v1.2.3');
  assert.notEqual(group('refs/tags/v1.2.3'), group('refs/tags/v1.2.4'));
  // The group must sit on the publish job. At workflow level it would also
  // queue or cancel pull-request builds.
  assert.doesNotMatch(windows.slice(0, windows.indexOf('\njobs:\n')), /^concurrency:/m);
});

const publishSteps = steps(publish);
const orderingIndex = publishSteps.findIndex((step) => /^        id: ordering$/m.test(step.text));

test('nothing is downloaded, uploaded or tagged unless the ordering guard allowed it', () => {
  assert.ok(orderingIndex >= 0, 'publish job has the ordering guard step');
  const ordering = publishSteps[orderingIndex];
  assert.equal(ordering.condition, "github.ref == 'refs/heads/master'", 'the guard runs for every rolling (master) publish');
  assert.doesNotMatch(ordering.text, /continue-on-error:/);

  // Ancestry cannot be answered from a depth-1 clone: is-ancestor would fail
  // and every publish would look "diverged".
  const checkout = publishSteps.slice(0, orderingIndex).find((step) => /^uses: actions\/checkout@/.test(step.text));
  assert.ok(checkout, 'history is checked out before the guard');
  assert.match(checkout.text, /^          fetch-depth: 0$/m);

  const sideEffects = /gh release|git push|git tag --force|download-artifact/;
  const withoutComments = (text) => text.replace(/^\s*#.*$/gm, '');
  for (const step of publishSteps.slice(0, orderingIndex + 1)) {
    assert.doesNotMatch(withoutComments(step.text), sideEffects, 'release side effect before or inside the guard');
  }
  const guarded = publishSteps.slice(orderingIndex + 1);
  assert.ok(guarded.some((step) => /gh release upload/.test(step.run)), 'assets are uploaded after the guard');
  assert.ok(guarded.some((step) => /git push origin "refs\/tags\//.test(step.run)), 'the tag is moved after the guard');
  for (const step of guarded) {
    if (!sideEffects.test(withoutComments(step.text))) continue;
    assert.ok(step.condition, `unguarded publish step: ${step.name}`);
    const allowed = (ref, decision) => new vm.Script(`Boolean(${toJS(step.condition)})`)
      .runInNewContext({ github: { ref }, steps: { ordering: { outputs: { publish: decision } } } }, { timeout: 100 });
    assert.equal(allowed('refs/heads/master', 'true'), true, step.name);
    // '' is what a skipped, failed or renamed guard step leaves behind.
    for (const decision of ['false', '', undefined, 'TRUE', 'yes']) assert.equal(allowed('refs/heads/master', decision), false, `${step.name}: ${decision}`);
    assert.equal(allowed('refs/tags/v1.2.3', ''), true, 'version tags are separate releases and have no rolling order');
  }
});

function findBash() {
  const { spawnSync } = require('node:child_process');
  if (process.platform !== 'win32') return 'bash';
  // Never "bash" from PATH on Windows: that can be the WSL launcher.
  const execPath = spawnSync('git', ['--exec-path'], { encoding: 'utf8' });
  if (execPath.status !== 0) return null;
  let dir = execPath.stdout.trim();
  for (let i = 0; i < 5; i++) {
    for (const candidate of ['bin/bash.exe', 'usr/bin/bash.exe']) {
      if (fs.existsSync(path.join(dir, candidate))) return path.join(dir, candidate);
    }
    dir = path.dirname(dir);
  }
  return null;
}

test('the ordering guard script publishes forwards only (executed against scratch repositories)', (t) => {
  const { spawnSync } = require('node:child_process');
  const os = require('node:os');
  const bash = findBash();
  if (!bash) return t.skip('Git Bash was not found next to git');
  const script = publishSteps[orderingIndex].run;
  assert.match(script, /^set -euo pipefail$/m);

  const scratch = fs.mkdtempSync(path.join(os.tmpdir(), 'nevr-ordering-'));
  t.after(() => fs.rmSync(scratch, { recursive: true, force: true, maxRetries: 5 }));
  const emptyConfig = path.join(scratch, 'gitconfig');
  fs.writeFileSync(emptyConfig, '');
  const env = {
    ...process.env, GIT_CONFIG_GLOBAL: emptyConfig, GIT_CONFIG_NOSYSTEM: '1', GIT_TERMINAL_PROMPT: '0',
    GIT_AUTHOR_NAME: 'gate', GIT_AUTHOR_EMAIL: 'gate@example.invalid', GIT_COMMITTER_NAME: 'gate', GIT_COMMITTER_EMAIL: 'gate@example.invalid',
  };
  const git = (cwd, ...args) => {
    const result = spawnSync('git', args, { cwd, env, encoding: 'utf8' });
    assert.equal(result.status, 0, `git ${args.join(' ')}: ${result.stderr}`);
    return result.stdout.trim();
  };
  const origin = path.join(scratch, 'origin.git'), work = path.join(scratch, 'work'), other = path.join(scratch, 'other');
  git(scratch, 'init', '--quiet', '--bare', 'origin.git');
  git(scratch, 'init', '--quiet', '-b', 'master', 'work');
  git(work, 'remote', 'add', 'origin', origin);
  const commit = (cwd, message) => { git(cwd, 'commit', '--quiet', '--allow-empty', '-m', message); return git(cwd, 'rev-parse', 'HEAD'); };
  const older = commit(work, 'older'), current = commit(work, 'current'), newer = commit(work, 'newer');
  git(work, 'checkout', '--quiet', '-b', 'rewritten', older);
  const diverged = commit(work, 'diverged');
  git(work, 'checkout', '--quiet', 'master');
  // A commit the publishing checkout has never seen (tag moved from elsewhere).
  git(scratch, 'init', '--quiet', '-b', 'master', 'other');
  const unknown = commit(other, 'unknown');

  const outputFile = path.join(scratch, 'github-output').replace(/\\/g, '/');
  const scriptFile = path.join(scratch, 'ordering.sh');
  fs.writeFileSync(scriptFile, script);
  const decide = () => {
    fs.writeFileSync(outputFile, '');
    const result = spawnSync(bash, [scriptFile.replace(/\\/g, '/')], { cwd: work, env: { ...env, GITHUB_SHA: current, GITHUB_OUTPUT: outputFile }, encoding: 'utf8' });
    return { status: result.status, output: fs.readFileSync(outputFile, 'utf8').trim(), log: `${result.stdout}${result.stderr}` };
  };
  const publishTag = (repo, ...tagArgs) => {
    git(repo, 'tag', '--force', ...tagArgs);
    git(repo, 'push', '--quiet', '--force', origin, 'refs/tags/windows-latest');
  };
  const decision = ({ status, output }) => ({ status, output });

  assert.deepEqual(decision(decide()), { status: 0, output: 'publish=true' }, 'first rolling release');
  publishTag(work, 'windows-latest', older);
  assert.deepEqual(decision(decide()), { status: 0, output: 'publish=true' }, 'published build is an ancestor');
  publishTag(work, 'windows-latest', current);
  assert.deepEqual(decision(decide()), { status: 0, output: 'publish=true' }, 're-run of the published commit');

  publishTag(work, 'windows-latest', newer);
  let superseded = decide();
  assert.deepEqual(decision(superseded), { status: 0, output: 'publish=false' }, 'an older run must publish nothing');
  assert.match(superseded.log, /::notice::.*superseded/);
  // An annotated tag lists the tag object first and the peeled commit last.
  publishTag(work, '-a', '-m', 'annotated', 'windows-latest', newer);
  superseded = decide();
  assert.deepEqual(decision(superseded), { status: 0, output: 'publish=false' }, 'annotated tag on a newer commit');

  publishTag(work, 'windows-latest', diverged);
  let refused = decide();
  assert.notEqual(refused.status, 0, 'diverged history fails the job');
  assert.equal(refused.output, '', 'a refusal must not leave a publish decision behind');
  assert.match(refused.log, /::error::/);

  publishTag(other, 'windows-latest', unknown);
  refused = decide();
  assert.notEqual(refused.status, 0, 'a published commit outside the fetched history fails the job');
  assert.equal(refused.output, '');
  assert.match(refused.log, /::error::/);
});

// ---------------------------------------------------------------------------
// The released desktop app is a windowed program; the workflow's build line and
// scripts/package-windows.ps1 must not drift apart.

const buildJob = job(windows, 'build');
const packagingScript = fs.readFileSync(path.join(root, 'scripts/package-windows.ps1'), 'utf8').replace(/\r\n/g, '\n');
// "-s -w -X main.a=b" -> ['-s', '-w', '-X main.a=b']
const linkerFlags = (ldflags) => ldflags.trim().split(/\s+(?=-)/);
const goBuilds = [...buildJob.matchAll(/^ +go build (.*) -o "\$package\/([a-z-]+\.exe)" (\.\/cmd\/[a-z]+)$/gm)].map((match) => {
  const ldflags = match[1].match(/-ldflags "([^"]+)"/);
  assert.ok(ldflags, `${match[2]} has inspectable -ldflags`);
  return { name: match[2], flags: linkerFlags(ldflags[1]), index: match.index };
});

test('released nevr-desktop.exe is linked windowed exactly as package-windows.ps1 links it; the tools stay console', () => {
  assert.deepEqual(goBuilds.map((build) => build.name).sort(), ['nevr-ac.exe', 'nevr-bridge.exe', 'nevr-compat.exe', 'nevr-desktop.exe', 'nevr-server.exe']);
  assert.equal((buildJob.match(/^ +go build /gm) || []).length, goBuilds.length, 'every go build line is inspected');
  const scriptPrograms = [...packagingScript.matchAll(/@\{ Name = "([^"]+)";/g)].map((match) => match[1]).sort();
  assert.deepEqual(scriptPrograms, goBuilds.map((build) => build.name).sort(), 'both packagers ship the same programs');

  const scriptWindowed = packagingScript.match(/if \(\$desktopSubsystem -eq "windows"\) \{[^}]*?\$linkerFlags \+= "([^"]+)"/);
  assert.ok(scriptWindowed, 'package-windows.ps1 adds its windowed linker flags in one inspectable place');
  const expected = linkerFlags(scriptWindowed[1]).sort();
  assert.deepEqual(expected, ['-H=windowsgui', '-X main.guiSubsystem=true']);

  const desktop = goBuilds.find((build) => build.name === 'nevr-desktop.exe');
  const identity = /^-X main\.build[A-Za-z]+=/;
  assert.deepEqual(desktop.flags.filter((flag) => !['-s', '-w'].includes(flag) && !identity.test(flag)).sort(), expected,
    'workflow and package-windows.ps1 link the desktop app with different subsystem flags');
  for (const name of ['buildCommit', 'buildTime', 'buildCommitTime']) assert.ok(desktop.flags.some((flag) => flag.startsWith(`-X main.${name}=`)), name);
  for (const build of goBuilds) {
    if (build !== desktop) assert.deepEqual(build.flags, ['-s', '-w'], `${build.name} must stay a plain console program`);
  }

  // The linker ignores -X for a variable that does not exist (renamed, or no
  // longer a string), and the app would silently keep its default.
  const desktopSources = fs.readdirSync(path.join(root, 'cmd/desktop')).filter((name) => name.endsWith('.go') && !name.endsWith('_test.go'))
    .map((name) => fs.readFileSync(path.join(root, 'cmd/desktop', name), 'utf8')).join('\n');
  for (const flag of desktop.flags.filter((value) => value.startsWith('-X main.'))) {
    const variable = flag.slice('-X main.'.length).split('=')[0];
    assert.match(desktopSources, new RegExp(`^\\s*(?:var\\s+)?${variable}\\s*=\\s*"`, 'm'), `cmd/desktop declares no string variable ${variable}`);
  }
  assert.match(desktopSources, /guiSubsystem == "true"/);
});

test('a windowed desktop build is refused unless the page sends the heartbeat that lets it quit', () => {
  const scriptMarker = packagingScript.match(/\$pageSendsHeartbeat = \$pageSource -match "([^"]+)"/);
  assert.ok(scriptMarker, 'package-windows.ps1 checks the page for its heartbeat');
  assert.match(packagingScript, /cmd\\desktop\\index\.html/);
  const workflowGuard = buildJob.match(/^ +if ! grep -q "([^"]+)" cmd\/desktop\/index\.html; then\n(?: +.*\n)*? +exit 1\n +fi$/m);
  assert.ok(workflowGuard, 'workflow fails the build when the page sends no heartbeat');
  assert.equal(workflowGuard[1], scriptMarker[1], 'workflow and package-windows.ps1 look for different heartbeat markers');
  const desktop = goBuilds.find((build) => build.name === 'nevr-desktop.exe');
  assert.ok(workflowGuard.index < desktop.index, 'the heartbeat precondition runs before the windowed build');
  // Fail here, in the fast job, rather than in the release build.
  const page = fs.readFileSync(path.join(root, 'cmd/desktop/index.html'), 'utf8');
  assert.ok(page.includes(scriptMarker[1]), 'cmd/desktop/index.html no longer sends the heartbeat');
});

test('the shipped subsystem is measured from the executables, recorded, and the log file is proven', () => {
  const packageJob = job(windows, 'package');
  const finalize = steps(packageJob).find((step) => step.name === 'Finalize hashes and verify publisher signatures');
  assert.ok(finalize);
  // IMAGE_OPTIONAL_HEADER.Subsystem: e_lfanew + 4 (signature) + 20 (file header) + 68.
  assert.match(finalize.run, /\[BitConverter\]::ToUInt16\(\$bytes, \$pe \+ 0x5C\)/);
  assert.match(finalize.run, /^ +2 \{ return "windows" \}\n +3 \{ return "console" \}$/m);
  const measured = finalize.run.indexOf('$desktopSubsystem = Get-PESubsystem (Join-Path $PWD "dist\\NEVR-Anticheat-Windows-x64\\nevr-desktop.exe")');
  const required = finalize.run.indexOf('if ($desktopSubsystem -ne "windows") {\n  throw ');
  const manifest = finalize.run.indexOf('$manifest = [ordered]@{');
  const earlyExit = finalize.run.indexOf('exit 0');
  assert.ok(measured >= 0 && required > measured && manifest > required, 'measure, require windowed, then write the manifest');
  assert.ok(earlyExit > manifest, 'the unsigned early exit must not skip the subsystem check');
  assert.match(finalize.run.slice(manifest), /^ +desktop_subsystem = \$desktopSubsystem$/m);
  assert.match(packagingScript, /^ +desktop_subsystem = \$desktopSubsystem$/m, 'package-windows.ps1 records the same field in its build identity');
  const tools = finalize.run.match(/foreach \(\$name in @\(([^)]*)\)\) \{\n +\$subsystem = Get-PESubsystem [^\n]*\n +if \(\$subsystem -ne "console"\) \{ throw /);
  assert.ok(tools, 'the command-line tools are required to be console programs');
  assert.deepEqual(tools[1].split(',').map((name) => name.trim().replace(/"/g, '')).sort(), ['nevr-ac.exe', 'nevr-bridge.exe', 'nevr-compat.exe', 'nevr-server.exe']);

  const startup = packageJob.indexOf('& .\\scripts\\test-installed-startup.ps1 -InstallRoot $installRoot');
  const logCheck = packageJob.indexOf('$appLog = Join-Path $dataRoot "logs\\nevr-desktop.log"');
  assert.ok(startup >= 0 && logCheck > startup, 'the log file is checked after the installed app really ran');
  assert.match(packageJob.slice(logCheck), /^ +if \(-not \(Test-Path -LiteralPath \$appLog -PathType Leaf\) -or \(Get-Item -LiteralPath \$appLog\)\.Length -le 0\) \{\n +throw /m);
  const appLogSource = fs.readFileSync(path.join(root, 'cmd/desktop/app_log.go'), 'utf8');
  assert.match(appLogSource, /appLogDirName\s*=\s*"logs"/);
  assert.match(appLogSource, /appLogFileName\s*=\s*"nevr-desktop\.log"/);
});

test('Uninstall-NEVR.cmd forwards its arguments so -RemoveEvidence is reachable', () => {
  const wrapper = fs.readFileSync(path.join(root, 'packaging/Uninstall-NEVR.cmd'), 'utf8').replace(/\r\n/g, '\n');
  const launches = wrapper.split('\n').filter((line) => /^powershell\.exe /i.test(line));
  assert.equal(launches.length, 1);
  assert.match(launches[0], / -File "%~dp0Uninstall-NEVR\.ps1" %\*$/);
  const uninstaller = fs.readFileSync(path.join(root, 'packaging/Uninstall-NEVR.ps1'), 'utf8');
  assert.match(uninstaller, /\[switch\]\$RemoveEvidence/);
  // The relocated copy that does the work must receive the switch as well.
  assert.match(uninstaller, /if \(\$RemoveEvidence\) \{ \$arguments \+= '-RemoveEvidence' \}/);
});
