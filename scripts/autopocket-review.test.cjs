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
  for (const bad of [null, {}, { catch_trajectory: [] }, { catch_trajectory: Array(33).fill(evidence().catch_trajectory[0]) }]) assert.equal(render(bad), '');
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
