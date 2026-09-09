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
const escape = value => String(value ?? '').replace(/[&<>"']/g, ch => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));

test('native tape is accepted consistently by pickers, drop and folder intake', () => {
  assert.match(page, /id="files"[^>]*accept="\.echoreplay,\.tape,\.json"/);
  assert.match(page, /id="folder"[^>]*accept="\.echoreplay,\.tape,\.json"/);
  const started = [], messages = [], get = nodes();
  const context = run(section('  function analyze(', "  ['dragenter'"), {
    busy: false, MAX_UPLOAD_FILE_BYTES: 1000, queue: [], queuePanel: null,
    document: { createElement: () => ({ innerHTML: '' }) },
    drop: { appendChild() {}, classList: { add() {} } }, $: get,
    uploadList: () => '', setStatus: text => messages.push(text),
    runQueue: indices => started.push(Array.from(indices)),
  });
  context.analyze(['capture.TAPE', 'original.echoreplay', 'legacy.json', 'old.nevrcap', 'setup.exe'].map(name => ({name,size:100})));
  assert.deepEqual(Array.from(context.queue, entry => entry.file.name), ['capture.TAPE','original.echoreplay','legacy.json']);
  assert.deepEqual(started, [[0,1,2]]);
  assert.match(messages.at(-1), /Ignored 2 unsupported/);
});

test('native source notice distinguishes compatibility views from original evidence', () => {
  const source = section('    const notices =', '    const tel =');
  for (const native of [true, false]) {
    const context = run(source + '\nresult = notices;', {
      m: {source: native ? 'tape' : 'replay',replaced:false,warnings:[]}, fmtInt:String,
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

function historyUI(matches, historyPage = 0) {
  const get = nodes(), preferences = { lastMatch: 'match-16' };
  get('history-sort').value = 'oldest';
  const source = section('  function renderHistory(', '  async function loadHistory(');
  const context = run(source, {
    $: get, historyMatches: matches, historyPage, HISTORY_PAGE_SIZE: 15, prefs: preferences,
    savePrefs: () => {}, fmtInt: String, fmtStart: String, fmtDur: String, timeAgo: escape, labelName: escape,
    screenState: (title, message, actions) => `${escape(title)} ${escape(message)} ${actions}`,
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
  const context = run(section('  function recordNotice(', "  $('history-search').value"), {
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
  const get = nodes(), storage = new Map(), writes = [], notices = [], buttons = [{ disabled: false }, { disabled: false }];
  let nextID = 0;
  get('note-body').value = 'A synthetic note awaiting confirmation';
  get('note-frame').value = '-1';
  const context = run(section('  function draftKey(', '  function selectedIncidentDetails('), {
    $: get, noteDraft: { id: 'stable-draft-id', matchID: 'm1', body: get('note-body').value, frame: -1 }, noteSavePending: false, investigationRequest: 1,
    activeInvestigationMatch: 'm1', activeInvestigationData: { notes: [] },
    operatorState: { health: { database_path: 'synthetic-evidence-store' } }, location: { pathname: '/synthetic-session/' },
    crypto: { randomUUID: () => `new-draft-${++nextID}` }, fmtInt: String,
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
  return { context, get, storage, writes, notices, buttons };
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
    speedChart: () => '', selectedIncidentDetails: () => '', renderNotes: () => 'Saved rows',
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
