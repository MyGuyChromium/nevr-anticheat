'use strict';

const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');
const page = fs.readFileSync(path.join(__dirname, '../cmd/desktop/index.html'), 'utf8');
const script = page.match(/<script>([\s\S]*?)<\/script>/)[1];
new vm.Script(script);
const start = script.indexOf('  function renderCatchTrajectory(');
const end = script.indexOf('  function renderPhysics(', start);
assert.ok(start >= 0 && end > start);
function render(evidence, blind = false) {
  const context = vm.createContext({ blindReview: blind });
  vm.runInContext(script.slice(start, end), context);
  return context.renderCatchTrajectory(evidence);
}
function evidence() {
  return { catch_trajectory: Array.from({ length: 5 }, (_, i) => ({
    frame_index: 100 + i, timestamp: 10 + i / 30,
    disc_position: [i, i * .1, 1], expected_position: [i, 0, 1],
    left_hand: [6, 1, 1], right_hand: [6, 2, 1],
  })) };
}
test('catch review renders three bounded projections with reference and attribution limits', () => {
  const html = render(evidence());
  assert.equal((html.match(/<svg /g) || []).length, 3);
  assert.equal((html.match(/<polyline /g) || []).length, 12);
  assert.equal((html.match(/hand at last free sample/g) || []).length, 6);
  for (const phrase of ['not proof', 'never adds suspicion score', 'constant velocity', 'attachment is excluded', 'independently', 'Left hand', 'Right hand', 'frames 100–104']) assert.ok(html.includes(phrase), phrase);
  assert.doesNotMatch(html, /NaN|Infinity|undefined/);
  assert.match(page, /p\.event\.detector_id === 'STATE_008' \? renderCatchTrajectory/);
});
test('blinded review never renders trajectory evidence', () => {
  assert.equal(render(evidence(), true), '');
});
test('malformed, unbounded and nonmonotonic evidence cannot create SVG', () => {
  for (const bad of [null, {}, { catch_trajectory: [] }, { catch_trajectory: Array(257).fill(evidence().catch_trajectory[0]) }]) assert.equal(render(bad), '');
  const mutations = [
    (s) => { s.disc_position = ['0\" onload=\"alert(1)', 0, 1]; },
    (s) => { s.left_hand = [Infinity, 0, 1]; },
    (s) => { s.right_hand = [NaN, 0, 1]; },
    (s) => { s.expected_position = [1e99, 0, 1]; },
    (s) => { s.expected_position = [1, 2]; },
    (s) => { s.frame_index = 99; },
    (s) => { s.timestamp = 1; },
    (s) => { s.frame_index = '102'; },
  ];
  for (const mutate of mutations) {
    const e = evidence(); mutate(e.catch_trajectory[2]);
    assert.equal(render(e), '');
  }
});
test('zero-range projection stays finite and text payloads never enter SVG', () => {
  const e = evidence();
  for (const sample of e.catch_trajectory) for (const field of ['disc_position', 'expected_position', 'left_hand', 'right_hand']) sample[field] = [1, 1, 1];
  e.attribution = '<img src=x onerror=alert(1)>';
  const html = render(e);
  assert.match(html, /0.50 m span/);
  assert.doesNotMatch(html, /NaN|Infinity|onerror|<img/);
});

test('high-rate trajectory evidence up to the bounded history size renders', () => {
  const e = { catch_trajectory: Array.from({ length: 256 }, (_, i) => ({
    ...evidence().catch_trajectory[0], frame_index: i, timestamp: i / 120,
  })) };
  assert.match(render(e), /256 free-disc samples/);
  e.catch_trajectory.push({ ...e.catch_trajectory[255], frame_index: 256, timestamp: 256 / 120 });
  assert.equal(render(e), '');
});

function catchLog(log, blind = false, detector = 'STATE_008') {
  const escape = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({'&':'&amp;','<':'&lt;','>':'&gt;','"':'&quot;',"'":'&#39;'}[c]));
  const context = vm.createContext({ blindReview: blind, esc: escape });
  const begin = script.indexOf('  function catchReviewDetails(');
  const finish = script.indexOf('  function decisionTraceDetails(', begin);
  assert.ok(begin >= 0 && finish > begin);
  vm.runInContext(script.slice(begin, finish), context);
  return context.catchReviewDetails({ match_id: 'm"<img>' }, { player_id: 'p"<img>' }, { detector_id: detector, catch_review: log });
}
function reviewLog() {
  return { version: 1, total: 4, dropped: 0, invalid: 0, observation: 1, excluded: 1, insufficient_data: 1, unconfirmed: 1,
    records: ['observation','excluded','insufficient_data','unconfirmed'].map((outcome, i) => ({
      frame_index: 20 + i * 20, timestamp: 1 + i, confirmed: i < 3, outcome,
      reason: 'catch_possible_contact', reason_description: 'Possible contact prevented comparison',
      metrics: { correction_duration_s: .2, contact_uncertainty_m: .01 },
    })) };
}
test('catch diagnostics expose decisions, uncertainty and exact-frame Spark/physics actions', () => {
  const html = catchLog(reviewLog());
  for (const text of ['4 finalized transitions', 'not every catch', 'not mean proven fair play', 'Possession unconfirmed', 'contact_uncertainty_m', 'Open catch in Spark', 'data-replay-frame="60"', 'data-physics-frame="80"']) assert.ok(html.includes(text), text);
  assert.equal((html.match(/data-replay-frame=/g) || []).length, 4);
  assert.doesNotMatch(html, /<img>|data-replay-event/);
});
test('catch logs preserve blind review and distinguish absent versus empty diagnostics', () => {
  assert.equal(catchLog(reviewLog(), true), '');
  assert.equal(catchLog(reviewLog(), false, 'THROW_001'), '');
  assert.match(catchLog(null), /diagnostics are unavailable/);
  assert.match(catchLog({ version: 0 }), /diagnostics are unavailable/);
  assert.match(catchLog({ version: 1, records: [], total: 0 }), /No explicit catch transition was finalized/);
});
test('catch log limits and malformed values cannot inject markup or unsafe frame actions', () => {
  const log = reviewLog();
  log.total = 300; log.dropped = 172; log.invalid = 1;
  log.records = Array.from({length:300}, (_,i)=>({...log.records[0], frame_index:i}));
  log.records[0].frame_index = '1" onclick="bad()';
  log.records[0].reason_description = '<img src=x onerror=alert(1)>';
  log.records[0].reason = '<script>bad()</script>';
  log.records[0].timestamp = Infinity;
  log.records[0].metrics = { '<img>': 1, infinity: Infinity, nan: NaN, string: '<script>' };
  const html = catchLog(log);
  assert.equal((html.match(/data-replay-frame=/g) || []).length, 127);
  assert.match(html, /172 additional transition records omitted/);
  assert.match(html, /1 malformed diagnostic payloads were rejected/);
  assert.match(html, /&lt;img/);
  assert.doesNotMatch(html, /<script>|<img|onclick=|Infinity|NaN/);
});
