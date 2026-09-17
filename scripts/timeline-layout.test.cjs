'use strict';

// Unit tests for the match timeline's pure layout function. The block between
// TIMELINE-LAYOUT-BEGIN and TIMELINE-LAYOUT-END in cmd/desktop/index.html is
// sliced out and run in a bare vm context: if it ever reaches for the DOM or a
// page helper, every test here fails. Synthetic data only. These tests prove
// positions and grouping, not how the SVG looks.
const assert = require('node:assert/strict');
const fs = require('node:fs');
const path = require('node:path');
const vm = require('node:vm');
const { test } = require('node:test');

const page = fs.readFileSync(path.join(__dirname, '../cmd/desktop/index.html'), 'utf8');
const begin = page.indexOf('// TIMELINE-LAYOUT-BEGIN');
const end = page.indexOf('// TIMELINE-LAYOUT-END');
assert.ok(begin > 0 && end > begin, 'the delimited timeline layout block exists');
assert.equal(page.indexOf('// TIMELINE-LAYOUT-BEGIN', begin + 1), -1, 'the block is delimited once');
const context = vm.createContext({});
vm.runInContext(page.slice(begin, end) + '\nthis.api = { timelineLayout, timelineNeighbor, tlClock, tlAxisStep, TL };', context);
const { timelineLayout, timelineNeighbor, tlClock, TL } = context.api;
const plain = (value) => JSON.parse(JSON.stringify(value));

const roster = (n) => Array.from({ length: n }, (_, i) => ({ player_id: `p${i + 1}`, name: `Synthetic ${i + 1}`, team: i % 2 ? 'orange' : 'blue', first_seen: 0, last_seen: 1200 }));
const throwAt = (time, player, speed, extra = {}) => ({ time, clock: '00:00', player_id: player, player, team: 'blue', speed, goal: false, frame_index: Math.round(time * 30), ...extra });
const signalAt = (timestamp, player, severity_label, is_shadow, severity = 0.5) => ({ event_id: `e-${timestamp}-${severity_label}-${is_shadow}`, detector_id: 'SYN_001', player_id: player, player_name: player, timestamp, severity, severity_label, is_shadow, frame_index: 0 });
let seed = 11;
const random = () => (seed = (seed * 1103515245 + 12345) % 2147483648) / 2147483648;

// Every coordinate that can reach the SVG must be a finite number.
function assertFinite(value, where = 'layout') {
  if (typeof value === 'number') { if (where.endsWith('.t') && value === Infinity) return; assert.ok(Number.isFinite(value), `${where} is ${value}`); return; }
  if (Array.isArray(value)) { value.forEach((item, i) => assertFinite(item, `${where}[${i}]`)); return; }
  if (value && typeof value === 'object') for (const [key, item] of Object.entries(value)) assertFinite(item, `${where}.${key}`);
}

test('match time maps linearly onto the plot and the axis is labelled m:ss', () => {
  const L = timelineLayout({ duration: 1200, cap: 18.9, players: roster(2), throws: [throwAt(0, 'p1', 10), throwAt(600, 'p1', 10), throwAt(1200, 'p1', 10)] }, 1157);
  const xs = L.marks.filter((mark) => mark.kind === 'throw').map((mark) => mark.x);
  assert.deepEqual(plain(xs), [L.x0, (L.x0 + L.x1) / 2, L.x1]);
  assert.ok(L.width <= 1157, 'the drawing fits the container it was given');
  assert.equal(L.step, 120);
  assert.deepEqual(plain(L.ticks.map((tick) => tick.label)), ['0:00', '2:00', '4:00', '6:00', '8:00', '10:00', '12:00', '14:00', '16:00', '18:00', '20:00']);
  for (let i = 1; i < L.ticks.length; i++) assert.ok(L.ticks[i].x - L.ticks[i - 1].x >= TL.tickGap, 'axis labels keep their distance');
  assert.equal(tlClock(59.6), '1:00');
  assert.equal(tlClock(NaN), '0:00');
});

test('lanes are grouped by team in roster order and an unrostered thrower still gets a lane', () => {
  const players = [{ player_id: 'o1', name: 'O1', team: 'orange' }, { player_id: 'b1', name: 'B1', team: 'blue' }, { player_id: 's1', name: 'S1', team: 'spectator' }, { player_id: 'b2', name: 'B2', team: 'blue' }];
  const L = timelineLayout({ duration: 60, players, throws: [throwAt(5, 'ghost', 9, { player: 'Ghost', team: 'orange' })] }, 1200);
  assert.deepEqual(plain(L.lanes.map((lane) => `${lane.team}:${lane.id}`)), ['blue:b1', 'blue:b2', 'orange:o1', 'orange:ghost', 'none:s1']);
  assert.deepEqual(plain(L.groups.map((group) => [group.team, group.players])), [['blue', 2], ['orange', 2], ['none', 1]]);
  for (let i = 1; i < L.lanes.length; i++) assert.ok(L.lanes[i].y >= L.lanes[i - 1].y + L.lanes[i - 1].h, 'lanes never overlap');
  const ghost = L.marks.find((mark) => mark.kind === 'throw');
  assert.equal(L.lanes[ghost.lane].id, 'ghost');
  assert.equal(ghost.y, L.lanes[ghost.lane].baseY);
});

