'use strict';

// Exercise the actual embedded-page rendering functions without a browser or
// third-party packages. All identities and incidents below are synthetic.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const page = fs.readFileSync(path.join(__dirname, '../cmd/desktop/index.html'), 'utf8');
const script = page.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script); // Also reject syntax errors anywhere in the application.
assert.doesNotMatch(page, /No player currently needs review/);
assert.match(page, /Scored review cases/);
assert.match(page, /Scored cases only/);
assert.match(page, /Check stored comparisons/);
assert.doesNotMatch(page, /certainly over cap/);
assert.match(page, /id="opportunity-truth"><option value="uncertain"/);
assert.doesNotMatch(page, /id="opportunity-blind"[^>]*checked/);
assert.match(page, /Choose an exact start and end frame/);
for (const id of ['opportunity-reviewer', 'opportunity-verifier', 'opportunity-verified-truth', 'opportunity-evidence-method', 'opportunity-evidence-reference']) {
  assert.ok(page.includes(`id="${id}"`), `independent evidence field ${id} is present`);
}
const start = script.indexOf('  function strongestPlayerEvent(');
const end = script.indexOf('  function assessmentFilter(', start);
assert.ok(start >= 0 && end > start, 'review rendering section exists');
const escape = (value) => String(value ?? '').replace(/[&<>"']/g, (ch) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[ch]));

function ui() {
  const context = vm.createContext({
    blindReview: false, esc: escape, fmtInt: String, fmtNum: String,
    who: (name, id) => `${escape(name)} <code>${escape(id)}</code>`,
    team: escape, bar: () => '', lvlTone: () => '',
  });
  vm.runInContext(script.slice(start, end), context);
  return context;
}

function fixture() {
  const players = [
    { player_id: 'unflagged', name: 'A spectator-independent fair reference', team: 'blue', frames: 600, score: 0, level: 'clean' },
    { player_id: 'arbitrary-thrower', name: 'Z synthetic thrower', team: 'orange', frames: 600, score: 0, level: 'clean' },
    { player_id: 'arbitrary-mover', name: 'M synthetic mover', team: 'blue', frames: 600, score: 0, level: 'clean' },
  ];
  for (const player of players) player.coverage = { version: 1, status: 'limited', detectors: ['THROW_001', 'MOV_006'].map((id) => ({ detector_id: id, enabled: true, status: 'limited', candidate_frames: 400, input_frames: 5, input_check: true, limitations: [] })) };
  const events = Array.from({ length: 5 }, (_, i) => ({
    event_id: `throw-${i}`, player_id: 'arbitrary-thrower', detector_id: 'THROW_001',
    detector_name: 'Impossible Release Velocity', is_shadow: true, severity: 0.5 + i / 10,
    frame_index: 100 + i * 20, merged_count: 9,
  }));
  events.push({ event_id: 'move-1', player_id: 'arbitrary-mover', detector_id: 'MOV_006', detector_name: 'Physical Playspace Walking', is_shadow: true, severity: 0.7, frame_index: 50 });
  return { match_id: 'synthetic-match', players, events };
}

test('zero-score shadow findings produce review-needed and a useful reason for any player', () => {
  const render = ui();
  const match = fixture();
  const a = render.playerReviewAssessment(match, match.players[1]);
  assert.equal(a.status, 'review_needed');
  assert.equal(a.signal_count, 5); // Not 5 * merged_count.
  assert.equal(a.scored_signals, 0);
  assert.equal(a.shadow_signals, 5);
  const html = render.reviewOverview(match);
  assert.match(html, /2 players with signals to review/);
  assert.match(html, /5 throw-speed signals/);
  assert.match(html, /review needed/);
  assert.match(html, /data-replay-event="throw-4"/);
  assert.match(html, /Review in Spark/);
  assert.doesNotMatch(html, /A spectator-independent/);
  const table = render.playersTable(match);
  assert.ok(table.indexOf('Z synthetic thrower') < table.indexOf('A spectator-independent'));
  assert.match(table, /Scoring total/);
  assert.doesNotMatch(table, />clean</);
});

test('canonical backend assessment is used for unfiltered views; filter scopes all findings and clips', () => {
  const render = ui();
  const match = fixture();
  const player = match.players[1];
  player.assessment = render.playerReviewAssessment(match, player);
  assert.equal(render.playerReviewAssessment(match, player), player.assessment);
  const html = render.reviewOverview(match, 'MOV_006');
  assert.match(html, /1 player with signals to review/);
  assert.match(html, /1 playspace-motion signal/);
  assert.match(html, /data-replay-event="move-1"/);
  assert.doesNotMatch(html, /throw-speed|Z synthetic thrower|throw-4/);
  const table = render.playersTable(match, 'MOV_006');
  assert.doesNotMatch(table, /Z synthetic thrower|A spectator-independent/);
  assert.match(table, /Scoring total \(overall\)/);
});

test('no signals does not claim verified fair play or create a review finding', () => {
  const render = ui();
  const match = fixture();
  match.events = [];
  const a = render.playerReviewAssessment(match, match.players[0]);
  assert.equal(a.status, 'no_signals');
  assert.equal(a.signal_count, 0);
  const html = render.reviewOverview(match);
  assert.match(html, /No detector signals found/);
  assert.match(html, /not a cheating verdict or proof of fair play/);
  assert.doesNotMatch(html, /Review in Spark|review needed/);
});

test('filtering a multi-detector player cannot reuse their unfiltered backend counts or clip', () => {
  const render = ui();
  const match = fixture();
  match.events[5].player_id = 'arbitrary-thrower';
  const player = match.players[1];
  player.assessment = render.playerReviewAssessment(match, player);
  assert.equal(player.assessment.signal_count, 6);
  const a = render.playerReviewAssessment(match, player, 'MOV_006');
  assert.equal(a.signal_count, 1);
  assert.equal(a.detectors.length, 1);
  assert.equal(a.detectors[0].detector_id, 'MOV_006');
  const html = render.reviewOverview(match, 'MOV_006') + render.playersTable(match, 'MOV_006');
  assert.match(html, /Z synthetic thrower/);
  assert.match(html, /1 playspace-motion signal/);
  assert.match(html, /data-replay-event="move-1"/);
  assert.doesNotMatch(html, /throw-speed|throw-4|6 automatic signals/);
});

test('blinded review conceals new conclusions, reasons, scores and signal-based ranking', () => {
  const render = ui();
  render.blindReview = true;
  const match = fixture();
  const html = render.reviewOverview(match) + render.playersTable(match);
  assert.match(html, /Automatic assessment concealed/);
  assert.doesNotMatch(html, /throw-speed|playspace-motion|review needed|5 signals|Scoring total/);
  assert.ok(html.indexOf('A spectator-independent') < html.indexOf('Z synthetic thrower'));
});

test('untrusted names and unknown detector labels are escaped', () => {
  const render = ui();
  const match = fixture();
  match.players[1].name = '<img src=x onerror=alert(1)>';
  match.events[0].detector_id = 'FUTURE_001';
  match.events[0].detector_name = '<script>alert(2)</script>';
  const html = render.reviewOverview(match) + render.playersTable(match);
  assert.match(html, /&lt;img/);
  assert.match(html, /&lt;script/);
  assert.doesNotMatch(html, /<img|<script/);
});

test('scored and shadow findings stay distinct without mutating source payloads', () => {
  const render = ui();
  const match = fixture();
  match.events[0].is_shadow = false;
  const before = JSON.stringify(match);
  const a = render.playerReviewAssessment(match, match.players[1]);
  assert.equal(a.scored_signals, 1);
  assert.equal(a.shadow_signals, 4);
  render.reviewOverview(match);
  render.playersTable(match, 'THROW_001');
  assert.equal(JSON.stringify(match), before);
});

test('missing coverage and unreachable detectors show insufficient data without hiding findings', () => {
  const render = ui(), match = fixture();
  delete match.players[0].coverage;
  assert.equal(render.playerReviewAssessment(match, match.players[0]).status, 'insufficient_data');
  match.players[2].coverage.detectors[1].status = 'insufficient_data';
  match.players[2].coverage.detectors[1].input_frames = 0;
  assert.equal(render.playerReviewAssessment(match, match.players[2], 'MOV_006').status, 'review_needed');
  match.events = [];
  assert.equal(render.playerReviewAssessment(match, match.players[2], 'MOV_006').status, 'insufficient_data');
  const html = render.reviewOverview(match, 'MOV_006') + render.playersTable(match, 'MOV_006');
  assert.match(html, /insufficient data|Insufficient data/);
  assert.match(html, /Coverage was not recorded/);
  assert.match(html, /M synthetic mover/);
  assert.doesNotMatch(html, /THROW_001|Review in Spark/);
});

test('coverage notes are escaped and do not leak into blinded overview', () => {
  const render = ui(), match = fixture();
  match.players[0].coverage.detectors[0].limitations = ['<img src=x onerror=alert(1)>'];
  assert.match(render.reviewOverview(match), /&lt;img/);
  assert.doesNotMatch(render.reviewOverview(match), /<img/);
  render.blindReview = true;
  assert.doesNotMatch(render.reviewOverview(match), /Candidate frames|&lt;img|THROW_001/);
});

test('detector filter cannot override whole-source insufficient data or quality gating', () => {
  const render = ui(), match = fixture(); match.events = [];
  const p = match.players[0];
  p.coverage.quality_gated = true;
  assert.equal(render.playerReviewAssessment(match, p, 'THROW_001').status, 'insufficient_data');
  p.coverage.quality_gated = false; p.coverage.status = 'insufficient_data';
  assert.equal(render.playerReviewAssessment(match, p, 'THROW_001').status, 'insufficient_data');
});

test('decision traces distinguish pipeline-only data and escape every reason', () => {
  const render = ui(), match = fixture(), p = match.players[0];
  p.coverage.detectors[0].decision_trace = { version: 1, internal_branches: true, reasons: [
    { code: '<img src=x>', description: '<script>bad()</script>', count: 4, first_frame: 7, last_frame: 23 }
  ], overflow_count: 2 };
  p.coverage.detectors[1].decision_trace = { version: 1, internal_branches: false, reasons: [] };
  const before = JSON.stringify(match);
  const html = render.decisionTraceDetails(match, p);
  assert.match(html, /&lt;img|&lt;script/);
  assert.doesNotMatch(html, /<img|<script/);
  assert.match(html, /pipeline only|Internal detector decisions are not instrumented/);
  assert.match(html, /data-physics-frame="7"/);
  assert.match(html, /exceeded the bounded/);
  assert.match(html, /Reasons can overlap/);
  assert.doesNotMatch(render.decisionTraceDetails(match, p, 'THROW_001'), /MOV_006/);
  assert.equal(JSON.stringify(match), before);
});

test('missing and blinded decision traces never expose conclusions or imply clearance', () => {
  const render = ui(), match = fixture(), p = match.players[0];
  assert.match(render.decisionTraceDetails(match, p), /No compatible trace/);
  delete p.coverage;
  assert.match(render.decisionTraceDetails(match, p), /missing traces are not a clean result/);
  render.blindReview = true;
  assert.match(render.decisionTraceDetails(match, p), /concealed/);
  assert.doesNotMatch(render.decisionTraceDetails(match, p), /THROW_001|MOV_006|unflagged/);
});

function blindUI() {
  const context = ui();
  const a = script.indexOf('  let blindSessions =');
  const b = script.indexOf('  async function openBlindWorkspace(', a);
  assert.ok(a >= 0 && b > a);
  vm.runInContext(script.slice(a,b), context);
  return context;
}

test('calibration details expose diversity and legal context without inventing missing error rates', () => {
  const render = blindUI();
  const a = script.indexOf('  function calibrationDetailsHTML(');
  const b = script.indexOf('  let blindSessions =', a);
  vm.runInContext(script.slice(a,b),render);
  const metric = { true_positive:2, false_positive:0, false_negative:0, true_negative:0, positive_opportunities:2, precision:1, recall:1, clusters:{ groups:1, positive_groups:1, negative_groups:0, largest_group_fraction:1, recall_observed_range:{lower:1,upper:1}, macro_recall:1 } };
  const d = { overall:metric, reasons:['<img src=x>'], by_legal_context:{slap:metric,'<script>':metric} };
  const before = JSON.stringify(d);
  const html = render.calibrationDetailsHTML(d);
  assert.match(html,/Connected-group diversity|Disc slap|not confidence intervals/);
  assert.match(html,/Reserved holdout \(not sealed\)/);
  assert.doesNotMatch(html,/<img|<script|undefined|NaN|Infinity/);
  assert.match(html,/&lt;img|&lt;script/);
  assert.match(html,/<td>—<\/td>/);
  assert.equal(JSON.stringify(d),before);
});

test('blind session rendering hides prematurely supplied ballots and findings', () => {
  const render = blindUI();
  const session = { session_id:'s', revealed:false, ballot_count:1, binding:{match_id:'m',player_id:'p',detector_id:'THROW_001',artifact_sha256:'a'.repeat(64),frame_start:1,frame_end:9}, ballots:[{reviewer_id:'SECRET-REVIEWER',ground_truth:'positive',comment:'SECRET-BALLOT'}], consensus:'positive' };
  const html = render.blindSessionDetails(session,[{detector_id:'SECRET-DETECTOR',observed_value:'SECRET-FINDING'}]);
  assert.doesNotMatch(html,/SECRET-|Locked review results|data-blind-reveal/);
  assert.match(html, /id="br-truth"><option value="uncertain"/);
  assert.match(html, /id="br-attest" type="checkbox">/);
  assert.match(html, /id="br-comment" maxlength="2000"/);
  assert.match(page, /pre_reveal_attestation:\$\('br-attest'\)\.checked/);
  assert.match(html, /data-blind-ballot="s"/);
  session.ballot_count = 2;
  const ready = render.blindSessionDetails(session);
  assert.doesNotMatch(ready,/SECRET-|data-blind-ballot/);
  assert.match(ready,/data-blind-reveal="s"/);
});

test('revealed review and attached evidence links are escaped and integrity constrained', () => {
  const render = blindUI();
  assert.doesNotMatch(render.blindArtifactLink('javascript:alert(1)'), /href=/);
  const session = { session_id:'<img>', revealed:true, ballot_count:2, consensus:'<script>bad()</script>', binding:{match_id:'<img>',player_id:'<img>',detector_id:'THROW_001',artifact_sha256:'a'.repeat(64),frame_start:1,frame_end:9}, ballots:[{reviewer_id:'<img>',ground_truth:'negative',comment:'<script>bad()</script>'}] };
  const html = render.blindSessionDetails(session,[{detector_id:'<img>',observed_value:'<img>',frame_index:2}]);
  assert.doesNotMatch(html,/<img|<script/);
  assert.match(html,/&lt;img|&lt;script/);
  assert.match(html,/api\/blind-review\/artifacts\/a{64}/);
  assert.doesNotMatch(html,/id="br-reviewer"/);
});

test('modal status messages are visible inside the active modal and never interpreted as HTML', () => {
  const nodes = { status:{}, 'lab-dialog':{open:true}, 'lab-dialog-status':{} };
  const context = vm.createContext({$: (id) => nodes[id]});
  const a = script.indexOf('  const setStatus ='), b = script.indexOf('  async function getJSON(',a);
  vm.runInContext(script.slice(a,b) + '\nsetStatus("<img src=x>", "err");',context);
  assert.equal(nodes['lab-dialog-status'].textContent,'<img src=x>');
  assert.equal(nodes['lab-dialog-status'].className,'status err');
  assert.equal(nodes['lab-dialog-status'].innerHTML,undefined);
});
