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