test('no goals, no throws, null arrays and a single player still lay out', () => {
  for (const data of [{ duration: 95, players: roster(1), goals: null, throws: null, events: null, cases: null }, { duration: 95, players: roster(1) }, {}, null]) {
    const L = timelineLayout(data, 1157);
    assert.equal(L.marks.length, 0);
    assertFinite(plain(L));
    assert.ok(L.height > 0 && L.width > 0);
  }
  const solo = timelineLayout({ duration: 95, players: roster(1) }, 1157);
  assert.equal(solo.lanes.length, 1);
  assert.equal(solo.hasTime, true);
  assert.equal(timelineLayout({}, 1157).hasTime, false);
});

test('twelve or more players shrink the lanes and the lane count is bounded', () => {
  const few = timelineLayout({ duration: 300, players: roster(4) }, 1157), many = timelineLayout({ duration: 300, players: roster(16) }, 1157);
  assert.ok(many.laneHeight < few.laneHeight);
  assert.ok(many.tickZone >= 10, 'throw ticks keep a readable height');
  assert.ok(many.height < 640, `16 players stay one screen tall (got ${many.height})`);
  const flood = timelineLayout({ duration: 300, players: roster(500) }, 1157);
  assert.equal(flood.lanes.length, TL.maxLanes);
  assert.equal(flood.lanesDropped, 500 - TL.maxLanes);
});

test('a 20 minute match with 400 throws is clustered without losing or hiding a throw', () => {
  const players = roster(10), throws = [];
  for (let i = 0; i < 400; i++) throws.push(throwAt(random() * 1200, `p${1 + Math.floor(random() * 10)}`, i % 50 === 7 ? 19.4 + random() : 5 + random() * 13));
  throws.sort((a, b) => a.time - b.time);
  const started = process.hrtime.bigint();
  const L = timelineLayout({ duration: 1200, cap: 18.9, players, throws }, 1157);
  const elapsed = Number(process.hrtime.bigint() - started) / 1e6;
  assert.ok(elapsed < 250, `layout took ${elapsed.toFixed(1)} ms`);
  const marks = L.marks.filter((mark) => mark.kind === 'throw');
  assert.ok(marks.length < 400 && marks.length > 200, `clustered to ${marks.length} marks`);
  assert.deepEqual(plain(marks.flatMap((mark) => mark.refs)).sort((a, b) => a - b), throws.map((_, i) => i), 'every throw belongs to exactly one mark');
  for (const lane of L.lanes.keys()) {
    const xs = marks.filter((mark) => mark.lane === lane).sort((a, b) => a.t - b.t);
    for (let i = 1; i < xs.length; i++) assert.ok(xs[i].t >= xs[i - 1].t1, 'clusters do not interleave');
  }
  const over = throws.map((th, i) => (th.speed > 18.9 ? i : -1)).filter((i) => i >= 0);
  assert.equal(over.length, 8);
  for (const i of over) {
    const mark = marks.find((candidate) => candidate.refs.includes(i));
    assert.equal(mark.over, true, 'an over-cap release is never hidden inside a legal-looking cluster');
    assert.ok(throws[mark.primary].speed > 18.9, 'the row it opens is an over-cap release');
  }
  assert.equal(marks.filter((mark) => mark.over).length <= over.length, true);
  assertFinite(plain(L));
});

test('throw height and shade follow release speed against the cap; over the cap is distinct', () => {
  const speeds = [6, 12, 18.9, 19.2, 40];
  const L = timelineLayout({ duration: 100, cap: 18.9, players: roster(1), throws: speeds.map((speed, i) => throwAt(10 + i * 20, 'p1', speed)) }, 1157);
  const [slow, mid, atCap, over, absurd] = L.marks.filter((mark) => mark.kind === 'throw');
  assert.ok(slow.h < mid.h && mid.h < atCap.h, 'faster is taller');
  assert.ok(slow.shade < mid.shade && mid.shade < atCap.shade && atCap.shade <= 1, 'faster is stronger');
  assert.equal(atCap.h, L.tickZone, 'a release at the cap reaches the cap rule');
  assert.equal(atCap.over, false, 'exactly at the cap is not over it');
  assert.ok(over.over && absurd.over && over.h > L.tickZone && absurd.h === over.h, 'over-cap overshoots the rule by a fixed amount');
  const uncapped = timelineLayout({ duration: 100, cap: 0, players: roster(1), throws: [throwAt(10, 'p1', 10), throwAt(50, 'p1', 30)] }, 1157);
  assert.equal(uncapped.marks.some((mark) => mark.over), false, 'without a cap nothing is called over it');
  assert.equal(uncapped.marks[1].h, uncapped.tickZone, 'and height is relative to the fastest release');
});

