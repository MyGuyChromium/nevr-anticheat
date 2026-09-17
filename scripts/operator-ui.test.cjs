'use strict';

// Exercise shipped operator-workflow code with Node built-ins. These checks are
// complementary to rendered-browser inspection; they do not claim visual QA.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const page = fs.readFileSync(path.join(__dirname, '../cmd/desktop/index.html'), 'utf8');
const script = page.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script);
// Every "escapes untrusted ..." assertion below runs against the page's OWN esc,
// sliced out of index.html like the other helpers. A test-local copy would keep
// passing after the shipped function lost a mapping.
const escDefinition = script.match(/^  const esc = (.+);$/m);
assert.ok(escDefinition, 'index.html defines esc as a single-line const');
const escape = vm.runInNewContext('(' + escDefinition[1] + ')');

test('the page\'s own esc neutralises markup, both quote styles and non-string input', () => {
  assert.equal(escape(`<img src=x onerror=1>&"'`), '&lt;img src=x onerror=1&gt;&amp;&quot;&#39;');
  assert.equal(escape('&lt;'), '&amp;lt;', 'already-escaped text is escaped again, never trusted');
  assert.equal(escape(null), '');
  assert.equal(escape(undefined), '');
  assert.equal(escape(0), '0');
  assert.equal(escape(12.5), '12.5');
  assert.equal(escape(false), 'false');
  assert.equal(escape({ toString: () => '<b>' }), '&lt;b&gt;');
  // Attribute contexts such as data-replay-match="..." and title='...' depend on both quotes.
  const attribute = `<i data-x="${escape('" onmouseover="alert(1)')}" title='${escape("' onfocus='alert(1)")}'>`;
  assert.equal((attribute.match(/"/g) || []).length, 2);
  assert.equal((attribute.match(/'/g) || []).length, 2);
});

test('test-local esc copies in sibling suites stay equivalent to the page\'s own esc', () => {
  // desktop-review and autopocket-review inject their own one-line copy. Pin each
  // copy to the shipped function so their escaping assertions cannot drift from it.
  const corpus = [`<img src=x onerror=1>&"'`, '&amp;', '', null, undefined, 0, 7.25, true, '</script><script>', 'a\'b"c', '<<>>&&""\'\''];
  for (const name of ['desktop-review.test.cjs', 'autopocket-review.test.cjs']) {
    const source = fs.readFileSync(path.join(__dirname, name), 'utf8');
    const copies = [...source.matchAll(/^\s*const escape = (.+);$/gm)];
    if (!copies.length) { assert.doesNotMatch(source, /esc:\s*escape\b/, `${name} injects an esc this test cannot find`); continue; }
    for (const copy of copies) {
      const local = vm.runInNewContext('(' + copy[1] + ')');
      for (const value of corpus) assert.equal(local(value), escape(value), `${name} copy differs for ${JSON.stringify(value)}`);
    }
  }
});

test('native tape is accepted consistently by pickers, drop and folder intake', () => {
  assert.match(page, /id="files"[^>]*accept="\.echoreplay,\.tape,\.json"/);
  assert.match(page, /id="folder"[^>]*accept="\.echoreplay,\.tape,\.json"/);
  const started = [], messages = [], get = nodes();
  const context = run(section('  function analyze(', "  ['dragenter'"), {
    busy: false, MAX_UPLOAD_FILE_BYTES: 1000, queue: [], queuePanel: null, rememberIntakeFile: file => 'token-' + file.name,
    document: { createElement: () => ({ innerHTML: '' }) },
    drop: { appendChild() {}, classList: { add() {} } }, $: get,
    uploadList: () => '', setStatus: text => messages.push(text),
    runQueue: indices => started.push(Array.from(indices)),
  });
  context.analyze(['capture.TAPE', 'original.echoreplay', 'legacy.json', 'old.nevrcap', 'setup.exe'].map(name => ({name,size:100})));
  assert.deepEqual(Array.from(context.queue, entry => entry.file.name), ['capture.TAPE','original.echoreplay','legacy.json']);
  assert.deepEqual(started, [[0,1,2]]);
  assert.deepEqual(Array.from(context.queue, entry => [entry.token, entry.force, entry.replaceSource]), [['token-capture.TAPE', false, false], ['token-original.echoreplay', false, false], ['token-legacy.json', false, false]], 'a plain upload never forces or replaces');
  assert.match(messages.at(-1), /Ignored 2 unsupported/);
});

test('native source notice distinguishes compatibility views from original evidence', () => {
  const source = section('    const notices =', '    const tel =');
  for (const native of [true, false]) {
    const context = run(section('  function intakeNotices(', '  function matchSection(') + source + '\nresult = notices;', {
      m: {source: native ? 'tape' : 'replay',replaced:false,warnings:[]}, fmtInt:String, intake: undefined,
    });
    if (native) {
      assert.match(context.result, /Session JSON and Spark clips are derived/);
      assert.match(context.result, /keep the original \.tape/);
      assert.match(context.result, /do not establish authoritative gameplay or confirmed cheating/);
    } else assert.equal(context.result, '');
  }
});

test('native capture provenance is available for stored review and escapes recorder metadata', () => {
  const context = run(section('  function nativeCaptureBlock(', '  function matchSection('));
  assert.equal(context.nativeCaptureBlock({source:'replay'}), '');
  assert.match(context.nativeCaptureBlock({source:'tape'}), /metadata is unavailable/);
  const html = context.nativeCaptureBlock({source:'tape',native_capture:{capture_id:'synthetic-id',producer:'<script>recorder</script>',game_type:'echo_arena',format_version:2,format_minor:0,format_patch:0,frame_encoding:'FRAME_ENCODING_SPARSE',schema_revision:'synthetic-schema',limitations:['<unknown contact>']}});
  assert.match(html,/synthetic-id/);
  assert.match(html,/Decoder schema/);
  assert.match(html,/not authenticated gameplay/);
  assert.match(html,/Original container integrity not verified/);
  assert.match(html,/&lt;script&gt;recorder&lt;\/script&gt;/);
  assert.match(html,/&lt;unknown contact&gt;/);
  assert.doesNotMatch(html,/<script>|<unknown contact>/);
  const verified = context.nativeCaptureBlock({source:'tape',native_capture:{container_integrity:'verified_footer_and_checksum'}});
  assert.match(verified,/Original container footer and checksum verified at import; not gameplay authentication/);
});

test('analysis failures offer a next action and keep raw diagnostics collapsed and escaped', () => {
  const ui = run(section('  const diagTexts =', '  function prepend('), {fmtBytes: String, fmtInt: String});
  const html = ui.failCard({file:'<invalid>.json',error:'<parse failure>',diagnostic:{container:'text',size_bytes:12,lines:0,findings:[],head_text:'<private sample>',head_hex:'12 ab',hint:'Export an original recording.'}});
  assert.match(html,/Your original recording was not modified/);
  assert.match(html,/choose another recording/);
  assert.match(html,/<details class="sub"><summary>Analysis error &amp; file diagnostics/);
  assert.doesNotMatch(html,/<details[^>]*\bopen\b/);
  assert.match(html,/&lt;private sample&gt;/);
  assert.doesNotMatch(html,/<private sample>|<parse failure>|<invalid>/);
});

// ---- intake honesty: what an upload did with a recording that was already stored ----
const conflictEntry = (extra = {}) => ({
  file: 'second-observer.echoreplay', ok: false, already_stored: true, match_id: 'SYN-MATCH-1', sha256: 'ab'.repeat(32), intake_token: '7',
  error: 'A different recording of match SYN-MATCH-1 is already stored. The stored analysis was kept and this file was not analyzed: tick 40 differs.',
  source_status: 'different',
  source_conflict: { match_id: 'SYN-MATCH-1', detail: 'tick 40 differs from the stored recording', stored_source_file: 'first-observer.echoreplay', stored_analyzed_at: '2026-03-01T10:00:00Z', stored_start_time: '2026-02-28T20:00:00Z', stored_raw_ticks: 5400, replace_field: 'replace_source',
    consequence: "Replacing deletes the stored recording's raw ticks, frames, findings and scores for this match and analyzes this file instead. Labels on the previous recording's findings stay in the library but will not be attached to the new findings." },
  ...extra,
});
function failureUI(files = [['7', { name: 'second-observer.echoreplay', size: 2048 }]]) {
  const ui = run(section('  const diagTexts =', '  function prepend('), { fmtBytes: n => `${n} B`, fmtInt: String, fmtStart: iso => `start(${iso})`, timeAgo: iso => `<time>${iso}</time>`, secId: id => 'match-' + encodeURIComponent(id) });
  for (const [token, file] of files) vm.runInContext(`intakeFiles.set(${JSON.stringify(token)}, ${JSON.stringify(file)})`, ui);
  return ui;
}

test('a different recording of a stored match shows both sides and keeps replacing behind an explicit confirmation', () => {
  const html = failureUI().failCard(conflictEntry());
  assert.doesNotMatch(html, /did not enable replacement|direct API request/, 'the old sentence blamed the API for a refusal that is now deliberate');
  assert.match(html, /different recording · stored analysis kept/);
  assert.match(html, /Nothing was changed: the stored analysis was kept and this file was not analyzed/);
  assert.match(html, /Stored on this PC[\s\S]*first-observer\.echoreplay[\s\S]*<time>2026-03-01T10:00:00Z<\/time>[\s\S]*start\(2026-02-28T20:00:00Z\)[\s\S]*5400/);
  assert.match(html, /This upload[\s\S]*second-observer\.echoreplay[\s\S]*2048 B[\s\S]*abababababababab…[\s\S]*tick 40 differs from the stored recording/);
  assert.match(html, /data-open="SYN-MATCH-1"/);
  // The destructive control is inside a hidden panel, disabled, and carries the file token.
  const confirm = html.slice(html.indexOf('data-replace-confirm'));
  assert.match(html, /<div class="conflict-confirm" data-replace-confirm hidden>/);
  assert.match(confirm, /<button class="btn btn-danger" data-replace-source="7" disabled>Delete stored recording and analyze this file<\/button>/);
  assert.equal((html.match(/data-replace-source=/g) || []).length, 1);
  assert.ok(html.indexOf('data-replace-source=') > html.indexOf('data-replace-confirm'), 'no replace control outside the confirmation');
  assert.match(confirm, /deletes the stored recording&#39;s raw ticks, frames, findings and scores/);
  assert.match(confirm, /Labels on the previous recording&#39;s findings stay in the library but will not be attached/);
  assert.match(confirm, /No automatic backup is taken\./);
  assert.match(confirm, /data-replace-backup>Back up database now/);
  assert.match(confirm, /<input type="checkbox" data-replace-ack>/);
  assert.match(confirm, /data-replace-cancel>Keep the stored recording/);
});

test('the conflict card escapes recording-supplied text and degrades without optional fields', () => {
  const hostile = '<img src=x onerror=alert(1)>';
  const html = failureUI([['7', { name: hostile, size: 10 }]]).failCard(conflictEntry({ file: hostile, match_id: hostile, sha256: hostile,
    source_conflict: { match_id: hostile, detail: hostile, stored_source_file: hostile, stored_start_time: hostile, stored_analyzed_at: '', stored_raw_ticks: 0, consequence: hostile } }));
  assert.doesNotMatch(html, /<img/);
  assert.ok((html.match(/&lt;img src=x onerror=alert\(1\)&gt;/g) || []).length >= 5);
  assert.match(html, /None active \(archived, or a legacy recording\)/);
  const bare = failureUI([]).failCard({ file: 'clip.echoreplay', already_stored: true, match_id: 'SYN-2', error: 'kept', source_conflict: {} });
  assert.match(bare, /File name not recorded/);
  assert.match(bare, /The engine gave no detail/);
  assert.match(bare, /No automatic backup is taken\./, 'the consequence text never depends on the engine sending it');
  assert.match(bare, /raw ticks, frames, findings and scores/);
  assert.doesNotMatch(bare, /data-replace-ask/, 'without the file in hand there is nothing to send again');
  assert.match(bare, /choose this file again/);
  assert.match(bare, /data-replace-source="" disabled/);
});

test('an already-stored refusal without a conflict offers the stored match and a re-analysis, not a wrong explanation', () => {
  const ui = failureUI();
  const html = ui.failCard({ file: 'again.echoreplay', already_stored: true, match_id: 'SYN-3', intake_token: '7', error: 'match SYN-3 is already analyzed, but its stored analysis could not be loaded: <boom>' });
  assert.match(html, /already stored/);
  assert.match(html, /&lt;boom&gt;/);
  assert.match(html, /Nothing was changed/);
  assert.match(html, /data-open="SYN-3"/);
  assert.match(html, /data-reanalyze="7"/);
  assert.doesNotMatch(html, /did not enable replacement|data-replace-source/);
  assert.doesNotMatch(ui.failCard({ file: 'again.echoreplay', already_stored: true, match_id: 'SYN-3', intake_token: 'gone', error: 'kept' }), /data-reanalyze/);
});

test('match notices say what intake did: already analyzed, replaced, re-analyzed, or nothing', () => {
  const ui = run(section('  function intakeNotices(', '  function matchSection('), { fmtInt: String });
  assert.deepEqual(Array.from(ui.intakeNotices({ replaced: false }, { token: '3' })), [], 'a first analysis carries no intake notice');
  const known = ui.intakeNotices({ already_analyzed: true, intake_note: 'This recording was already analyzed; <b>nothing</b> was changed.', source_detail: 'all 5400 stored ticks match' }, { token: '3' }).join('');
  assert.match(known, /class="notice info"/, 'neutral, not a warning');
  assert.match(known, /Already analyzed\./);
  assert.match(known, /&lt;b&gt;nothing&lt;\/b&gt; was changed/);
  assert.match(known, /all 5400 stored ticks match/);
  assert.match(known, /data-reanalyze="3">Re-analyze this recording/);
  assert.doesNotMatch(ui.intakeNotices({ already_analyzed: true }, undefined).join(''), /data-reanalyze/, 'a match opened from History has no file to send again');
  assert.match(ui.intakeNotices({ already_analyzed: true }, undefined).join(''), /stored analysis is shown and nothing was changed/);
  const replaced = ui.intakeNotices({ replaced: true, cleared_events: 12, cleared_scores: 4, source_status: 'different' }, { token: '3', replacedSource: true }).join('');
  assert.match(replaced, /Stored recording replaced\./);
  assert.match(replaced, /12 events, 4 score snapshots/);
  assert.match(replaced, /not attached to the new findings/);
  assert.match(replaced, /No backup was taken/);
  const again = ui.intakeNotices({ replaced: true, cleared_events: 2, cleared_scores: 1, source_status: 'extends', source_detail: 'all 100 stored ticks match and this file holds 50 more' }, { token: '3' }).join('');
  assert.match(again, /Re-analyzed: the previous analysis of this match was cleared \(2 events, 1 score snapshots\)/);
  assert.match(again, /completes the stored copy/);
  assert.doesNotMatch(again, /Stored recording replaced/);
});

test('upload results are counted by what happened: analyzed, already analyzed, conflict, failed', () => {
  const shown = [], removed = [];
  const ui = run(section('  const responseItems =', '  function processQueueItem('), {
    prepend: html => shown.push(html), secId: id => 'match-' + id,
    matchSection: (m, source, intake) => `match(${m.match_id}|${source}|${intake.token}|${intake.replacedSource})`,
    failCard: res => `fail(${res.file}|${res.match_id || ''}|${res.intake_token}|${res.source_conflict ? 'conflict' : res.already_stored ? 'stored' : 'failed'})`,
    document: { getElementById: id => ({ remove: () => removed.push(id) }) },
  });
  const item = { token: '9', force: false, replaceSource: false };
  assert.deepEqual({ ...ui.showResponse({ file: 'a.echoreplay', ok: true, match: { match_id: 'M1' }, match_id: 'M1' }, item) }, { analyzed: 1, known: 0, conflicts: 0, stored: 0, failed: 0 });
  assert.equal(shown.pop(), 'match(M1|from upload|9|false)');
  assert.deepEqual({ ...ui.showResponse({ file: 'a.echoreplay', ok: true, already_analyzed: true, match: { match_id: 'M1', already_analyzed: true }, match_id: 'M1' }, item) }, { analyzed: 0, known: 1, conflicts: 0, stored: 0, failed: 0 });
  assert.equal(shown.pop(), 'match(M1|already analyzed · stored analysis|9|false)', 'a stored analysis is never presented as "from upload"');
  assert.deepEqual({ ...ui.showResponse(conflictEntry(), item) }, { analyzed: 0, known: 0, conflicts: 1, stored: 0, failed: 0 });
  assert.equal(shown.pop(), 'fail(second-observer.echoreplay|SYN-MATCH-1|9|conflict)', 'the card gets this queue item\'s file token');
  // A rematch file: one match known, one refused, plus a file-level error.
  const mixed = ui.showResponse({ file: 'two.echoreplay', error: 'stopped part-way', matches: [{ ok: true, match_id: 'M1', already_analyzed: true, match: { match_id: 'M1' } }, { ...conflictEntry(), match_id: 'M2' }, { ok: false, match_id: 'M3', error: 'parse' }] }, item);
  assert.deepEqual({ ...mixed }, { analyzed: 0, known: 1, conflicts: 1, stored: 0, failed: 2 });
  // The engine mirrors the first match's refusal into the file entry: that is one outcome, not two.
  const refused = conflictEntry();
  shown.length = 0;
  assert.deepEqual({ ...ui.showResponse({ ...refused, matches: [{ ok: false, already_stored: true, match_id: refused.match_id, error: refused.error, source_status: 'different', source_conflict: refused.source_conflict }] }, item) }, { analyzed: 0, known: 0, conflicts: 1, stored: 0, failed: 0 });
  assert.deepEqual(shown, ['fail(second-observer.echoreplay|SYN-MATCH-1|9|conflict)'], 'one card for one refusal');
  // Only a confirmed replace that really re-analysed is reported as a replacement, and it clears the conflict card.
  removed.length = 0;
  ui.showResponse({ file: 'b.echoreplay', ok: true, match_id: 'M2', match: { match_id: 'M2', replaced: true } }, { token: '9', force: true, replaceSource: true });
  assert.equal(shown.pop(), 'match(M2|from upload|9|true)');
  assert.deepEqual(removed, ['conflict-match-M2']);
  ui.showResponse({ file: 'b.echoreplay', ok: true, match_id: 'M2', match: { match_id: 'M2', replaced: true } }, { token: '9', force: true, replaceSource: false });
  assert.equal(shown.pop(), 'match(M2|from upload|9|false)');
  ui.showResponse({ file: 'b.echoreplay', ok: true, match_id: 'M4', match: { match_id: 'M4', replaced: false } }, { token: '9', force: true, replaceSource: true });
  assert.equal(shown.pop(), 'match(M4|from upload|9|false)', 'nothing was stored before, so nothing was replaced');
});

test('force and replace_source are sent only when the queue item asks for them', () => {
  const sent = [];
  class FakeXHR { constructor() { this.upload = {}; } open() {} send(fd) { sent.push(fd.fields); } }
  const context = run(section('  function processQueueItem(', '  async function runQueue('), {
    XMLHttpRequest: FakeXHR, FormData: class { constructor() { this.fields = []; } append(name, value) { this.fields.push([name, typeof value === 'string' ? value : 'file']); } },
    currentXHR: null, queueStopped: false, queue: [{}], setQueueState: () => {}, setQueueBar: () => {}, overall: () => {}, setStatus: () => {}, setConnectionState: () => {}, fmtBytes: String,
  });
  const file = { name: 'synthetic.echoreplay' };
  context.processQueueItem({ file }, 0);
  context.processQueueItem({ file, force: true }, 0);
  context.processQueueItem({ file, force: true, replaceSource: true }, 0);
  context.processQueueItem({ file, replaceSource: false, force: false }, 0);
  assert.deepEqual(sent, [[['files', 'file']], [['files', 'file'], ['force', '1']], [['files', 'file'], ['replace_source', '1']], [['files', 'file']]]);
});

test('the queue never calls a file "complete" when it was already analyzed or refused as a different recording', async () => {
  const outcomes = [
    [{ analyzed: 0, known: 1, conflicts: 0, stored: 0, failed: 0 }, /already analyzed · nothing changed/, 'stored', /1 already analyzed/],
    [{ analyzed: 0, known: 0, conflicts: 1, stored: 0, failed: 0 }, /different recording · nothing changed · see Results/, 'conflict', /1 different recording of a stored match \(nothing changed\)/],
    [{ analyzed: 1, known: 0, conflicts: 0, stored: 0, failed: 0 }, /complete · results available/, 'done', /1 analyzed/],
  ];
  for (const [counts, stateText, itemState, summary] of outcomes) {
    const get = nodes(), states = [], status = [];
    const context = run(section('  async function runQueue(', '  function analyze('), {
      $: get, busy: false, queueStopped: false, currentXHR: null, reduced: true, queue: [{ file: { name: 'one.echoreplay' }, token: '5' }], drop: get('drop'),
      processQueueItem: async () => ({ response: { ok: counts.analyzed + counts.known > 0, match: { match_id: 'synthetic-match' } } }),
      showResponse: () => counts, responseItems: result => [result], setQueueState: (index, message, cls, retry) => states.push({ message, cls, retry }),
      overall: () => {}, setStatus: (message, tone) => status.push({ message, tone }), refreshPanels: () => {},
    });
    await context.runQueue([0]);
    assert.match(states.at(-1).message, stateText);
    assert.equal(states.at(-1).retry, false, 'neither outcome is a failure to retry');
    assert.equal(context.queue[0].state, itemState);
    assert.match(status.at(-1).message, summary);
    if (counts.known) assert.match(get('ust-0').innerHTML, /data-open="synthetic-match"[\s\S]*data-reanalyze="5"/);
    if (counts.conflicts) { assert.doesNotMatch(states.at(-1).message, /complete/); assert.notEqual(status.at(-1).tone, 'ok'); assert.doesNotMatch(get('ust-0').innerHTML, /data-reanalyze/); }
  }
});

test('the Re-analyze switch forces every file of a queue; a result card can force one file or replace', () => {
  const started = [];
  const make = (checked) => run(section('  function analyze(', "  ['dragenter'"), {
    busy: false, MAX_UPLOAD_FILE_BYTES: 1000, queue: [], queuePanel: null, rememberIntakeFile: () => 't',
    document: { createElement: () => ({ innerHTML: '' }) }, drop: { appendChild() {}, classList: { add() {} } },
    $: id => (id === 'reanalyze' ? { checked } : { hidden: false }), uploadList: () => '', setStatus: () => {}, runQueue: indices => started.push(indices),
  });
  const files = [{ name: 'a.echoreplay', size: 1 }];
  let context = make(true); context.analyze(files);
  assert.deepEqual([context.queue[0].force, context.queue[0].replaceSource], [true, false], 'the switch never replaces a stored recording');
  context = make(false); context.analyze(files, { force: true });
  assert.deepEqual([context.queue[0].force, context.queue[0].replaceSource], [true, false]);
  context = make(false); context.analyze(files, { force: true, replaceSource: true });
  assert.deepEqual([context.queue[0].force, context.queue[0].replaceSource], [true, true]);
  context = make(true); context.analyze(files, { force: false });
  assert.equal(context.queue[0].force, false, 'explicit flags win over the switch');
  // The upload console no longer claims that every upload re-analyzes and replaces.
  const markup = page.slice(page.indexOf('<body>'), page.indexOf('<script>'));
  assert.doesNotMatch(markup, /Every upload uses the current detectors and replaces earlier derived events/);
  assert.match(markup, /<input type="checkbox" id="reanalyze"/);
  assert.doesNotMatch(markup, /id="reanalyze"[^>]*checked/, 'off by default');
});

test('the update card decides before the click: a refused downgrade is not "up to date", a blocked install says why', () => {
  const { updateView } = run(section('  function updateView(', '  async function loadAutomation('));
  const text = view => view.notes.map(note => `${note.strong || ''} ${note.text}`).join(' | ');
  const current = updateView({ available: false, downgrade: false, install_supported: true });
  assert.deepEqual([current.tag, current.tone, current.button, current.canInstall, current.notes.length], ['up to date', 'ok', 'Up to date', false, 0]);
  const ready = updateView({ available: true, downgrade: false, install_supported: true });
  assert.deepEqual([ready.tag, ready.button, ready.canInstall], ['update available', 'Install update', true]);

  const reason = 'The published Windows release (aaaaaaaaaaaa, committed 2026-02-27T09:00:00Z) is not newer than this build (bbbbbbbbbbbb, committed 2026-03-01T10:00:00Z); refusing to downgrade.';
  const older = updateView({ available: false, downgrade: true, downgrade_reason: reason, install_supported: true });
  assert.notEqual(older.tag, 'up to date');
  assert.notEqual(older.tone, 'ok');
  assert.deepEqual([older.button, older.canInstall], ['Nothing to install', false]);
  assert.match(text(older), /^Nothing will be installed\. The published Windows release \(aaaaaaaaaaaa/);
  assert.match(text(updateView({ downgrade: true, install_supported: true })), /Nothing will be installed\. The published Windows release is not newer than this build\./, 'a missing reason still says why');
  assert.ok(text(older).includes(reason), 'the engine\'s sentence is shown as written');
  assert.match(text(older), /installing it by hand would replace this build with one that is not newer/);
  // Contract: downgrade implies available=false. If an engine ever sent both, the page still must not offer the install.
  assert.equal(updateView({ available: true, downgrade: true, install_supported: true }).canInstall, false);
  assert.equal(updateView({ available: true, downgrade: true, install_supported: true }).button, 'Nothing to install');

  const portable = updateView({ available: true, install_supported: false, install_unsupported_reason: 'This is a portable copy.', install_reason: 'legacy text' });
  assert.deepEqual([portable.tag, portable.button, portable.canInstall], ['update available', 'One-click install unavailable', false]);
  assert.match(text(portable), /One-click install is not available on this copy\. This is a portable copy\. The verified installer can still be downloaded and run manually\./);
  assert.doesNotMatch(text(portable), /legacy text/);
  assert.match(text(updateView({ available: true, install_supported: false, install_reason: 'Older engine reason.' })), /Older engine reason\./, 'an engine without the new field still explains itself');
  assert.match(text(updateView({ available: true, install_supported: false })), /The engine gave no reason\./);
  const dev = updateView({ available: false, install_supported: false, install_unsupported_reason: 'This is a development build.' });
  assert.deepEqual([dev.tag, dev.canInstall], ['up to date', false]);
  assert.match(text(dev), /One-click install is not available on this copy: This is a development build\./);

  const failed = updateView({ available: false, downgrade: false, install_supported: true, error: 'could not confirm that the published Windows release is newer than this build: timeout' });
  assert.deepEqual([failed.tag, failed.tone, failed.button, failed.canInstall], ['check unavailable', 'bad', 'Unavailable', false]);
  assert.match(text(failed), /could not confirm/);
  // An engine from before this wave (no downgrade, no install_supported) and no engine at all.
  assert.equal(updateView({ available: true, install_supported: true }).canInstall, true);
  assert.equal(updateView({ available: true }).canInstall, false, "install support must be stated, never assumed");
  assert.deepEqual([updateView(undefined).tag, updateView(null).canInstall], ['up to date', false]);
  // Everything the card prints from the engine goes through esc.
  const card = section('  async function loadAutomation(', '  let historyMatches');
  assert.match(card, /\$\{esc\(uv\.tag\)\}/);
  assert.match(card, /\$\{esc\(note\.strong\)\}/);
  assert.match(card, /\$\{esc\(note\.text\)\}/);
  assert.match(card, /\$\{uv\.canInstall \? '' : 'disabled'\}>\$\{esc\(uv\.button\)\}/);
});

test('library import reports rows this PC kept as "kept local", not as rejected or failed', () => {
  const { libraryImportView } = run(section('  function libraryImportView(', '  async function loadStudio('), { fmtInt: String });
  const clean = libraryImportView({ ok: true, imported: { labels: 2, reviews: 3, opportunities: 0, notes: 1, filters: 0 }, rejected: { labels: 0, reviews: 0 }, kept_local: { labels: 0, reviews: 0, opportunities: 0 }, kept_local_examples: null });
  assert.deepEqual([clean.imported, clean.keptLocal, clean.rejected, clean.tone, clean.status], [6, 0, 0, 'ok', 'Imported 6 evidence records.']);
  assert.doesNotMatch(clean.html, /<details/);

  const kept = libraryImportView({ ok: true, imported: { labels: 1 }, rejected: { labels: 0, reviews: 0 }, first_error: '',
    kept_newer_local: { labels: 1, reviews: 1 }, kept_local: { labels: 2, reviews: 1, opportunities: 1 },
    kept_local_examples: ['match SYN-1: kept "clean" from 2026-03-02T10:00:00Z over imported "<b>cheat</b>" from 2026-03-01T10:00:00Z'] });
  assert.deepEqual([kept.imported, kept.keptLocal, kept.rejected, kept.tone], [1, 4, 0, 'ok'], 'kept rows never turn the import into a failure');
  assert.equal(kept.status, 'Imported 1 evidence record · 4 kept local (newer label on this PC).');
  assert.doesNotMatch(kept.status, /reject/i);
  assert.match(kept.html, /Kept local \(newer label on this PC\)/);
  assert.match(kept.html, /4 <span class="muted">2 labels, 1 review, 1 opportunity<\/span>/);
  assert.match(kept.html, /This is not an error\./);
  assert.match(kept.html, /2 newer on this PC; 2 where the imported row is equally old but different, or carries no review time/);
  assert.match(kept.html, /<dt>Rejected<\/dt><dd>0<\/dd>/);
  assert.match(kept.html, /Rows kept local · first 1 of 4/);
  assert.match(kept.html, /&lt;b&gt;cheat&lt;\/b&gt;/);
  assert.doesNotMatch(kept.html, /<b>cheat/);

  const mixed = libraryImportView({ ok: false, imported: { labels: 1 }, rejected: { labels: 0, reviews: 2 }, first_error: 'review <x>: overlapping', kept_local: { labels: 1 } });
  assert.deepEqual([mixed.keptLocal, mixed.rejected, mixed.tone], [1, 2, 'err']);
  assert.equal(mixed.status, 'Imported 1 evidence record · 1 kept local (newer label on this PC) · rejected 2. review <x>: overlapping', 'setStatus writes textContent, so the status stays raw');
  assert.match(mixed.html, /review &lt;x&gt;: overlapping/);
  // An engine from before kept_local only reports the newer-local subset; one from before both reports neither.
  const older = libraryImportView({ ok: true, imported: { labels: 1 }, rejected: {}, kept_newer_local: { labels: 2, reviews: 0 }, kept_newer_local_examples: ['match SYN-2: kept'] });
  assert.deepEqual([older.keptLocal, older.tone], [2, 'ok']);
  assert.match(older.html, /match SYN-2: kept/);
  assert.deepEqual([libraryImportView({ ok: true, imported: { labels: 1 } }).keptLocal, libraryImportView(undefined).imported], [0, 0]);
  assert.equal(libraryImportView({ ok: true, imported: { labels: 'many', reviews: NaN } }).imported, 0, 'non-numeric counts are ignored, never concatenated');
  assert.equal(libraryImportView({ ok: true, kept_local: { labels: 1 }, kept_local_examples: Array.from({ length: 50 }, (_, i) => `row ${i}`) }).html.match(/<li>/g).length, 20);
  // The import handler shows this view and keeps it across the panel refresh it triggers.
  assert.match(script, /lastLibraryImport=result;const view=libraryImportView\(result\);setStatus\(view\.status,view\.tone\);refreshPanels\(\)/);
  assert.match(script, /\$\{lastLibraryImport \? libraryImportView\(lastLibraryImport\)\.html : ''\}/);
});

test('the Regression Lab keeps "no current analysis for this match" apart from "detector stayed quiet"', () => {
  const { regressionHTML } = run(section('  function regressionHTML(', '  async function loadRegression('), { fmtInt: String, fmtNum: (n, d) => Number(n).toFixed(d), who: (name, id) => `who(${name || id})` });
  const item = (extra) => ({ match_id: 'SYN-1', player_id: 'p1', player_name: 'Synthetic', detector_id: 'SYN_001', frame_index: 120, expectation: 'signal remains present', passed: false, analysis_state: 'analyzed', reviewed_at: '2026-03-01T10:00:00Z', ...extra });
  const report = {
    total: 3, passed: 1, failed: 2, excluded_unsure: 1, notice: 'Labels are expectations.', superseded_labels: 2,
    items: [item({ passed: true, current: { severity: 0.5, confidence: 0.9 } }), item({ frame_index: 300 }), item({ frame_index: 400, expectation: 'legal play stays clear', current: { severity: 0.72, confidence: 0.8, observed_value: '<b>21 m/s</b>' } })],
    no_current_analysis: 2, no_current_analysis_matches: 2,
    no_current_analysis_items: [item({ match_id: 'SYN-AWAY', analysis_state: 'match_not_stored', analysis_note: 'This match is not stored on this PC. <Analyze> its recording here first.' }), item({ match_id: 'SYN-STALE', analysis_state: 'not_analyzed', analysis_note: 'Analyze its recording again.' })],
  };
  const html = regressionHTML(report);
  const attention = html.slice(html.indexOf('Needs attention'), html.indexOf('data-regression-untested'));
  const untested = html.slice(html.indexOf('data-regression-untested'));
  assert.match(attention, /Needs attention<span class="count">2</);
  assert.match(attention, /<b>Detector stayed quiet\.<\/b> The match was analyzed here/);
  assert.match(attention, /<b>Detector fires here\.<\/b> Severity 0\.72 · confidence 0\.80 · &lt;b&gt;21 m\/s&lt;\/b&gt;/);
  assert.doesNotMatch(html, /<table|<th>/, 'the lab card is half-width: entries are a list, so the match button is never pushed out of the card');
  assert.equal((attention.match(/<li>/g) || []).length, 2);
  assert.match(attention, /data-open="SYN-1">SYN-1 · 300</);
  assert.doesNotMatch(attention, /SYN-AWAY|SYN-STALE|No current analysis/, 'an untested label is never listed as a failing one');
  assert.match(untested, /No current analysis for this match<span class="count">2</);
  assert.match(untested, /neither passing nor failing\. This is not the detector staying quiet/);
  assert.doesNotMatch(untested, /Detector stayed quiet\./);
  assert.match(untested, /<b>Match not stored on this PC\.<\/b> This match is not stored on this PC\. &lt;Analyze&gt; its recording here first\./);
  assert.match(untested, /<b>Stored, never analyzed here\.<\/b> Analyze its recording again\./);
  assert.doesNotMatch(untested, /data-open="SYN-AWAY"/, 'a match that is not stored cannot be opened');
  assert.match(untested, /data-open="SYN-STALE"/);
  assert.match(html, /<span>Not tested<\/span><b>2<\/b><small class="muted">no current analysis for 2 matches/);
  assert.match(html, /2 older labels of a re-analyzed observation were folded into the newest one/);
  assert.doesNotMatch(untested, /<details class="sub" open/, 'failures stay the open section');

  // Only untested labels: the lab must not claim "No expectations yet" or "every expectation passes".
  const only = regressionHTML({ total: 0, passed: 0, failed: 0, excluded_unsure: 0, notice: 'n', items: [], no_current_analysis: 1, no_current_analysis_matches: 1, no_current_analysis_items: [report.no_current_analysis_items[0]] });
  assert.doesNotMatch(only, /No expectations yet|currently passes/);
  assert.match(only, /No label could be tested yet/);
  assert.match(only, /<details class="sub" open data-regression-untested>/);
  assert.match(only, /no current analysis for 1 match</);
  // An engine from before this wave: no new fields at all.
  const legacy = regressionHTML({ total: 1, passed: 1, failed: 0, excluded_unsure: 0, notice: 'n', items: [item({ passed: true })] });
  assert.match(legacy, /Every tested expectation currently passes/);
  assert.doesNotMatch(legacy, /Not tested|data-regression-untested|folded/);
  assert.match(regressionHTML({ total: 0, items: null }), /No expectations yet/);
  assert.match(regressionHTML({ total: 0, no_current_analysis_items: [item({ analysis_state: 'match_not_stored' })] }), /Not tested<\/span><b>1</, 'the count falls back to the listed items');
});

test('a scheduled or failed restore is announced with its cancel action; an older engine shows nothing', () => {
  const ui = run(section('  function restoreBannerHTML(', '  async function loadRestoreState('), { fmtAbs: t => `abs(${t.toISOString()})` });
  assert.equal(ui.restoreBannerHTML({}), null);
  assert.equal(ui.restoreBannerHTML(null), null);
  assert.equal(ui.restoreBannerHTML({ restore_pending: null, restore_failed: null }), null);
  const pending = ui.restoreBannerHTML({ restore_pending: { source: 'C:\\data\\backups\\<b>nightly.db', target: 'C:\\data\\nevr.db', requested_at: '2026-03-01T10:00:00Z' } });
  assert.equal(pending.tone, '');
  assert.match(pending.html, /A database restore is scheduled for the next launch/);
  assert.match(pending.html, /Backup &lt;b&gt;nightly\.db will replace the current evidence database/);
  assert.doesNotMatch(pending.html, /C:\\data/, 'the banner names the backup, not the whole path');
  assert.match(pending.html, /scheduled abs\(2026-03-01T10:00:00\.000Z\)/);
  assert.match(pending.html, /<button class="btn" data-restore-cancel>Cancel the restore<\/button>/);
  assert.match(ui.restoreBannerHTML({ restore_pending: {} }).html, /A backup will replace/, 'an unreadable request is still announced and can still be cancelled');
  const failed = ui.restoreBannerHTML({ restore_pending: { source: 'x.db' }, restore_failed: { source: '/data/backups/old.db', failed_at: '2026-03-02T08:00:00Z', error: 'backup <gone>' } });
  assert.equal(failed.tone, 'bad', 'a failure outranks a pending request');
  assert.match(failed.html, /did not run\./);
  assert.match(failed.html, /backup &lt;gone&gt;/);
  assert.match(failed.html, /Backup: old\.db\./);
  assert.match(failed.html, /The evidence database was left as it was/);
  assert.match(failed.html, /data-restore-cancel>Dismiss/);
});

function section(from, to) {
  const begin = script.indexOf(from);
  const finish = script.indexOf(to, begin + from.length);
  assert.ok(begin >= 0 && finish > begin, `embedded section ${from} exists`);
  return script.slice(begin, finish);
}

function run(source, globals = {}) {
  const context = vm.createContext({ esc: escape, ...globals });
  vm.runInContext(source, context);
  return context;
}

function http(fetcher) {
  const calls = [], states = [], timers = [], cleared = [];
  const context = run(section('  async function requestJSON(', '  const getJSON ='), {
    AbortController,
    fetch: async (...args) => { calls.push(args); return fetcher(...args); },
    setConnectionState: value => states.push(value),
    setTimeout: (callback, duration) => { const timer = { callback, duration }; timers.push(timer); return timer; },
    clearTimeout: timer => cleared.push(timer),
  });
  return { request: context.requestJSON, calls, states, timers, cleared };
}

function response(status, body) {
  return { ok: status >= 200 && status < 300, status, json: async () => body };
}

test('initial screen reports unknown engine and queue state, not decorative metrics', () => {
  const markup = page.slice(page.indexOf('<body>'), page.indexOf('<script>'));
  assert.match(markup, /id="connection" class="connection checking"/);
  assert.match(markup, /id="operator-backlog">Not loaded/);
  assert.match(markup, /id="catalog-count">—/);
  assert.doesNotMatch(markup, />ONLINE<|>30<|>31</);
  assert.match(markup, /Live monitoring is not available/);
  assert.match(markup, /No automatic punishment/);
  assert.match(markup, /No successful connection check yet/);
  assert.match(markup, /class="skip-link" href="#workspace"/);
});

test('screen-state messages escape untrusted titles and explanations', () => {
  const context = run(section('  function screenState(', '  const esc ='));
  const html = context.screenState('<img src=x onerror=alert(1)>', '<script>bad()</script> & "quoted"');
  assert.match(html, /&lt;img src=x/);
  assert.match(html, /&lt;script&gt;bad/);
  assert.match(html, /&amp; &quot;quoted&quot;/);
  assert.doesNotMatch(html, /<img|<script/);
  const action = '<button type="button">Retry connection</button>';
  assert.match(context.screenState('Unavailable', 'Try again', action), /<button type="button">Retry connection/);
});

function statusUI(state, now = 100000) {
  const elements = new Map();
  const get = id => {
    if (!elements.has(id)) {
      const parent = { className: '' };
      elements.set(id, { textContent: '', closest: () => parent });
    }
    return elements.get(id);
  };
  const context = run(section('  function updateOperatorStatus(', '  function screenState('), {
    operatorState: state, Date: { now: () => now }, $: get, intentionallyStopped: false,
  });
  context.updateOperatorStatus();
  return { get };
}

test('health provenance renders current identity honestly and escapes export metadata', async () => {
  async function render(provenance) {
    const element = { innerHTML: '', className: '', removeAttribute() {} };
    const context = run(section('  async function loadHealth(', '  const fmtVec ='), {
      $: () => element,
      getJSON: async () => ({ version: 'test', analysis_active: false, provenance }),
      operatorState: {}, setConnectionState() {}, fmtInt: String, fmtNum: String, fmtBytes: String, fmtAbs: String,
      panelError: (_element, _purpose, error) => { throw error; },
    });
    await context.loadHealth();
    return element.innerHTML;
  }
  const html = await render({ build_commit: '<script>commit</script>', source_revision: 'revision', source_modified: null,
    build_identity: 'unverified_build', executable_sha256: 'synthetic-sha256', config_fingerprint: '<img>config', enforcement_policy: 'review-only-v1' });
  assert.match(html, /Current runtime only, not the original analysis/);
  assert.match(html, /Source modified<\/dt><dd>Unknown/);
  assert.match(html, /synthetic-sha256/);
  assert.match(html, /review-only-v1/);
  assert.match(html, /&lt;script&gt;commit/);
  assert.match(html, /&lt;img&gt;config/);
  assert.doesNotMatch(html, /<script>|<img>/);
  assert.match(await render(undefined), /Exact runtime provenance is unavailable/);
});

test('stale successful health cannot show an idle or ready engine', () => {
  const ui = statusUI({ connection: 'online', checkedAt: 80000, appStatus: { analysis_active: false }, settings: { watch_enabled: true, watch_status: 'watching' } });
  assert.match(ui.get('operator-freshness').textContent, /status is stale/);
  assert.match(ui.get('operator-freshness').textContent, /Not database health or telemetry freshness/);
  assert.match(ui.get('operator-activity').textContent, /unknown/);
  assert.equal(ui.get('import-readiness').textContent, 'Engine not verified');
  assert.doesNotMatch(ui.get('operator-connection').closest().className, /tone-ok/);
});

test('fresh local engine is explicitly Replay and cannot masquerade as a live feed', () => {
  const ui = statusUI({ connection: 'online', checkedAt: 99000, appStatus: { analysis_active: true }, settings: { watch_enabled: true, watch_status: 'scanning' } });
  assert.equal(ui.get('operator-activity').textContent, 'Replay · analyzing');
  assert.match(ui.get('operator-source').textContent, /Folder watch: scanning/);
  assert.match(ui.get('operator-source').textContent, /No live match connection/);
  assert.equal(ui.get('import-readiness').textContent, 'Analysis in progress');
});

test('permission failure takes precedence over a recently successful health response', () => {
  const ui = statusUI({ connection: 'denied', checkedAt: 99000, appStatus: { analysis_active: false } });
  assert.equal(ui.get('operator-connection').textContent, 'Access denied');
  assert.match(ui.get('operator-activity').textContent, /unknown/);
  assert.equal(ui.get('import-readiness').textContent, 'Engine not verified');
});

test('status refresh safely stops when app is closing or workspace was removed', () => {
  for (const stopped of [true, false]) {
    const context = run(section('  function updateOperatorStatus(', '  function screenState('), {
      operatorState: {}, intentionallyStopped: stopped, $: () => null,
    });
    assert.doesNotThrow(() => context.updateOperatorStatus());
  }
});

test('late connection callbacks cannot overwrite terminal shutdown status', () => {
  const state = { connection: 'closing' };
  const context = run(section('  function setConnectionState(', '  function updateOperatorStatus('), {
    intentionallyStopped: true, operatorState: state,
    $: () => assert.fail('shutdown connection callback must not touch removed workspace or header'),
    updateOperatorStatus: () => assert.fail('shutdown must not restart status updates'),
  });
  for (const lateStatus of [true, false, 'denied', 'unavailable']) context.setConnectionState(lateStatus);
  assert.equal(state.connection, 'closing');
});

function healthProbe(fetchHealth, initial = {}) {
  const state = { connection: 'checking', checkedAt: 0, appStatus: null, ...initial };
  const calls = [], transitions = [];
  const context = run(section('  async function probeConnection(', '  // ---- match sections'), {
    intentionallyStopped: false, statusProbePending: false, operatorState: state,
    getJSON: async url => { calls.push(url); return fetchHealth(); },
    setConnectionState: value => { state.connection = value === true ? 'online' : value; transitions.push(value); },
    updateOperatorStatus: () => {},
    Date: { now: () => 100000 },
  });
  return { probe: context.probeConnection, context, state, calls, transitions };
}

test('incomplete or unversioned local status never stamps a fresh connection result', async () => {
  for (const payload of [{}, { version: 'test' }, { analysis_active: false }, { version: 'test', analysis_active: false }, { schema_version: 'nevr-desktop-status/v1', version: 'test', analysis_active: 'false' }]) {
    const client = healthProbe(async () => payload);
    await client.probe();
    assert.equal(client.state.checkedAt, 0);
    assert.equal(client.state.appStatus, null);
    assert.deepEqual(client.transitions, ['unavailable']);
  }
});

test('concurrent status probes use the lightweight route, coalesce and stop at shutdown', async () => {
  let complete;
  const client = healthProbe(() => new Promise(resolve => { complete = resolve; }));
  const first = client.probe();
  await client.probe();
  assert.deepEqual(client.calls, ['api/status']);
  complete({ schema_version: 'nevr-desktop-status/v1', version: 'test', analysis_active: false });
  await first;
  assert.equal(client.state.checkedAt, 100000);
  assert.deepEqual(client.transitions, [true]);
  client.context.intentionallyStopped = true;
  await client.probe();
  assert.equal(client.calls.length, 1);
});

test('detailed storage refresh cannot replace current connection activity or draft database identity', async () => {
  const current = { connection: 'online', checkedAt: 1000, appStatus: { analysis_active: true } };
  const element = { innerHTML: '', className: '', removeAttribute() {} };
  const fullHealth = { version: 'test', analysis_active: false, database_path: 'isolated-evidence.db' };
  const calls = [];
  const context = run(section('  async function loadHealth(', '  const fmtVec ='), {
    $: () => element, getJSON: async url => { calls.push(url); return fullHealth; }, operatorState: current,
    setConnectionState: () => assert.fail('storage measurements are not a connection heartbeat'),
    fmtInt: String, fmtNum: String, fmtBytes: String,
    fmtAbs: value => { assert.equal(Object.prototype.toString.call(value), '[object Date]'); return '<received time>'; },
    panelError: (_element, _purpose, error) => { throw error; },
  });
  await context.loadHealth();
  assert.deepEqual(calls, ['api/health']);
  assert.equal(current.checkedAt, 1000);
  assert.equal(current.appStatus.analysis_active, true);
  assert.equal(current.health.database_path, 'isolated-evidence.db');
  assert.match(element.innerHTML, /Point-in-time storage measurements/);
  assert.match(element.innerHTML, /Storage details received &lt;received time&gt;/);
  assert.doesNotMatch(element.innerHTML, /<received time>/);
  assert.match(element.innerHTML, /data-retry-panel="health"[^>]*>Refresh storage details/);
  assert.match(script, /setInterval\(probeConnection, 5000\)/);
  assert.doesNotMatch(script, /setInterval\([^;]*(?:loadHealth|refreshPanels)/);
});

function nodes() {
  const values = new Map();
  return id => {
    if (!values.has(id)) values.set(id, {
      id, value: '', textContent: '', innerHTML: '', checked: false, scrollTop: 72, hidden: false,
      removeAttribute: () => {}, setAttribute: () => {}, classList: { remove: () => {} },
      focus() { this.focused = true; }, scrollIntoView() { this.scrolledIntoView = true; },
      insertAdjacentHTML(_position, html) { this.innerHTML += html; },
    });
    return values.get(id);
  };
}

test('new case arrivals do not replace visible rows until explicit refresh', async () => {
  const get = nodes();
  get('flagged').innerHTML = '<tr>Currently selected case</tr>';
  let renderCount = 0;
  const initial = { single_match: [{ case_id: 'c1', status: 'pending' }], cross_match: [] };
  const incoming = { single_match: [{ case_id: 'c2', status: 'pending' }, ...initial.single_match], cross_match: [] };
  const context = run(section('  async function loadFlagged(', '  async function loadObservations('), {
    $: get, caseSnapshot: initial, pendingCaseSnapshot: null,
    getJSON: async () => incoming, renderFlagged: () => { renderCount++; },
    panelError: () => assert.fail('successful load must not render failure'),
  });
  await context.loadFlagged();
  assert.equal(context.caseSnapshot, initial);
  assert.equal(context.pendingCaseSnapshot, incoming);
  assert.equal(get('flagged').innerHTML, '<tr>Currently selected case</tr>');
  assert.equal(get('flagged').scrollTop, 72);
  assert.match(get('refresh-cases').textContent, /changes available/i);
  assert.equal(renderCount, 0);
  await context.loadFlagged(true);
  assert.equal(context.caseSnapshot, incoming);
  assert.equal(context.pendingCaseSnapshot, null);
  assert.equal(renderCount, 1);
});

test('failed queue fetch does not advertise zero pending cases', async () => {
  const get = nodes();
  let errors = 0;
  const context = run(section('  async function loadFlagged(', '  async function loadObservations('), {
    $: get, caseSnapshot: null, pendingCaseSnapshot: null,
    getJSON: async () => { throw new Error('Save store unavailable'); },
    renderFlagged: () => assert.fail('failed fetch must not render an empty queue'),
    panelError: () => { errors++; },
  });
  await context.loadFlagged();
  assert.match(get('operator-backlog').textContent, /unavailable/i);
  assert.match(get('operator-backlog-note').textContent, /does not mean the queue is empty/);
  assert.equal(errors, 1);
});

function historyUI(matches, historyPage = 0, globals = {}) {
  const get = nodes(), preferences = { lastMatch: 'match-16' };
  get('history-sort').value = 'oldest';
  const source = section('  function renderHistory(', '  async function loadHistory(');
  const context = run(source, {
    $: get, historyMatches: matches, historyPage, HISTORY_PAGE_SIZE: 15, prefs: preferences,
    savePrefs: () => {}, fmtInt: String, fmtStart: String, fmtDur: String, timeAgo: escape, labelName: escape,
    screenState: (title, message, actions) => `${escape(title)} ${escape(message)} ${actions}`,
    blindReview: false, views: [], activeInvestigationData: null, exposeMatch: () => {}, ...globals,
  });
  return { context, get, preferences };
}

test('history keeps saved page and selection with large names and no-signal wording', () => {
  const matches = Array.from({ length: 35 }, (_, i) => ({
    match_id: `match-${i}`, source: 'replay', analyzed_at: new Date(100000 + i * 1000).toISOString(),
    players: [{ name: '<img src=x>' + 'very long name '.repeat(80), player_id: `p${i}` }], event_count: 0,
  }));
  const ui = historyUI(matches, 1), before = JSON.stringify(matches);
  ui.context.renderHistory();
  const html = ui.get('history').innerHTML;
  assert.match(html, /Page 2 of 3/);
  assert.match(html, /<tr class="selected">/);
  assert.equal((html.match(/data-investigation-match=/g) || []).length, 15);
  assert.match(html, /No recorded observations/);
  assert.doesNotMatch(html, />clean<|<img/);
  assert.match(html, /&lt;img/);
  assert.equal(ui.preferences.historyPage, 1);
  assert.equal(JSON.stringify(matches), before);
});

test('unknown observation count and empty search get different intentional states', () => {
  const ui = historyUI([{ match_id: 'm1', players: [], source: 'replay' }]);
  ui.context.renderHistory();
  assert.match(ui.get('history').innerHTML, /Observations unknown/);
  assert.doesNotMatch(ui.get('history').innerHTML, /No recorded observations|>clean</);
  ui.get('history-search').value = 'not-in-the-library';
  ui.context.renderHistory();
  assert.match(ui.get('history').innerHTML, /No matching recordings/);
  assert.match(ui.get('history').innerHTML, /reset-history-filters/);
  const empty = historyUI([]);
  empty.context.renderHistory();
  assert.match(empty.get('history').innerHTML, /Import a replay/);
});

test('changed history remains deferred without losing search, page or scroll', async () => {
  const get = nodes(), initial = [{ match_id: 'm1' }], incoming = [{ match_id: 'm2' }, ...initial];
  get('history-search').value = 'retained search';
  get('history').innerHTML = '<table>Retained rows</table>';
  const context = run(section('  async function loadHistory(', "  ['history-search'"), {
    $: get, historyMatches: initial, historyPage: 3, pendingHistory: null, prefs: {},
    getJSON: async () => ({ matches: incoming }), renderHistory: () => assert.fail('changed history must wait for explicit refresh'),
    populateLabMatches: () => {}, panelError: () => assert.fail('no fetch error'),
  });
  await context.loadHistory();
  assert.equal(context.historyMatches, initial);
  assert.equal(context.pendingHistory, incoming);
  assert.equal(context.historyPage, 3);
  assert.equal(get('history-search').value, 'retained search');
  assert.equal(get('history').scrollTop, 72);
  assert.equal(get('history').innerHTML, '<table>Retained rows</table>');
});

test('appearance accepts only supported preferences and honors system reduced motion', () => {
  const get = nodes(), dataset = {};
  const context = run(section('  function applyAppearance(', '  applyAppearance();'), {
    $: get, prefs: { theme: 'javascript:bad', density: 'compact', textSize: 'large', motion: 'system', shortcuts: 'true' },
    document: { documentElement: { dataset } }, reduced: false, matchMedia: () => ({ matches: true }),
  });
  context.applyAppearance();
  assert.equal(dataset.theme, 'dark');
  assert.equal(dataset.density, 'compact');
  assert.equal(dataset.textSize, 'large');
  assert.equal(context.reduced, true);
  assert.equal(get('pref-shortcuts').checked, false, 'keyboard shortcut opt-in must be an explicit boolean');
  context.prefs.shortcuts = true;
  context.applyAppearance();
  assert.equal(get('pref-shortcuts').checked, true);
});

test('notifications coalesce identical messages, escape text and stay bounded', () => {
  const get = nodes(), notices = [];
  const context = run(section('  function recordNotice(', '  let blindReview = false;'), {
    $: get, notices, fmtAbs: () => 'time',
  });
  context.recordNotice('<img src=x onerror=alert(1)>', 'err');
  context.recordNotice('<img src=x onerror=alert(1)>', 'err');
  assert.equal(notices.length, 1);
  assert.equal(notices[0].count, 2);
  assert.match(get('notice-history').innerHTML, /repeated 2 times/);
  assert.match(get('notice-history').innerHTML, /&lt;img/);
  assert.doesNotMatch(get('notice-history').innerHTML, /<img/);
  for (let i = 0; i < 35; i++) context.recordNotice(`Different operation ${i}`, 'ok');
  assert.equal(notices.length, 30);
});

test('successful requests send data once and never declare telemetry healthy', async () => {
  const payload = { body: 'Synthetic review text', frame_index: 12 };
  const client = http(async () => response(200, { note_id: 'n1', ...payload }));
  const saved = await client.request('POST', 'api/match/example/notes', payload);
  assert.equal(saved.note_id, 'n1');
  assert.equal(client.calls.length, 1);
  const [url, options] = client.calls[0];
  assert.equal(url, 'api/match/example/notes');
  assert.equal(options.method, 'POST');
  assert.equal(options.cache, 'no-store');
  assert.equal(options.headers['Content-Type'], 'application/json');
  assert.deepEqual(JSON.parse(options.body), payload);
  assert.equal(options.signal.aborted, false);
  assert.equal(client.timers[0].duration, 90000);
  assert.deepEqual(client.cleared, client.timers);
  assert.deepEqual(client.states, []);
});

test('GET and DELETE use bounded requests without adding a body or retry', async () => {
  for (const method of ['GET', 'DELETE']) {
    const client = http(async () => response(200, { ok: true }));
    await client.request(method, 'api/example');
    assert.equal(client.calls.length, 1);
    assert.equal(client.calls[0][1].body, undefined);
    assert.equal(client.calls[0][1].headers, undefined);
    assert.equal(client.timers[0].duration, method === 'GET' ? 20000 : 90000);
    assert.deepEqual(client.cleared, client.timers);
  }
});

test('401 and 403 are access-denied states, not disconnection or success', async () => {
  for (const status of [401, 403]) {
    const client = http(async () => response(status, { error: '<img>private internal detail</img>' }));
    await assert.rejects(client.request('GET', 'api/match/forbidden'), error => {
      assert.equal(error.status, status);
      assert.match(error.message, /Access denied/);
      assert.match(error.message, /current app window/);
      assert.doesNotMatch(error.message, /private internal detail/);
      return true;
    });
    assert.deepEqual(client.states, ['denied']);
    assert.equal(client.calls.length, 1);
    assert.deepEqual(client.cleared, client.timers);
  }
});

test('409 keeps server conflict text and never retries or mutates the submitted draft', async () => {
  const draft = { note_id: 'draft-id', body: 'Unsaved investigation', frame_index: 21 };
  const before = JSON.stringify(draft);
  const client = http(async () => response(409, { error: 'A newer review exists. Reload before saving.' }));
  await assert.rejects(client.request('POST', 'api/match/example/notes', draft), error => {
    assert.equal(error.status, 409);
    assert.match(error.message, /newer review/);
    return true;
  });
  assert.equal(JSON.stringify(draft), before);
  assert.equal(client.calls.length, 1);
  assert.deepEqual(client.states, []);
  assert.deepEqual(client.cleared, client.timers);
});

test('network failures retain open-evidence guidance and cannot auto-retry a write', async () => {
  const client = http(async () => { throw new TypeError('Network failed'); });
  await assert.rejects(client.request('POST', 'api/example', { body: 'Keep me' }), error => {
    assert.match(error.message, /disconnected/);
    assert.match(error.message, /open evidence stays visible/);
    return true;
  });
  assert.equal(client.calls.length, 1);
  assert.deepEqual(client.states, [false]);
  assert.deepEqual(client.cleared, client.timers);
});

test('timeout warns that a write may still run instead of reporting it cancelled', async () => {
  let client;
  client = http(async (_url, options) => {
    client.timers[0].callback();
    assert.equal(options.signal.aborted, true);
    throw new Error('AbortError');
  });
  await assert.rejects(client.request('POST', 'api/example', { body: 'Draft' }), error => {
    assert.match(error.message, /may still be running/);
    assert.match(error.message, /check its results before retrying/);
    assert.doesNotMatch(error.message, /success|cancelled|saved/i);
    return true;
  });
  assert.equal(client.calls.length, 1);
  assert.deepEqual(client.cleared, client.timers);
});

test('a call with its own time budget reports that the page gave up, not that the engine is down', async () => {
  let client;
  client = http(async (_url, options) => {
    client.timers[0].callback();
    assert.equal(options.signal.aborted, true);
    throw new Error('AbortError');
  });
  await assert.rejects(client.request('POST', 'api/update/install', undefined, { timeoutMs: 720000, timeoutMessage: 'Stopped waiting; the download was cancelled.' }), error => {
    assert.equal(error.timedOut, true);
    assert.equal(error.message, 'Stopped waiting; the download was cancelled.');
    assert.doesNotMatch(error.message, /may still be running|disconnected/);
    return true;
  });
  assert.equal(client.timers[0].duration, 720000);
  assert.deepEqual(client.states, [], 'a page-side timeout must not mark a healthy engine disconnected');
  assert.deepEqual(client.cleared, client.timers);

  // A real network failure during a long call is still a disconnection.
  const dropped = http(async () => { throw new TypeError('Network failed'); });
  await assert.rejects(dropped.request('POST', 'api/update/install', undefined, { timeoutMs: 720000 }), error => {
    assert.equal(error.timedOut, undefined);
    assert.match(error.message, /disconnected/);
    return true;
  });
  assert.deepEqual(dropped.states, [false]);
  // Nonsense budgets fall back to the default instead of disabling the timeout.
  for (const timeoutMs of [0, -5, NaN, Infinity, '720000']) {
    const fallback = http(async () => response(200, { ok: true }));
    await fallback.request('POST', 'api/example', undefined, { timeoutMs });
    assert.equal(fallback.timers[0].duration, 90000);
  }
});

function updateInstall(post) {
  const button = { disabled: false, textContent: 'Install update' };
  const messages = [], intervals = [], stopped = [], posts = [];
  const source = section('  const UPDATE_INSTALL_TIMEOUT_MS =', '  const getJSON =') +
    '\nasync function clickInstall(e) {\n' + section("    if (e.target.closest('#install-update')) {", "    if (e.target.closest('#auto-updates')) {") + '\n}';
  const context = run(source, {
    intentionallyStopped: false, Date, Math, String,
    $: id => id === 'install-update' ? button : null,
    setStatus: (text, tone = '') => messages.push({ text, tone }),
    setInterval: (callback, every) => { const timer = { callback, every }; intervals.push(timer); return timer; },
    clearInterval: timer => stopped.push(timer),
    setTimeout: () => 0, document: { querySelector: () => ({}) }, window: { close() {} },
    postJSON: async (...args) => { posts.push(args); return post(...args); },
  });
  const click = () => context.clickInstall({ target: { closest: selector => selector === '#install-update' ? button : null } });
  return { context, click, button, messages, intervals, stopped, posts };
}

test('one-click update waits longer than the engine download budget and shows elapsed time', async () => {
  let during;
  const ui = updateInstall(async () => { ui.intervals[0].callback(); during = { ...ui.button }; return { message: 'Update verified.' }; });
  await ui.click();
  assert.equal(ui.posts.length, 1);
  const [url, payload, options] = ui.posts[0];
  assert.equal(url, 'api/update/install');
  assert.equal(payload, undefined);
  // desktop_runtime.go gives the download 10 minutes; the default 90 s POST timeout cancelled it.
  assert.ok(options.timeoutMs > 10 * 60 * 1000, 'the page must outwait the engine, not cancel it');
  assert.equal(options.timeoutMs, vm.runInContext('UPDATE_INSTALL_TIMEOUT_MS', ui.context));
  assert.match(options.timeoutMessage, /did not finish within 12 minutes/);
  assert.equal(options.timeoutMs, 12 * 60 * 1000, 'the message names the same budget');
  assert.equal(during.disabled, true);
  assert.match(during.textContent, /^Downloading & verifying… \d+:\d\d$/);
  assert.equal(vm.runInContext('updateInstallLabel(65000)', ui.context), 'Downloading & verifying… 1:05');
  assert.match(ui.messages[0].text, /several minutes; keep this window open/);
  assert.deepEqual(ui.stopped, ui.intervals, 'the elapsed-time ticker stops when the request settles');
  assert.equal(ui.button.textContent, 'Restarting…');
  assert.equal(ui.context.intentionallyStopped, true);
  assert.deepEqual(ui.messages.at(-1), { text: 'Update verified.', tone: 'ok' });
});

test('an update the page stopped waiting for is reported as cancelled, with the engine still running', async () => {
  const ui = updateInstall(async (_url, _payload, options) => { const error = new Error(options.timeoutMessage); error.timedOut = true; throw error; });
  await ui.click();
  const last = ui.messages.at(-1);
  assert.equal(last.tone, 'err');
  assert.match(last.text, /cancels the download/);
  assert.match(last.text, /nothing was installed/);
  assert.match(last.text, /still running this version/);
  assert.match(last.text, /Manual download/);
  assert.doesNotMatch(last.text, /may still be running|disconnected|did not respond/);
  assert.match(last.text, /^The update did not finish within 12 minutes/, 'the page-side timeout is not dressed up as an engine failure');
  assert.equal(ui.button.disabled, false);
  assert.equal(ui.button.textContent, 'Install update');
  assert.equal(ui.context.intentionallyStopped, false);
  assert.deepEqual(ui.stopped, ui.intervals);

  const refused = updateInstall(async () => { throw new Error('finish or cancel the active replay analysis before updating'); });
  await refused.click();
  assert.equal(refused.messages.at(-1).text, 'Update failed safely: finish or cancel the active replay analysis before updating');
  assert.equal(refused.button.disabled, false);
});

test('unreadable success response remains a failed operation', async () => {
  const client = http(async () => ({ ok: true, status: 200, json: async () => { throw new SyntaxError('bad json'); } }));
  await assert.rejects(client.request('POST', 'api/example'), /unreadable response/);
  assert.equal(client.calls.length, 1);
  assert.deepEqual(client.states, []);
});

test('HTTP server failure remains distinct from network failure', async () => {
  const client = http(async () => response(503, { error: 'Engine is shutting down; reopen NEVR.' }));
  await assert.rejects(client.request('GET', 'api/health'), error => {
    assert.equal(error.status, 503);
    assert.match(error.message, /shutting down/);
    return true;
  });
  assert.equal(client.calls.length, 1);
  assert.deepEqual(client.states, []);
});

function queueBar() {
  const attributes = new Map(), classes = new Map();
  const bar = {
    classList: { toggle: (key, value) => classes.set(key, value) },
    setAttribute: (key, value) => attributes.set(key, String(value)),
    removeAttribute: key => attributes.delete(key),
    firstElementChild: { style: {} },
  };
  const context = run(section('  const setQueueBar =', '  const overall =') + '\nglobalThis.setBar = setQueueBar;', { $: id => id === 'ubar-0' ? bar : null });
  return { set: context.setBar, attributes, classes, bar };
}

test('analysis stage is indeterminate while transfer progress remains measured', () => {
  const ui = queueBar();
  ui.set(0, 42.6);
  assert.equal(ui.attributes.get('aria-valuenow'), '43');
  ui.set(0, 100, true);
  assert.equal(ui.classes.get('wait'), true);
  assert.equal(ui.attributes.has('aria-valuenow'), false, 'unknown analysis completion cannot be announced as 100%');
  ui.set(0, 100, false);
  assert.equal(ui.attributes.get('aria-valuenow'), '100');
  assert.doesNotThrow(() => ui.set(999, 100, true));
});

test('stopping queue reports backend acceptance separately from completed cancellation', async () => {
  const handler = section("    if (e.target.closest('#cancel-queue'))", "    if (e.target.closest('#retry-failed'))");
  for (const result of [{ cancelled: true }, { cancelled: false }, {}]) {
    const messages = [], button = { disabled: false };
    let aborts = 0, requests = 0;
    const context = run(`async function cancelClick(e) {\n${handler}\n}`, {
      queueStopped: false, currentXHR: { abort: () => { aborts++; } },
      setStatus: message => messages.push(message),
      postJSON: async url => { assert.equal(url, 'api/analyze/cancel'); requests++; return result; },
    });
    await context.cancelClick({ target: { closest: () => button } });
    assert.equal(context.queueStopped, true);
    assert.equal(requests, 1);
    assert.equal(aborts, 0, 'browser abort cannot stand in for a backend cancellation');
    assert.equal(button.disabled, true);
    assert.match(messages.at(-1), result.cancelled ? /accepted.*request.*final result/i : /did not confirm.*may finish/i);
    assert.doesNotMatch(messages.at(-1), /successfully cancelled|analysis cancelled|analysis complete/i);
  }
});

test('failed cancellation request does not report the active file cancelled', async () => {
  const handler = section("    if (e.target.closest('#cancel-queue'))", "    if (e.target.closest('#retry-failed'))");
  const messages = [];
  const context = run(`async function cancelClick(e) {\n${handler}\n}`, {
    queueStopped: false,
    setStatus: (message, tone) => messages.push({ message, tone }),
    postJSON: async () => { throw new Error('Disconnected'); },
  });
  await context.cancelClick({ target: { closest: () => ({}) } });
  assert.equal(context.queueStopped, true);
  assert.match(messages.at(-1).message, /cancellation was not confirmed/);
  assert.match(messages.at(-1).message, /may continue/);
  assert.equal(messages.at(-1).tone, 'err');
});

test('a file that finishes during stop remains complete while remaining files are not started', async () => {
  const get = nodes(), states = [], status = [];
  let context, processed = 0;
  context = run(section('  async function runQueue(', '  function analyze('), {
    $: get, busy: false, queueStopped: false, currentXHR: null, reduced: true,
    queue: [{ file: { name: 'one.echoreplay' } }, { file: { name: 'two.echoreplay' } }], drop: get('drop'),
    processQueueItem: async () => { processed++; context.queueStopped = true; return { response: { ok: true, match: { match_id: 'synthetic-match' } } }; },
    showResponse: () => ({ analyzed: 1, stored: 0, failed: 0 }), responseItems: result => [result],
    setQueueState: (index, message) => states.push({ index, message }),
    overall: () => {}, setStatus: message => status.push(message), refreshPanels: () => {},
  });
  await context.runQueue([0, 1]);
  assert.equal(processed, 1);
  assert.equal(context.queue[0].state, 'done');
  assert.match(states.find(s => s.index === 0).message, /complete.*results available/);
  assert.match(states.find(s => s.index === 1).message, /not started/);
  assert.match(status.at(-1), /1 analyzed.*1 not started/);
  assert.match(get('ust-0').innerHTML, /data-open="synthetic-match"/);
});

test('upload timeout stops remaining files but does not claim backend cancellation', async () => {
  let xhr;
  class FakeXHR {
    constructor() { xhr = this; this.upload = {}; }
    open(method, url) { this.method = method; this.url = url; }
    send() {}
  }
  const context = run(section('  function processQueueItem(', '  async function runQueue('), {
    XMLHttpRequest: FakeXHR, FormData: class { append() {} },
    currentXHR: null, queueStopped: false, queue: [{}],
    setQueueState: () => {}, setQueueBar: () => {}, overall: () => {}, setStatus: () => {}, setConnectionState: () => {},
    fmtBytes: String,
  });
  const pending = context.processQueueItem({ file: { name: 'synthetic.echoreplay' } }, 0);
  assert.equal(xhr.timeout, 30 * 60 * 1000);
  xhr.ontimeout();
  const result = await pending;
  assert.equal(context.queueStopped, true);
  assert.match(result.error, /may still be working/);
  assert.match(result.error, /Remaining files were not started/);
  assert.doesNotMatch(result.error, /successfully cancelled|analysis cancelled/i);
});

test('drop-zone Enter activates only the drop target, never a nested action', () => {
  let handler, picked = 0;
  const drop = { addEventListener: (type, callback) => { assert.equal(type, 'keydown'); handler = callback; } };
  const context = run(section("  drop.addEventListener('keydown'", "  $('files').addEventListener('change'"), {
    drop, busy: false, $: () => ({ click: () => { picked++; } }),
  });
  const event = target => ({ target, key: 'Enter', prevented: false, preventDefault() { this.prevented = true; } });
  const nested = event({}); handler(nested);
  assert.equal(picked, 0);
  assert.equal(nested.prevented, false);
  const direct = event(drop); handler(direct);
  assert.equal(picked, 1);
  assert.equal(direct.prevented, true);
  context.busy = true; handler(event(drop));
  assert.equal(picked, 1);
});

function chart(points) {
  const context = run(section('  function speedChart(', '  function renderInvestigation('), { fmtNum: String });
  return context.speedChart(points);
}

function chartPoints() {
  return [0, 1, 2].map(time => ({ player_id: 'synthetic', player_name: '<img src=x onerror=alert(1)>', time, pose_speed: 2, game_speed: 0, disc_speed: 3 }));
}

test('timeline preserves missing values as gaps instead of zero-speed observations', () => {
  const points = chartPoints();
  points[1].game_speed = null;
  points[1].disc_speed = null;
  const before = JSON.stringify(points);
  const html = chart(points);
  for (const series of ['game', 'disc']) {
    const path = html.match(new RegExp(`<path class="${series}" d="([^"]*)"`));
    assert.ok(path, `${series} is present`);
    assert.equal((path[1].match(/M /g) || []).length, 2, 'missing point starts a separate segment');
    assert.equal((path[1].match(/L /g) || []).length, 0, 'no invented line across the missing sample');
  }
  assert.equal(JSON.stringify(points), before);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<img|NaN|Infinity/);
});

test('timeline displays a measured zero and does not bridge different player streams', () => {
  const points = chartPoints();
  points.splice(1, 0, { ...points[0], player_id: 'different', pose_speed: 999999, game_speed: 999999 });
  const html = chart(points);
  const game = html.match(/<path class="game" d="([^"]*)"/);
  assert.ok(game);
  const baseline = html.match(/<text x="[^"]+" y="([^"]+)">0<\/text>/);
  assert.ok(baseline);
  assert.match(game[1], new RegExp(` ${Number(baseline[1]).toFixed(1).replace('.', '\\.')}($| )`));
  assert.equal((game[1].match(/L /g) || []).length, 2);
  assert.doesNotMatch(html, /999999|NaN|Infinity/);
});

test('empty timeline provides an intentional state, not an empty SVG', () => {
  const html = chart([]);
  assert.match(html, /No .*timeline|No .*samples/i);
  assert.doesNotMatch(html, /<svg/);
});

function noteUI(options = {}) {
  const get = nodes(), storage = new Map(), writes = [], notices = [], mirrored = [], buttons = [{ disabled: false }, { disabled: false }];
  let nextID = 0;
  get('note-body').value = 'A synthetic note awaiting confirmation';
  get('note-frame').value = '-1';
  const context = run(section('  function draftKey(', '  function selectedIncidentDetails('), {
    $: get, noteDraft: { id: 'stable-draft-id', matchID: 'm1', body: get('note-body').value, frame: -1 }, noteSavePending: false, investigationRequest: 1,
    activeInvestigationMatch: 'm1', activeInvestigationData: { notes: [] },
    operatorState: { health: { database_path: 'synthetic-evidence-store' } }, location: { pathname: '/synthetic-session/' },
    crypto: { randomUUID: () => `new-draft-${++nextID}` }, fmtInt: String,
    // The api/ui-prefs mirror is covered by its own tests; here it is absent (404 fallback).
    serverNoteDraft: matchID => options.serverDraft?.(matchID) ?? null,
    mirrorNoteDraft: draft => { mirrored.push({ ...draft }); return options.mirrorStores === true; },
    document: { querySelectorAll: () => buttons },
    localStorage: {
      getItem: key => storage.get(key) ?? null,
      setItem: (key, value) => { if (options.storageFails) throw new Error('Storage unavailable'); storage.set(key, value); },
      removeItem: key => storage.delete(key),
    },
    postJSON: async (url, payload) => { writes.push({ url, payload }); return options.save ? options.save(payload) : { note_id: payload.note_id }; },
    getJSON: async () => options.load ? options.load() : { notes: [] },
    recordNotice: (message, tone) => notices.push({ message, tone }),
  });
  return { context, get, storage, writes, notices, mirrored, buttons };
}

test('note save guards duplicate clicks and waits for matching backend confirmation', async () => {
  let complete;
  const ui = noteUI({ save: () => new Promise(resolve => { complete = resolve; }) });
  const first = ui.context.saveInvestigationNote();
  assert.equal(ui.context.noteSavePending, true);
  assert.ok(ui.buttons.every(button => button.disabled));
  assert.match(ui.get('note-status').textContent, /Saving/);
  assert.equal(ui.writes.length, 1);
  await ui.context.saveInvestigationNote();
  assert.equal(ui.writes.length, 1, 'second click cannot create a second note');
  assert.notEqual(ui.get('note-body').value, '');
  complete({ note_id: 'stable-draft-id' });
  await first;
  assert.equal(ui.get('note-body').value, '');
  assert.equal(ui.context.noteSavePending, false);
  assert.ok(ui.buttons.every(button => !button.disabled));
  assert.match(ui.get('note-status').textContent, /Saved to the evidence database/);
  assert.equal(ui.storage.has(ui.context.draftKey('m1')), false);
});

test('failed and conflicting note saves retain the original body and retry ID', async () => {
  for (const [status, message] of [[503, 'Database unavailable'], [409, 'Newer review exists'], [0, 'Timed out; operation may still run']]) {
    const ui = noteUI({ save: () => { const error = new Error(message); error.status = status; throw error; } });
    const original = ui.get('note-body').value;
    await ui.context.saveInvestigationNote();
    assert.equal(ui.get('note-body').value, original);
    assert.equal(ui.context.noteDraft.id, 'stable-draft-id');
    assert.equal(JSON.parse(ui.storage.get(ui.context.draftKey('m1'))).body, original);
    assert.match(ui.get('note-status').textContent, status === 409 ? /Save conflict/ : /Save not confirmed/);
    assert.ok(ui.notices.every(notice => notice.tone === 'err'));
    await ui.context.saveInvestigationNote();
    assert.equal(ui.writes.length, 2);
    assert.equal(ui.writes[0].payload.note_id, ui.writes[1].payload.note_id, 'retry is idempotent by draft ID');
    assert.equal(ui.context.noteSavePending, false);
  }
});

test('mismatched save response cannot clear a draft or report it saved', async () => {
  const ui = noteUI({ save: () => ({ note_id: 'wrong-id' }) });
  const original = ui.get('note-body').value;
  await ui.context.saveInvestigationNote();
  assert.equal(ui.get('note-body').value, original);
  assert.equal(ui.context.noteDraft.id, 'stable-draft-id');
  assert.match(ui.get('note-status').textContent, /did not confirm this note/);
  assert.ok(ui.notices.every(notice => notice.tone === 'err'));
});

test('confirmed save followed by failed list refresh stays saved, not a false save failure', async () => {
  const ui = noteUI({ load: () => { throw new Error('Disconnected after save'); } });
  await ui.context.saveInvestigationNote();
  assert.equal(ui.get('note-body').value, '');
  assert.match(ui.get('note-status').textContent, /Note saved, but the notes list could not refresh/);
  assert.doesNotMatch(ui.get('note-status').textContent, /Save not confirmed|Save conflict/);
  assert.ok(ui.notices.every(notice => notice.tone === 'ok'));
});

test('edits made during save become a distinct retained draft after confirmation', async () => {
  let complete;
  const ui = noteUI({ save: () => new Promise(resolve => { complete = resolve; }) });
  const first = ui.context.saveInvestigationNote();
  ui.get('note-body').value = 'Newer edits typed while saving';
  ui.get('note-frame').value = '42';
  ui.context.persistNoteDraft();
  complete({ note_id: 'stable-draft-id' });
  await first;
  assert.equal(ui.get('note-body').value, 'Newer edits typed while saving');
  assert.notEqual(ui.context.noteDraft.id, 'stable-draft-id');
  const savedDraft = JSON.parse(ui.storage.get(ui.context.draftKey('m1')));
  assert.equal(savedDraft.body, 'Newer edits typed while saving');
  assert.equal(savedDraft.frame, 42);
  assert.equal(savedDraft.id, ui.context.noteDraft.id);
  assert.match(ui.get('note-status').textContent, /newer edits remain a local draft/);
});

test('late note confirmation cannot clear the next match workspace', async () => {
  let complete;
  const ui = noteUI({ save: () => new Promise(resolve => { complete = resolve; }) });
  const first = ui.context.saveInvestigationNote();
  ui.context.activeInvestigationMatch = 'm2';
  ui.context.noteDraft = { id: 'other-draft', matchID: 'm2', body: 'Other match note', frame: 100 };
  ui.get('note-body').value = 'Other match note';
  ui.get('note-frame').value = '100';
  ui.context.persistNoteDraft();
  complete({ note_id: 'stable-draft-id' });
  await first;
  assert.equal(ui.context.noteDraft.id, 'other-draft');
  assert.equal(ui.get('note-body').value, 'Other match note');
  assert.equal(JSON.parse(ui.storage.get(ui.context.draftKey('m2'))).body, 'Other match note');
});

test('confirmed note remains saved after setup replaced and removed the editor', async () => {
  let complete;
  const ui = noteUI({ save: () => new Promise(resolve => { complete = resolve; }) });
  const pending = ui.context.saveInvestigationNote();
  ui.context.investigationRequest++;
  ui.context.$ = id => ['note-body', 'note-frame', 'note-status'].includes(id) ? null : ui.get(id);
  complete({ note_id: 'stable-draft-id' });
  await pending;
  assert.ok(ui.notices.some(notice => notice.tone === 'ok' && /note saved/.test(notice.message)));
  assert.ok(ui.notices.every(notice => notice.tone !== 'err'), 'removed editor cannot turn confirmed save into failure');
  assert.equal(ui.storage.has(ui.context.draftKey('m1')), false);
  assert.equal(ui.context.noteDraft.body, '');
});

test('newer draft survives confirmation after another modal removes its editor', async () => {
  let complete;
  const ui = noteUI({ save: () => new Promise(resolve => { complete = resolve; }) });
  const pending = ui.context.saveInvestigationNote();
  ui.get('note-body').value = 'Newer draft before switching screens';
  ui.context.persistNoteDraft();
  ui.context.investigationRequest++;
  ui.context.$ = id => ['note-body', 'note-frame', 'note-status'].includes(id) ? null : ui.get(id);
  complete({ note_id: 'stable-draft-id' });
  await pending;
  const retained = JSON.parse(ui.storage.get(ui.context.draftKey('m1')));
  assert.equal(retained.body, 'Newer draft before switching screens');
  assert.notEqual(retained.id, 'stable-draft-id');
  assert.equal(ui.context.noteDraft.body, retained.body);
  assert.ok(ui.notices.every(notice => notice.tone !== 'err'));
});

function modalUI() {
  const get = nodes(), pending = [];
  get('lab-dialog').open = false;
  get('lab-dialog').showModal = () => { get('lab-dialog').open = true; };
  const context = run(section('  function openLabDialog(', '  function calibrationDetailsHTML(') + section('  async function openInvestigation(', '  async function loadStudio('), {
    $: get, investigationRequest: 0, persistNoteDraft: () => {},
    getJSON: url => new Promise((resolve, reject) => pending.push({ url, resolve, reject })),
    screenState: (title, message) => `${escape(title)} ${escape(message)}`,
    renderInvestigation: data => `Review ${data.matchID}`,
  });
  return { context, get, pending };
}

test('late setup success or failure cannot replace a newer review workspace', async () => {
  for (const fail of [false, true]) {
    const ui = modalUI();
    const setup = ui.context.showSetup();
    const review = ui.context.openInvestigation('m2');
    ui.pending[1].resolve({ matchID: 'm2' });
    await review;
    assert.equal(ui.get('lab-dialog-content').innerHTML, 'Review m2');
    if (fail) ui.pending[0].reject(new Error('Late setup error'));
    else ui.pending[0].resolve({ writable: true, spark_installed: true, steps: [] });
    await setup;
    assert.equal(ui.get('lab-dialog-content').innerHTML, 'Review m2');
    assert.equal(ui.get('lab-dialog-title').textContent, 'Replay review · m2');
  }
});

test('late investigation cannot overwrite a different modal or reopen a closed dialog', async () => {
  const ui = modalUI();
  const review = ui.context.openInvestigation('m1');
  ui.context.openLabDialog('Another task', 'Keep this screen');
  ui.pending[0].resolve({ matchID: 'm1' });
  await review;
  assert.equal(ui.get('lab-dialog-content').innerHTML, 'Keep this screen');
  const setup = ui.context.showSetup();
  ui.get('lab-dialog').open = false;
  ui.context.investigationRequest++;
  ui.pending[1].resolve({ writable: true, spark_installed: true });
  await setup;
  assert.equal(ui.get('lab-dialog').open, false);
});

test('new investigation controls stay disabled with reason during an earlier note save', () => {
  const context = run(section('  function renderInvestigation(', '  function selectIncident('), {
    activeInvestigationMatch: '', activeInvestigationData: null, activePlaylist: [], activePlaylistIndex: -1,
    selectedIncidentIndex: 0, noteSavePending: true, noteDraft: null, prefs: {}, blindReview: false,
    readNoteDraft: matchID => ({ matchID, body: 'Current draft', frame: -1 }),
    savePrefs: () => {}, fmtInt: String, fmtNum: String, who: escape, timeAgo: escape,
    speedChart: () => '', selectedIncidentDetails: () => '', renderNotes: () => 'Saved rows', noteMatchRendered: () => {},
  });
  const html = context.renderInvestigation({ match: { match_id: 'new-match' }, incidents: [], notes: [{}, {}] });
  assert.match(html, /data-add-note="new-match" disabled aria-describedby="note-status"/);
  assert.match(html, /data-add-bookmark="new-match" disabled aria-describedby="note-status"/);
  assert.match(html, /Another note save is awaiting confirmation/);
  assert.ok(html.indexOf('<nav class="review-footer"') < html.indexOf('class="review-workspace-grid"'));
  assert.match(html, /Saved notes · <span id="saved-note-count">2/);
  assert.match(html, /<\/details><div class="form-row">[\s\S]*<textarea id="note-body"/);
});

function quitUI(options = {}) {
  const get = nodes(), requests = [], messages = [], timers = [], removed = [], main = { innerHTML: 'Preserved workspace' };
  const nav = { hidden: false }, skip = { hidden: false }, connectionLabel = { textContent: 'CONNECTED' };
  const controls = ['quit', 'open-viewer', 'quick-setup'].map(get);
  for (const control of controls) { control.attributes = {}; control.setAttribute = (key, value) => { control.attributes[key] = value; }; }
  get('connection').querySelector = () => connectionLabel;
  get('connection').className = 'connection online';
  let handler;
  get('quit').textContent = 'Quit';
  get('quit').addEventListener = (_name, callback) => { handler = callback; };
  get('lab-dialog').close = () => { get('lab-dialog').open = false; };
  const context = run(section("  $('quit').addEventListener('click'", "  $('retry').addEventListener('click'"), {
    $: get, intentionallyStopped: false, noteSavePending: !!options.savePending, AbortController, operatorState: { connection: 'online' },
    confirm: () => true, persistNoteDraft: () => {},
    fetch: async (...args) => { requests.push(args); return options.fetch ? options.fetch(...args) : { ok: true, status: 200 }; },
    setStatus: (message, tone) => messages.push({ message, tone }),
    document: { querySelector: selector => ({ main, '.workspace-nav': nav, '.skip-link': skip }[selector] || null), querySelectorAll: selector => selector === '.top button' ? controls : [] },
    setTimeout: (callback, duration) => { const timer = { callback, duration }; timers.push(timer); return timer; },
    clearTimeout: timer => removed.push(timer),
  });
  return { context, get, click: () => handler(), requests, messages, main, timers, removed, nav, skip, connectionLabel, controls };
}

test('quit requires a successful response and reports accepted shutdown, not process completion', async () => {
  const ui = quitUI();
  await ui.click();
  assert.equal(ui.context.intentionallyStopped, true);
  assert.match(ui.main.innerHTML, /Shutdown requested/);
  assert.doesNotMatch(ui.main.innerHTML, /has stopped/);
  assert.equal(ui.get('quit').disabled, true);
  assert.equal(ui.timers[0].duration, 10000);
  assert.deepEqual(ui.removed, ui.timers);
  await ui.click();
  assert.equal(ui.requests.length, 1);
});

test('accepted shutdown replaces connected header and disables terminal workspace actions', async () => {
  const ui = quitUI();
  ui.get('offline-banner').hidden = false;
  await ui.click();
  assert.equal(ui.context.operatorState.connection, 'closing');
  assert.equal(ui.get('connection').className, 'connection closing');
  assert.equal(ui.connectionLabel.textContent, 'SHUTDOWN REQUESTED');
  assert.equal(ui.get('offline-banner').hidden, true);
  assert.equal(ui.nav.hidden, true);
  assert.equal(ui.skip.hidden, true);
  assert.equal(ui.get('retry').disabled, true);
  for (const control of ui.controls) {
    assert.equal(control.disabled, true);
    assert.match(control.title, /local engine is closing/);
    assert.equal(control.attributes['aria-describedby'], 'shutdown-description');
  }
  assert.match(ui.main.innerHTML, /id="shutdown-description"/);
  assert.match(ui.main.innerHTML, /controls are unavailable until you reopen/);
});

test('failed quit retains workspace and permits retry without claiming shutdown', async () => {
  for (const failure of [() => ({ ok: false, status: 403 }), () => { throw new Error('Network error'); }]) {
    const ui = quitUI({ fetch: failure });
    await ui.click();
    assert.equal(ui.context.intentionallyStopped, false);
    assert.equal(ui.main.innerHTML, 'Preserved workspace');
    assert.equal(ui.get('quit').disabled, false);
    assert.equal(ui.get('connection').className, 'connection online');
    assert.equal(ui.nav.hidden, false);
    assert.ok(ui.controls.filter(control => control.id !== 'quit').every(control => !control.disabled));
    assert.match(ui.messages.at(-1).message, /Shutdown was not confirmed/);
    assert.deepEqual(ui.removed, ui.timers);
  }
});

test('quit waits for pending note confirmation and coalesces duplicate requests', async () => {
  const saving = quitUI({ savePending: true });
  await saving.click();
  assert.equal(saving.requests.length, 0);
  assert.match(saving.messages.at(-1).message, /note save is still awaiting confirmation/);
  let complete;
  const ui = quitUI({ fetch: () => new Promise(resolve => { complete = resolve; }) });
  const pending = ui.click();
  await ui.click();
  assert.equal(ui.requests.length, 1);
  complete({ ok: true, status: 200 });
  await pending;
});

test('draft recovery is bounded and separated by evidence store and match', () => {
  const ui = noteUI();
  const originalKey = ui.context.draftKey('m1');
  assert.notEqual(originalKey, ui.context.draftKey('m2'));
  ui.storage.set(originalKey, JSON.stringify({ id: 'recovered-id', matchID: 'm1', body: 'a'.repeat(9000), frame: 12 }));
  assert.equal(ui.context.readNoteDraft('m1').body.length, 8000);
  assert.equal(ui.context.readNoteDraft('m1').id, 'recovered-id');
  ui.context.operatorState.health.database_path = 'another-synthetic-store';
  assert.notEqual(originalKey, ui.context.draftKey('m1'));
  assert.equal(ui.context.readNoteDraft('m1').body, '');
});

test('invalid draft storage and unavailable local storage do not discard editable text', async () => {
  const ui = noteUI({ storageFails: true, save: () => { throw new Error('Offline'); } });
  ui.storage.set(ui.context.draftKey('m1'), '{not JSON');
  assert.equal(ui.context.readNoteDraft('m1').body, '');
  const original = ui.get('note-body').value;
  assert.equal(ui.context.persistNoteDraft(), false);
  await ui.context.saveInvestigationNote();
  assert.equal(ui.get('note-body').value, original);
  assert.match(ui.get('note-status').textContent, /if local storage is available/);
});

test('invalid notes and bookmark frames never submit to backend', async () => {
  for (const { body, frame, bookmark, field } of [
    { body: ' ', frame: '-1', bookmark: false, field: 'note-body' },
    { body: 'Some text', frame: '-2', bookmark: false, field: 'note-frame' },
    { body: '', frame: '-1', bookmark: true, field: 'note-frame' },
    { body: '', frame: '1.5', bookmark: true, field: 'note-frame' },
  ]) {
    const ui = noteUI();
    ui.get('note-body').value = body;
    ui.get('note-frame').value = frame;
    await ui.context.saveInvestigationNote(bookmark);
    assert.equal(ui.writes.length, 0);
    assert.equal(ui.get(field).focused, true);
    assert.match(ui.get('note-status').className, /err/);
  }
});

test('saved note markup is escaped even in identifier and kind fields', () => {
  const ui = noteUI();
  const html = ui.context.renderNotes([{ note_id: '\"><img src=x>', body: '<script>bad()</script>', kind: '<svg>', frame_index: 20 }]);
  assert.doesNotMatch(html, /<img|<script|<svg/);
  assert.match(html, /&lt;script/);
  assert.match(html, /&quot;&gt;&lt;img/);
});

function keyEvent(key, options = {}) {
  const region = { classList: { contains: name => name === (options.region || 'review-queue') } };
  return {
    key, prevented: false,
    target: { closest: selector => selector.startsWith('input,') ? (options.editable ? {} : null) : options.outside ? null : region },
    preventDefault() { this.prevented = true; },
    ...options,
  };
}

function keyboardUI(enabled = false, dialogOpen = true) {
  const selected = [], focused = [], rows = [0, 1, 2].map(index => ({ dataset: { playlistRow: String(index) }, classList: { toggle: () => {} } }));
  let handler, clicks = 0;
  const context = run(section('  function shortcutTarget(', "  document.addEventListener('input', e =>"), {
    prefs: { shortcuts: enabled }, $: () => ({ open: dialogOpen }),
    selectedIncidentIndex: 1, activePlaylist: [0, 1, 2], activePlaylistIndex: 1,
    selectIncident: (...args) => selected.push(args),
    document: {
      addEventListener: (event, listener) => { assert.equal(event, 'keydown'); handler = listener; },
      querySelectorAll: () => rows,
      querySelector: selector => ({ focus: () => focused.push(selector), click: () => { clicks++; } }),
    },
  });
  return { context, dispatch: event => handler(event), selected, focused, clicks: () => clicks };
}

test('keyboard shortcuts are opt-in, focus-scoped and inactive inside editable controls', () => {
  const ui = keyboardUI(false);
  ui.dispatch(keyEvent('j'));
  assert.equal(ui.selected.length, 0);
  ui.context.prefs.shortcuts = true;
  const event = keyEvent('j');
  ui.dispatch(event);
  assert.deepEqual(ui.selected, [[2, true]]);
  assert.equal(event.prevented, true);
  for (const invalid of [
    keyEvent('j', { editable: true }), keyEvent('ArrowDown', { editable: true }),
    keyEvent('k', { outside: true }), keyEvent('ArrowUp', { outside: true }),
    keyEvent('j', { isComposing: true }), keyEvent('j', { ctrlKey: true }),
    keyEvent('j', { altKey: true }), keyEvent('j', { metaKey: true }), keyEvent('j', { shiftKey: true }),
    keyEvent('j', { target: null }),
  ]) {
    ui.dispatch(invalid);
    assert.equal(invalid.prevented, false);
  }
  assert.equal(ui.selected.length, 1);
});

test('arrows navigate a focused queue without enabling character-only shortcuts', () => {
  const ui = keyboardUI(false);
  ui.dispatch(keyEvent('ArrowDown'));
  ui.dispatch(keyEvent('ArrowUp'));
  assert.deepEqual(ui.selected, [[2, true], [0, true]]);
  assert.equal(ui.clicks(), 0);
  const closed = keyboardUI(true, false);
  closed.dispatch(keyEvent('ArrowDown'));
  assert.equal(closed.selected.length, 0);
});

test('Enter, Space and unrelated shortcuts never auto-launch a replay or review action', () => {
  const ui = keyboardUI(true);
  for (const key of ['Enter', ' ', 'b', 'Delete', 'Escape']) {
    const event = keyEvent(key);
    ui.dispatch(event);
    assert.equal(event.prevented, false);
  }
  assert.equal(ui.selected.length, 0);
  assert.equal(ui.clicks(), 0);
  assert.equal(ui.focused.length, 0);
});

test('playlist keyboard navigation focuses a real control without launching it', () => {
  const ui = keyboardUI(true);
  ui.dispatch(keyEvent('ArrowDown', { region: 'playlist' }));
  assert.equal(ui.context.activePlaylistIndex, 2);
  ui.dispatch(keyEvent('ArrowDown', { region: 'playlist' }));
  assert.equal(ui.context.activePlaylistIndex, 2, 'selection is bounded by the playlist');
  ui.dispatch(keyEvent('k', { region: 'playlist' }));
  assert.equal(ui.context.activePlaylistIndex, 1);
  assert.match(ui.focused.at(-1), /data-playlist-index="1"/);
  assert.equal(ui.clicks(), 0);
  assert.equal(ui.selected.length, 0);
});

// ---- report formatting, window lifecycle and stored preferences -------------

test('match duration rounds the total before splitting so seconds never read 60', () => {
  const { fmtDur } = run(section('  const fmtDur =', '  const fmtClock =') + '\nthis.fmtDur = fmtDur;');
  assert.equal(fmtDur(1199.97), '20 min');
  assert.equal(fmtDur(1199.4), '19 min 59 s');
  assert.equal(fmtDur(59.6), '1 min');
  assert.equal(fmtDur(61.2), '1 min 1 s');
  assert.equal(fmtDur(24), '24 s');
  assert.equal(fmtDur(0), '—');
  for (let sec = 0.25; sec < 7300; sec += 0.37) assert.doesNotMatch(fmtDur(sec), /\b60 s/);
});

function prefsStore(options = {}) {
  const requests = [], timers = [], storage = new Map(Object.entries(options.storage || {}));
  const context = run(section('  const uiStore =', '  const savePrefs =') + '\nthis.uiStore = uiStore;', {
    prefs: options.prefs || {}, prefKey: 'nevr-desktop-preferences-v1', TextEncoder, AbortSignal, Date: { now: () => 5000 },
    localStorage: { getItem: key => storage.get(key) ?? null, setItem: (key, value) => storage.set(key, value) },
    fetch: async (url, init = {}) => { requests.push({ url, method: init.method || 'GET', body: init.body, keepalive: init.keepalive }); return options.respond(url, init); },
    setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; }, clearTimeout: () => {},
  });
  return { context, requests, timers, storage };
}

test('an engine without api/ui-prefs leaves preferences in localStorage and is never written to', async () => {
  const ui = prefsStore({ prefs: { theme: 'light' }, respond: () => ({ ok: false, status: 404, json: async () => ({}) }) });
  assert.equal(await ui.context.loadUIPrefs(), false);
  assert.equal(ui.context.uiStore.available, false);
  ui.context.queuePrefsSave();
  assert.equal(ui.context.mirrorNoteDraft({ id: 'd1', matchID: 'm1', body: 'kept locally', frame: 4 }), false);
  await ui.context.pushUIPrefs(true);
  assert.deepEqual(ui.requests.map(r => r.method), ['GET']);
  assert.equal(ui.timers.length, 0);
  assert.equal(ui.context.prefs.theme, 'light');
});

test('stored preferences and unsent drafts survive a new origin; an unreadable store is never overwritten', async () => {
  const stored = { schema: 'nevr-ui-prefs/v1', updated_at: 9000, prefs: { theme: 'light', blindReview: true, updatedAt: 9000 },
    note_drafts: { m1: { id: 'draft-1', matchID: 'm1', body: 'x'.repeat(9000), frame: 12, at: 1 }, wrong: { id: 'draft-2', matchID: 'other', body: 'mismatched key', frame: 1 }, broken: 'not an object' } };
  const ui = prefsStore({ respond: () => ({ ok: true, status: 200, json: async () => stored }) });
  assert.equal(await ui.context.loadUIPrefs(), true);
  assert.equal(ui.context.prefs.theme, 'light');
  assert.equal(ui.context.prefs.blindReview, true);
  assert.equal(JSON.parse(ui.storage.get('nevr-desktop-preferences-v1')).theme, 'light');
  const draft = ui.context.serverNoteDraft('m1');
  assert.equal(draft.id, 'draft-1');
  assert.equal(draft.body.length, 8000);
  assert.equal(draft.frame, 12);
  assert.equal(ui.context.serverNoteDraft('wrong'), null);
  assert.equal(ui.context.serverNoteDraft('broken'), null);
  assert.equal(ui.requests.length, 1, 'reading the newer stored copy writes nothing back');

  const failing = prefsStore({ prefs: { theme: 'dark' }, respond: () => ({ ok: false, status: 503, json: async () => ({}) }) });
  assert.equal(await failing.context.loadUIPrefs(), false);
  failing.context.queuePrefsSave();
  await failing.context.pushUIPrefs();
  assert.deepEqual(failing.requests.map(r => r.method), ['GET']);
});

test('newer local preferences seed the store, and the payload stays under the 64 KiB contract', async () => {
  const ui = prefsStore({ prefs: { theme: 'system', updatedAt: 9000 }, respond: (_url, init) => init.method === 'PUT' ? { ok: true, status: 204 } : { ok: true, status: 200, json: async () => ({ updated_at: 10, prefs: { theme: 'dark' } }) } });
  assert.equal(await ui.context.loadUIPrefs(), false);
  assert.equal(ui.context.prefs.theme, 'system');
  assert.equal(ui.timers.at(-1).delay, 400);
  for (let i = 0; i < 9; i++) assert.equal(ui.context.mirrorNoteDraft({ id: `d${i}`, matchID: `m${i}`, body: 'é'.repeat(8000), frame: i }), true);
  assert.equal(ui.context.mirrorNoteDraft({ matchID: 'm8', body: '   ' }), true, 'an emptied draft is removed from the store');
  await ui.timers.at(-1).callback();
  const put = ui.requests.at(-1);
  assert.equal(put.method, 'PUT');
  assert.equal(put.url, 'api/ui-prefs');
  assert.ok(Buffer.byteLength(put.body) <= 64 * 1024, `payload ${Buffer.byteLength(put.body)} bytes`);
  const body = JSON.parse(put.body);
  assert.equal(body.schema, 'nevr-ui-prefs/v1');
  assert.equal(body.prefs.theme, 'system');
  assert.ok(Object.keys(body.note_drafts).length >= 1 && !('m8' in body.note_drafts));
  assert.equal(ui.context.uiStore.dirty, false);
});

test('note drafts fall back to the stored copy and report retention honestly', () => {
  const ui = noteUI({ serverDraft: id => id === 'm9' ? { id: 'stored-draft', matchID: 'm9', body: 'typed before the restart', frame: 3 } : null });
  assert.equal(ui.context.readNoteDraft('m9').id, 'stored-draft');
  assert.equal(ui.context.readNoteDraft('m2').body, '');
  assert.equal(ui.context.persistNoteDraft(), true);
  assert.equal(ui.mirrored.at(-1).matchID, 'm1');
  const storeOnly = noteUI({ storageFails: true, mirrorStores: true });
  assert.equal(storeOnly.context.persistNoteDraft(), true, 'the stored copy alone still retains the draft');
});

function lifecycle(respond, stopped = false) {
  const requests = [], timers = [], beacons = [], listeners = {}, flushed = [];
  const context = run(section('  let heartbeatEvery = 5000;', '  probeConnection();'), {
    intentionallyStopped: stopped,
    fetch: async (url, init) => { requests.push({ url, method: init.method }); return respond(); },
    setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; },
    addEventListener: (type, handler) => { listeners[type] = handler; },
    navigator: { sendBeacon: url => { beacons.push(url); return true; } },
    pushUIPrefs: keepalive => flushed.push(keepalive),
  });
  return { context, requests, timers, beacons, listeners, flushed };
}

test('heartbeat posts every five seconds, tolerates an engine without the route and signals leaving', async () => {
  const ok = lifecycle(() => ({ status: 204 }));
  await ok.context.heartbeat();
  assert.deepEqual(ok.requests, [{ url: 'api/heartbeat', method: 'POST' }]);
  assert.equal(ok.timers.at(-1).delay, 5000);
  ok.listeners.pagehide();
  assert.deepEqual(ok.beacons, ['api/heartbeat?leaving=1']);
  assert.deepEqual(ok.flushed, [true]);

  const missing = lifecycle(() => ({ status: 404 }));
  await missing.context.heartbeat();
  assert.equal(missing.timers.at(-1).delay, 60000, 'a 404 is ignored and only re-asked once a minute');

  const offline = lifecycle(() => { throw new Error('connection refused'); });
  await offline.context.heartbeat();
  assert.equal(offline.timers.at(-1).delay, 5000);

  const stopped = lifecycle(() => ({ status: 204 }), true);
  await stopped.context.heartbeat();
  stopped.listeners.pagehide();
  assert.equal(stopped.requests.length + stopped.beacons.length + stopped.timers.length, 0, 'Quit already stopped the engine');
});

test('closed advanced sections fetch nothing and load once opened', () => {
  const sections = { 'advanced-tools': { open: false }, 'advanced-calibration': { open: true } };
  const context = run(section('  const lazyStale =', '  async function loadCalibration(') + '\nthis.lazyStale = lazyStale;', { $: id => sections[id] });
  context.lazyStale['advanced-tools'] = false;
  assert.equal(context.deferUntilOpen('advanced-tools'), true);
  assert.equal(context.lazyStale['advanced-tools'], true, 'a refresh while closed marks the section stale');
  assert.equal(context.deferUntilOpen('advanced-calibration'), false);
  assert.match(script, /async function loadStudio\(\) \{\s*if \(deferUntilOpen\('advanced-tools'\)\) return;/);
  assert.match(script, /async function loadCalibration\(\) \{\s*if \(deferUntilOpen\('advanced-calibration'\)\) return;/);
  assert.match(script, /async function loadRegression\(\) \{\s*if \(deferUntilOpen\('advanced-calibration'\)\) return;/);
  assert.match(script, /addEventListener\('toggle'/);
});

test('storage measurements are requested at most once a minute by panel refreshes', () => {
  let now = 1000000, loads = 0;
  const timers = [];
  const context = run(section('  const HEALTH_MIN_INTERVAL =', '  // loadCalibration, loadRegression'), {
    Date: { now: () => now }, loadHealth: () => { loads++; },
    setTimeout: (callback, delay) => { timers.push({ callback, delay }); return timers.length; }, clearTimeout: () => {},
  });
  context.refreshHealth();
  assert.equal(loads, 1);
  now += 20000; context.refreshHealth();
  assert.equal(loads, 1);
  assert.equal(timers.at(-1).delay, 40000);
  now += 40000; timers.at(-1).callback();
  assert.equal(loads, 2);
});

test('navigation offers a Results entry only while results exist and follows the scroll position', () => {
  const markup = page.slice(page.indexOf('<body>'), page.indexOf('<script>'));
  assert.match(markup, /<a href="#h-results" id="nav-results" hidden>Results<\/a>/);
  assert.equal((markup.match(/aria-current="location"/g) || []).length, 1);
  assert.match(script, /new IntersectionObserver\(/);
  assert.match(script, /\$\('nav-results'\)\.hidden = false/);
  assert.match(script, /\$\('nav-results'\)\.hidden = true/);
});

// ---- blinded review ---------------------------------------------------------
// Synthetic identifiers only. Every string below that must stay hidden is unique,
// so a single leak anywhere in the rendered markup is caught by name.
const HIDDEN = ['THROW_001', 'ZZ_SECRET_777', 'Hidden detector name', '9.9.9-secret', 'observed-secret', 'explanation-secret', 'expected-secret',
  'critical', 'shadow', 'action_worthy', 'score 91', 'Ban recommended', 'strongest', 'data-replay-event', 'data-physics-event', 'diagnostic/event'];
function blindMatch() {
  const event = (id, frame, detector, extra = {}) => ({ event_id: id, detector_id: detector, detector_name: 'Hidden detector name', detector_version: '9.9.9-secret',
    player_id: 'player-a', player_name: 'Synthetic Player', frame_index: frame, frame_range_start: frame, frame_range_end: frame + 2, timestamp: frame / 30,
    severity: 0.2, severity_label: 'low', confidence: 0.5, observed_value: 'observed-secret', explanation: 'explanation-secret', expected_range: 'expected-secret',
    is_shadow: false, merged_count: 3, ...extra });
  return { match_id: 'synthetic-match', players: [{ player_id: 'player-a', coverage: { version: 1, detectors: [{ detector_id: 'ZZ_SECRET_777', enabled: true }] } }],
    events: [event('ev-late', 900, 'THROW_001', { severity: 0.99, severity_label: 'critical' }), event('ev-early', 30, 'ZZ_SECRET_777', { is_shadow: true }), event('ev-mid', 400, 'THROW_001')],
    cases: [{ case_id: 'case-1', player_id: 'player-a', player_name: 'Synthetic Player', level: 'action_worthy', suspicion_score: 91, recommended_action: 'Ban recommended', status: 'pending' }] };
}
function blindUI(blind) {
  const context = run(section('  function assessmentFilter(', '  function diagBlock('), {
    // The sliced region may carry blocks that register page-level listeners at load (the match timeline does);
    // a no-op document keeps this slice loadable whichever order the UI pull requests merge in.
    document: { addEventListener() {}, querySelectorAll: () => [], activeElement: null },
    blindReview: blind, revealedEvents: new Set(), EVENT_LIMIT: 50, fmtInt: String, fmtNum: String, fmtClock: String, fmtBytes: String,
    who: (name, id) => `${escape(name)} <code>${escape(id)}</code>`, team: escape, lvlName: escape,
    meter: (value, tone) => `<meter data-tone="${tone || ''}">${value}</meter>`, sevBadge: label => `<b>${escape(label)}</b>`, badge: level => `<b>${escape(level)}</b>`,
    strongestPlayerEvent: m => m.events[0], replayButton: (m, e, label) => `<button data-replay-event="${escape(e.event_id)}">${escape(label)}</button>`,
  });
  const render = (m, detectorID = '') => context.assessmentFilter(m, 0) + context.eventsTable(m, 0, false, detectorID) + context.casesBlock(m);
  return { context, render };
}

test('blinded match view names no detector, severity, ranking or case conclusion anywhere', () => {
  const m = blindMatch();
  const open = blindUI(false).render(m);
  for (const secret of HIDDEN) assert.ok(open.includes(secret), `the unblinded view shows "${secret}", so the blinded assertion below has power`);
  assert.match(open, /<select id="cheat-filter-0"/);

  for (const detectorID of ['', 'THROW_001', 'ZZ_SECRET_777']) {
    const html = blindUI(true).render(m, detectorID);
    for (const secret of HIDDEN) assert.ok(!html.includes(secret), `blinded view leaks "${secret}" (filter "${detectorID}")`);
    assert.doesNotMatch(html, /<select|<option/);
    assert.match(html, /Detector filter concealed during blinded review · 3 observations/);
    // A stale or forged detector filter cannot narrow concealed rows to one detector.
    assert.equal((html.match(/data-event-row=/g) || []).length, 3);
    // Match order, not "strongest first".
    assert.ok(html.indexOf('data-event-row="ev-early"') < html.indexOf('data-event-row="ev-mid"'));
    assert.ok(html.indexOf('data-event-row="ev-mid"') < html.indexOf('data-event-row="ev-late"'));
    // The clip is requested by frame: the engine names event clips after the detector.
    assert.match(html, /data-replay-match="synthetic-match" data-replay-frame="30"/);
    assert.match(html, /Review cases, levels, scores and recommended actions are concealed/);
    assert.doesNotMatch(html, /case-1/);
  }
});

test('revealing one observation unblinds that row only', () => {
  const ui = blindUI(true), m = blindMatch();
  ui.context.revealedEvents.add('ev-mid');
  const html = ui.context.eventsTable(m, 0, false);
  const rows = html.split('<tbody class="incident">').slice(1).map(row => row.split('</tbody>')[0]);
  assert.equal(rows.length, 3);
  assert.match(rows[1], /data-event-row="ev-mid"/);
  assert.match(rows[1], /THROW_001/);
  assert.match(rows[1], /data-replay-event="ev-mid"/);
  assert.doesNotMatch(rows[1], /recorded as blinded/);
  for (const row of [rows[0], rows[2]]) {
    for (const secret of HIDDEN) assert.ok(!row.includes(secret), `concealed row leaks ${secret}`);
    assert.match(row, /Decision will be recorded as blinded\./);
  }
});

test('a label is recorded as blind only if this session never showed that match unblinded', () => {
  const m = blindMatch();
  const fresh = blindUI(true);
  fresh.context.noteMatchRendered(m);
  assert.equal(fresh.context.blindEligible('ev-early'), true);
  fresh.context.revealedEvents.add('ev-early');
  assert.equal(fresh.context.blindEligible('ev-early'), false);
  assert.equal(fresh.context.blindEligible('ev-mid'), true);

  // Seen unblinded first, then blinded review switched on: rows are concealed again, labels are not blind.
  const seen = blindUI(false);
  seen.context.noteMatchRendered(m);
  seen.context.blindReview = true;
  seen.context.revealedEvents.clear();
  for (const e of m.events) assert.equal(seen.context.blindEligible(e.event_id), false);
  const html = seen.context.eventsTable(m, 0, false);
  for (const secret of HIDDEN) assert.ok(!html.includes(secret), `re-concealed view leaks ${secret}`);
  assert.equal((html.match(/>Shown earlier; not recorded as blinded.</g) || []).length, 3);
  assert.doesNotMatch(html, /Decision will be recorded as blinded\./);

  // Exposed by match id before the match payload was loaded (case queue, History filter, report tool).
  const early = blindUI(true);
  early.context.exposeMatch('synthetic-match', []);
  early.context.noteMatchRendered(m);
  assert.equal(early.context.blindEligible('ev-late'), false);
  early.context.noteMatchRendered({ match_id: 'other-match', events: [{ event_id: 'other-event' }] });
  assert.equal(early.context.blindEligible('other-event'), true);

  // Exposed while already loaded (a report tool opened from a blinded match).
  const tool = blindUI(true);
  tool.context.noteMatchRendered(m);
  tool.context.exposeMatch('synthetic-match', [null, m]);
  assert.equal(tool.context.blindEligible('ev-mid'), false);

  // Blinded review off never yields a blind label.
  assert.equal(blindUI(false).context.blindEligible('never-rendered'), false);
});

function reviewClick(globals) {
  const posts = [], messages = [];
  const source = 'async function clickReview(e) {\n' + section("    const reviewButton = e.target.closest('button[data-review-event]');", "    const replay = e.target.closest('button[data-replay-event]');") + '\n}';
  const context = run(source, {
    activeInvestigationData: null, views: [blindMatch()], revealedEvents: new Set(), prompt: () => null,
    postJSON: async (url, payload) => { posts.push({ url, payload }); return { verdict: payload.verdict, comment: '', reviewed_at: 'now', blind_review: payload.blind_review }; },
    document: { querySelectorAll: () => [], querySelector: () => null }, $: () => ({ open: false }),
    reviewControls: () => '', selectIncident: () => {}, selectedIncidentIndex: 0,
    setStatus: (text, tone) => messages.push({ text, tone }), loadHealth: () => {}, loadRegression: () => {}, ...globals,
  });
  const button = { dataset: { reviewEvent: 'ev-mid', verdict: 'no' }, disabled: false };
  return { posts, messages, click: () => context.clickReview({ target: { closest: selector => selector === 'button[data-review-event]' ? button : null } }) };
}

test('the review request carries blind_review only when the observation is still blind-eligible', async () => {
  const eligible = reviewClick({ blindReview: true, blindEligible: () => true });
  await eligible.click();
  assert.equal(eligible.posts[0].payload.blind_review, true);
  assert.doesNotMatch(eligible.messages.at(-1).text, /THROW_001/);

  // Concealed on screen, but already exposed this session: not a blind sample, and the status line still names nothing.
  const exposed = reviewClick({ blindReview: true, blindEligible: () => false });
  await exposed.click();
  assert.equal(exposed.posts[0].payload.blind_review, false);
  assert.doesNotMatch(exposed.messages.at(-1).text, /THROW_001/);

  const open = reviewClick({ blindReview: false, blindEligible: () => false });
  await open.click();
  assert.equal(open.posts[0].payload.blind_review, false);
  assert.match(open.messages.at(-1).text, /Saved THROW_001 as a false positive/);
});

test('blinded History hides signal counts and the per-detector filter; an unblinded detector filter ends blind recording for the listed matches', () => {
  const matches = [
    { match_id: 'with-signal', source: 'replay', analyzed_at: new Date(1000).toISOString(), players: [], event_count: 4, flagged: true, detector_ids: ['THROW_001'] },
    { match_id: 'without-signal', source: 'replay', analyzed_at: new Date(2000).toISOString(), players: [], event_count: 0, detector_ids: [] },
  ];
  const blind = historyUI(matches, 0, { blindReview: true, exposeMatch: () => assert.fail('a blinded list exposes nothing') });
  blind.get('history-detector').value = 'THROW_001';
  blind.context.renderHistory();
  const hidden = blind.get('history').innerHTML;
  assert.equal(blind.get('history-detector').disabled, true);
  assert.match(hidden, /with-signal/);
  assert.match(hidden, /without-signal/, 'the detector filter is ignored while blinded');
  assert.equal((hidden.match(/Signals hidden/g) || []).length, 2);
  assert.doesNotMatch(hidden, /Review case|observations<|No recorded observations/);

  const exposedIDs = [];
  const open = historyUI(matches, 0, { exposeMatch: id => exposedIDs.push(id) });
  open.context.renderHistory();
  assert.deepEqual(exposedIDs, [], 'an unfiltered list does not say which detector fired');
  assert.match(open.get('history').innerHTML, /Review case/);
  open.get('history-detector').value = 'THROW_001';
  open.context.renderHistory();
  assert.equal(open.get('history-detector').disabled, false);
  assert.deepEqual(exposedIDs, ['with-signal']);
});

test('blinded case queue hides scores and finding context and cannot be searched by detector', () => {
  const snapshot = { single_match: [{ case_id: 'case-1', player_id: 'p1', player_name: 'Synthetic Player', match_id: 'queued-match', status: 'pending', suspicion_score: 91.5, explanation: 'THROW_001 explanation-secret', detectors: { ZZ_SECRET_777: 2 } }],
    cross_match: [{ case_id: 'case-2', player_id: 'p2', player_name: 'Other Player', status: 'pending', decayed_score: 77.25, match_ids: ['queued-match'] }] };
  const ui = blind => {
    const get = nodes(), exposedIDs = [];
    const context = run(section('  function renderFlagged(', '  async function loadFlagged('), {
      $: get, caseSnapshot: snapshot, blindReview: blind, views: [], activeInvestigationData: null, exposeMatch: id => exposedIDs.push(id),
      fmtInt: String, fmtNum: String, who: (name, id) => `${escape(name)} ${escape(id)}`,
      screenState: (title, message) => `${escape(title)} ${escape(message)}`,
    });
    return { context, get, exposedIDs };
  };
  const blind = ui(true);
  blind.context.renderFlagged();
  const hidden = blind.get('flagged').innerHTML;
  for (const secret of ['THROW_001', 'explanation-secret', 'ZZ_SECRET_777', '91.5', '77.25']) assert.ok(!hidden.includes(secret), `blinded queue leaks ${secret}`);
  assert.match(hidden, /Finding context hidden/);
  assert.match(hidden, /Synthetic Player/);
  assert.deepEqual(blind.exposedIDs, []);
  for (const query of ['THROW_001', 'zz_secret_777', 'explanation-secret']) {
    blind.get('case-search').value = query;
    blind.context.renderFlagged();
    assert.match(blind.get('flagged').innerHTML, /No cases match these filters/, `a blinded queue must not confirm "${query}"`);
  }

  const open = ui(false);
  open.context.renderFlagged();
  assert.match(open.get('flagged').innerHTML, /THROW_001 explanation-secret/);
  assert.match(open.get('flagged').innerHTML, /91\.5/);
  assert.deepEqual(open.exposedIDs, ['queued-match'], 'showing the finding context ends blind recording for that match');
});

test('blinded review workspace conceals the shadow/scored tag, and match tools that cannot be blinded are marked', () => {
  const event = blindMatch().events[1];
  const render = (blind, revealed = []) => run(section('  function selectedIncidentDetails(', '  function renderInvestigation('), {
    blindReview: blind, revealedEvents: new Set(revealed), fmtInt: String, fmtNum: String, who: escape, screenState: () => '',
    reviewControls: (_e, concealed, recordedBlind) => `[concealed=${concealed} blind=${recordedBlind}]`, blindEligible: id => blind && !revealed.includes(id),
  }).selectedIncidentDetails({ match: { events: [event] } }, { id: 'INC-001', player_id: 'player-a', event_ids: ['ev-early'], start_frame: 30, end_frame: 32, start_time: 1 });
  const hidden = render(true);
  for (const secret of [...HIDDEN, 'Observation only', 'Scored review signal']) assert.ok(!hidden.includes(secret), `blinded workspace leaks ${secret}`);
  assert.match(hidden, /\[concealed=true blind=true\]/);
  assert.match(render(true, ['ev-early']), /Observation only/);
  assert.match(render(false), /Observation only/);

  const tools = run(section('  function calibrationControls(', '  function telemetryHealthBlock('), { fmtInt: String, fmtBytes: String, labelName: escape })
    .calibrationControls({ match_id: 'synthetic-match' });
  for (const tool of [/href="api\/match\/synthetic-match\/report"/, /data-compare-match=/, /data-sandbox-match=/]) {
    const tag = tools.match(/<(?:a|button)[^>]*>/g).find(markup => tool.test(markup));
    assert.match(tag, /data-unblinds-match="synthetic-match"/);
  }
  assert.match(script, /closest\('\[data-unblinds-match\]'\)[\s\S]{0,200}exposeMatch\(tool\.dataset\.unblindsMatch/);
});

test('switching blinded review on or off re-renders every identity surface and keeps the exposure record', () => {
  const source = 'function changeBlind(e) {\n' + section("    if (e.target.id === 'blind-review') {", "    const select = e.target.closest('select[data-cheat-filter]');") + '\n}';
  const m = blindMatch(), state = { detector: 'THROW_001', all: false };
  const slots = { events: { innerHTML: '' }, players: { innerHTML: '' }, controls: { outerHTML: '' }, total: { textContent: '' }, overview: { innerHTML: '' }, cases: { innerHTML: '' } };
  const root = { dataset: { assessment: '0' }, querySelector: selector => ({ '.events': slots.events, '[data-assessment-players]': slots.players, '.assessment-controls': slots.controls, '[data-detection-count]': slots.total })[selector] };
  const calls = [], rendered = [];
  const context = run(source, {
    blindReview: false, prefs: {}, savePrefs: () => calls.push('savePrefs'), revealedEvents: new Set(['ev-mid']), views: [m], viewState: [state], fmtInt: String,
    document: { querySelectorAll: () => [root], querySelector: selector => selector === '[data-review-overview="0"]' ? slots.overview : selector === '[data-match-cases="0"]' ? slots.cases : null },
    renderTimeline: () => {}, // present once the match timeline is merged; the blind toggle redraws it
    noteMatchRendered: match => rendered.push({ id: match.match_id, blindAtCall: context.blindReview }),
    eventsTable: (_m, _idx, all, detector) => `events(${detector})`, playersTable: (_m, detector) => `players(${detector})`, reviewOverview: (_m, detector) => `overview(${detector})`,
    assessmentFilter: () => `filter(blind=${context.blindReview})`, casesBlock: () => `cases(blind=${context.blindReview})`,
    renderHistory: () => calls.push('renderHistory'), renderFlagged: () => calls.push('renderFlagged'), setStatus: text => calls.push(text),
  });
  context.changeBlind({ target: { id: 'blind-review', checked: true } });
  assert.equal(context.blindReview, true);
  assert.equal(state.detector, '', 'an active detector filter is cleared, not silently kept behind concealed rows');
  assert.deepEqual(slots, { events: { innerHTML: 'events()' }, players: { innerHTML: 'players()' }, controls: { outerHTML: 'filter(blind=true)' }, total: { textContent: '3' }, overview: { innerHTML: 'overview()' }, cases: { innerHTML: 'cases(blind=true)' } });
  assert.ok(calls.includes('renderHistory') && calls.includes('renderFlagged'));
  assert.match(calls.at(-1), /already showed stay concealed, but their labels are not recorded as blinded/);

  rendered.length = 0;
  context.changeBlind({ target: { id: 'blind-review', checked: false } });
  assert.deepEqual(rendered, [{ id: 'synthetic-match', blindAtCall: false }], 'the view about to be shown unblinded is put on the exposure record');
  assert.equal(slots.controls.outerHTML, 'filter(blind=false)');
  assert.equal(slots.cases.innerHTML, 'cases(blind=false)');

  // The match section itself records an unblinded render, and offers the cases block for re-rendering.
  assert.match(script, /function matchSection\(m, source, intake\) \{[\s\S]{0,200}noteMatchRendered\(m\);/);
  assert.match(script, /<div data-match-cases="\$\{idx\}">\$\{casesBlock\(m\)\}<\/div>/);
  assert.match(script, /activeInvestigationData = d;[^\n]*\n\s*noteMatchRendered\(d\.match\);/);
});