test('a signal cluster shows what the detections table ranks first; blinded review leaks nothing', () => {
  const events = [signalAt(100, 'p1', 'critical', true, 0.9), signalAt(100.2, 'p1', 'medium', false, 0.4), signalAt(100.4, 'p1', 'high', false, 0.7), signalAt(700, 'p1', 'low', true, 0.1)];
  const data = { duration: 1200, cap: 18.9, players: roster(2), events, cases: [{ case_id: 'c1', player_id: 'p1', level: 'high_risk' }] };
  const L = timelineLayout(data, 1157);
  const signals = L.marks.filter((mark) => mark.kind === 'signal');
  assert.equal(signals.length, 2);
  assert.deepEqual(plain([signals[0].count, signals[0].primary, signals[0].sev, signals[0].shadow]), [3, 2, 'high', false], 'scoring beats observation-only, then severity');
  assert.deepEqual(plain([signals[1].count, signals[1].sev, signals[1].shadow]), [1, 'low', true]);
  assert.equal(signals[0].y, L.lanes[0].signalY);
  assert.ok(L.lanes[0].signalY > L.lanes[0].baseY, 'signals sit below the throw baseline');
  const caseMark = L.marks.at(-1);
  assert.deepEqual(plain([caseMark.kind, caseMark.level, caseMark.caseID]), ['case', 'high_risk', 'c1']);
  assert.ok(L.width <= 1157);

  const blind = timelineLayout({ ...data, blind: true }, 1157);
  const hidden = blind.marks.filter((mark) => mark.kind === 'signal');
  assert.deepEqual(plain(hidden.map((mark) => [mark.concealed, mark.sev, mark.shadow, mark.primary])), [[true, '', true, 0], [true, '', true, 3]]);
  assert.equal(blind.marks.some((mark) => mark.kind === 'case'), false, 'case levels are concealed too');
  assert.doesNotMatch(JSON.stringify(hidden), /critical|high|medium/);
});

test('untrusted times never become coordinates', () => {
  const bad = [NaN, -5, Infinity, '12', null, undefined, 1e12];
  const L = timelineLayout({
    duration: 120, cap: 18.9, players: roster(2),
    goals: bad.map((time) => ({ time, team: 'blue', blue_score: 1, orange_score: 0 })),
    throws: bad.map((time) => throwAt(time, 'p1', 10)).concat([throwAt(30, 'p1', NaN), null]),
    events: bad.map((timestamp) => signalAt(timestamp, 'p2', 'low', true)).concat([undefined]),
  }, 1157);
  assert.equal(L.duration, 120, 'a wild timestamp does not stretch the axis');
  assert.equal(L.marks.length, 1);
  assert.equal(L.marks[0].t, 30);
  assert.equal(L.undated, bad.length * 3 + 2);
  assertFinite(plain(L));
  const late = timelineLayout({ duration: 120, players: roster(1), throws: [throwAt(150, 'p1', 10)] }, 1157);
  assert.equal(late.duration, 150, 'a mark shortly after the stated end stretches the axis instead of vanishing');
  assert.equal(late.marks[0].x, late.x1);
});

test('close goals keep their marks and only drop a colliding score label', () => {
  const goal = (time, blue_score, orange_score, team) => ({ time, team, blue_score, orange_score, points: 2 });
  const L = timelineLayout({ duration: 1200, players: roster(2), goals: [goal(300, 2, 0, 'blue'), goal(305, 2, 2, 'orange'), goal(900, 4, 2, 'blue')] }, 1157);
  const goals = L.marks.filter((mark) => mark.kind === 'goal');
  assert.deepEqual(plain(goals.map((mark) => [mark.lane, mark.team, mark.blue, mark.orange, mark.showLabel])), [[-1, 'blue', 2, 0, true], [-1, 'orange', 2, 2, false], [-1, 'blue', 4, 2, true]]);
  assert.ok(goals.every((mark) => mark.y === L.scoreY && mark.y < L.lanesTop), 'goals live in the score lane above the players');
});

test('marks are indexed in match-time order and the keyboard walks them', () => {
  const L = timelineLayout({
    duration: 600, cap: 18.9, players: roster(3),
    goals: [{ time: 200, team: 'blue', blue_score: 2, orange_score: 0 }],
    throws: [throwAt(400, 'p3', 10), throwAt(100, 'p1', 10), throwAt(198, 'p1', 12, { goal: true })],
    events: [signalAt(100, 'p2', 'low', true), signalAt(500, 'p1', 'high', false)],
    cases: [{ case_id: 'c9', player_id: 'p2', level: 'suspicious' }],
  }, 1157);
  assert.deepEqual(plain(L.marks.map((mark) => mark.i)), [0, 1, 2, 3, 4, 5, 6]);
  assert.deepEqual(plain(L.marks.map((mark) => mark.kind)), ['throw', 'signal', 'throw', 'goal', 'throw', 'signal', 'case']);
  const timed = L.marks.filter((mark) => mark.kind !== 'case').map((mark) => mark.t);
  assert.deepEqual(timed, timed.slice().sort((a, b) => a - b));
  assert.equal(L.marks[2].goal, true);
  assert.equal(timelineNeighbor(L, 0, 'ArrowRight'), 1);
  assert.equal(timelineNeighbor(L, 0, 'ArrowLeft'), null);
  assert.equal(timelineNeighbor(L, 6, 'ArrowRight'), null);
  assert.equal(timelineNeighbor(L, 3, 'Home'), 0);
  assert.equal(timelineNeighbor(L, 3, 'End'), 6);
  assert.equal(timelineNeighbor(L, 3, 'ArrowDown'), 2, 'down from the goal lands on the nearest mark of the first lane');
  assert.equal(timelineNeighbor(L, 2, 'ArrowUp'), 3, 'up from the first lane reaches the score lane');
  assert.equal(timelineNeighbor(L, 3, 'ArrowUp'), null);
  assert.equal(timelineNeighbor(L, 0, 'Tab'), null);
  assert.equal(timelineNeighbor(L, 99, 'ArrowRight'), null);
});

test('a narrow container keeps a usable plot and lets its own box scroll', () => {
  const L = timelineLayout({ duration: 300, players: roster(2) }, 300);
  assert.equal(L.x1 - L.x0, TL.minPlot);
  assert.ok(L.width > 300);
  assert.equal(L.label, TL.labelNarrow);
  assert.equal(timelineLayout({ duration: 300, players: roster(2) }, 1370).label, TL.label);
});

// The renderer is not pure (it uses the page's formatters), but what it writes
// into the document must be inert whatever an untrusted recording calls a player.
test('the rendered SVG escapes recording-supplied text and exposes every mark to the keyboard', () => {
  const from = page.indexOf('// TIMELINE-LAYOUT-BEGIN'), to = page.indexOf('  function timelineLegend(');
  assert.ok(to > from);
  const escapeHTML = (s) => String(s ?? '').replace(/[&<>"']/g, (c) => ({ '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' }[c]));
  const render = vm.createContext({ esc: escapeHTML, fmtInt: (n) => (typeof n === 'number' ? String(n) : '—'), fmtNum: (n, d = 1) => (typeof n === 'number' ? n.toFixed(d) : '—'), lvlName: (l) => String(l || '').replace(/_/g, ' ') });
  vm.runInContext(page.slice(from, to) + '\nthis.api = { timelineLayout, timelineSVG };', render);
  const hostile = '"><img src=x onerror=alert(1)>';
  const m = {
    summary: { goals: [{ time: 20, team: 'blue', scorer: hostile, blue_score: 2, orange_score: 0, points: 2 }], throws: [throwAt(10, hostile, 19.5, { player: hostile }), throwAt(40, hostile, 9, { player: hostile })] },
    events: [{ ...signalAt(30, hostile, 'high', false), detector_id: hostile, detector_name: hostile, player_name: hostile }],
    cases: [{ case_id: hostile, player_id: hostile, player_name: hostile, level: hostile }],
  };
  const L = render.api.timelineLayout({ duration: 60, cap: 18.9, players: [{ player_id: hostile, name: hostile, team: hostile }], goals: m.summary.goals, throws: m.summary.throws, events: m.events, cases: m.cases }, 1157);
  const svg = render.api.timelineSVG(m, L);
  assert.doesNotMatch(svg, /<img|onerror=alert\(1\)>/, 'hostile text never becomes markup');
  assert.match(svg, /&lt;img src=x onerror=alert\(1\)&gt;/);
  const groups = svg.match(/<g class="tl-mark[^>]*>/g) || [];
  assert.equal(groups.length, L.marks.length);
  assert.equal(L.marks.length, 5);
  for (const tag of groups) assert.match(tag, /data-tl-mark="\d+" tabindex="(0|-1)" role="button" aria-label="[^"]+"/);
  assert.equal(groups.filter((tag) => tag.includes('tabindex="0"')).length, 1, 'one tab stop; arrow keys reach the rest');
  assert.match(groups.find((tag) => tag.includes('tl-over')), /over the 18\.9 metres per second engine cap, marked for review/);
  assert.equal(groups.filter((tag) => tag.includes('tl-over')).length, 1, 'only the over-cap release is drawn as over the cap');
});
