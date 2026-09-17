'use strict';
// Rendered-UI layout check for the desktop page (cmd/desktop/index.html).
//
// The node --test suites slice index.html with regex/vm and never lay it out,
// so a CSS rule that collapses a table column passes all of them. This script
// renders the real page in a locally installed Edge/Chrome through the Chrome
// DevTools Protocol (Node built-ins only: no npm install, no puppeteer) and
// asserts layout METRICS, not pixels:
//   - the document never overflows horizontally,
//   - no visible player (.who) cell is narrower than 160 px,
//   - no visible report-table row is taller than its per-table budget,
//   - the first column of the wide report tables stays pinned while scrolling,
//   - the physics inspector's per-frame rows stay compact,
//   - with the "Larger" text size the match timeline's player names still end
//     before the plot begins and the page still does not overflow,
//   - the top navigation follows the section being read.
// It runs at 1280 and 1440 px wide, in the light and the dark theme, plus one
// 1024 px pass where the wide tables must scroll inside their container, which
// is what exercises the pinned first column.
//
// Run from the repository root (needs Go + a C compiler for the fixture,
// Node >= 22 for the built-in WebSocket, and Edge or Chrome):
//
//   node scripts/ui-render-check.cjs
//
// By default it starts the opt-in synthetic fixture itself
// (NEVR_UI_FIXTURE=1 go test ./cmd/desktop -run ^TestOperatorUIFixture$),
// which serves the embedded page on 127.0.0.1:19015 over a seeded synthetic
// database, and stops it again through the fixture control port 19016.
//
// Options:
//   --url <url>        check an already running page instead of starting the
//                      fixture (the fixture is then neither started nor stopped)
//   --shots <dir>      also write PNG screenshots into <dir>
//   --label <name>     screenshot file prefix (default "check")
//   --browser <path>   browser executable (or set NEVR_UI_BROWSER)
//   --upload <file>    with --url: analyze this recording through the page's own
//                      upload control in every pass instead of opening a stored
//                      match (slow; for a private local look at real data - keep
//                      such screenshots and output out of the repository)
//   --report-only      print violations but exit 0 (for before/after captures)
//
// Exit code: 0 = all metrics within budget, 1 = violation, 2 = could not run.
// Synthetic fixture data only; this proves layout, not detector accuracy.

const { spawn } = require('node:child_process');
const fs = require('node:fs');
const os = require('node:os');
const path = require('node:path');

const FIXTURE_URL = 'http://127.0.0.1:19015/0123456789abcdef0123456789abcdef/';
const FIXTURE_CONTROL = 'http://127.0.0.1:19016';
// [width, theme, enforce row budgets]. Row budgets describe the supported desktop
// widths; the narrow pass checks overflow, player cells and the pinned column only.
const PASSES = [[1280, 'dark', true], [1280, 'light', true], [1440, 'dark', true], [1440, 'light', true], [1024, 'dark', false]];
const HEIGHT = 900;
const MIN_WHO_CELL = 160;
// Row budgets in CSS px. A row holds at most a two-line player cell, a short
// explanation and a stack of small buttons; the defect this guards against
// produced ~440 px rows. A detection is one <tbody class="incident"> (a facts
// row plus an evidence row) and is measured as a whole.
const ROW_BUDGETS = [
  ['.stat-tables', 96],
  ['.assessment-players', 170],
  ['.events', 240],
  ['.throw-table', 130],
  ['#history', 170],
  ['#flagged', 200],
  ['#observations', 200],
];
const PHYSICS_ROW_BUDGET = 120;

const args = process.argv.slice(2);
const option = (name) => { const i = args.indexOf(name); return i >= 0 ? args[i + 1] : undefined; };
const flag = (name) => args.includes(name);
const sleep = (ms) => new Promise((resolve) => setTimeout(resolve, ms));
const repoRoot = path.resolve(__dirname, '..');

function findBrowser() {
  const explicit = option('--browser') || process.env.NEVR_UI_BROWSER;
  if (explicit) return explicit;
  const roots = [process.env['PROGRAMFILES(X86)'], process.env.PROGRAMFILES, process.env.LOCALAPPDATA].filter(Boolean);
  const candidates = [];
  for (const root of roots) {
    candidates.push(path.join(root, 'Microsoft', 'Edge', 'Application', 'msedge.exe'));
    candidates.push(path.join(root, 'Google', 'Chrome', 'Application', 'chrome.exe'));
  }
  candidates.push('/usr/bin/google-chrome', '/usr/bin/chromium', '/usr/bin/chromium-browser', '/usr/bin/microsoft-edge',
    '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome', '/Applications/Microsoft Edge.app/Contents/MacOS/Microsoft Edge');
  return candidates.find((candidate) => fs.existsSync(candidate));
}

async function reachable(url) {
  try { const r = await fetch(url, { signal: AbortSignal.timeout(2000) }); return r.ok; } catch (_) { return false; }
}

async function startFixture() {
  if (await reachable(FIXTURE_URL + 'api/status')) {
    throw new Error('port 19015 already serves a fixture. Stop it, or pass --url ' + FIXTURE_URL + ' to check it as it is.');
  }
  const child = spawn('go', ['test', './cmd/desktop', '-run', '^TestOperatorUIFixture$', '-count=1', '-v', '-timeout=30m'],
    { cwd: repoRoot, env: { ...process.env, NEVR_UI_FIXTURE: '1' }, stdio: ['ignore', 'pipe', 'pipe'] });
  let output = '', exited = false;
  child.stdout.on('data', (chunk) => { output += chunk; });
  child.stderr.on('data', (chunk) => { output += chunk; });
  child.on('exit', () => { exited = true; });
  const deadline = Date.now() + 10 * 60 * 1000; // first run compiles the CGO SQLite driver
  while (Date.now() < deadline) {
    if (exited) throw new Error('the UI fixture exited before it was ready:\n' + output.slice(-2000));
    if (await reachable(FIXTURE_URL + 'api/status')) return child;
    await sleep(1000);
  }
  child.kill();
  throw new Error('the UI fixture did not become ready within 10 minutes');
}

async function stopFixture(child) {
  try { await fetch(FIXTURE_CONTROL + '/shutdown', { method: 'POST', headers: { 'X-NEVR-Fixture-Control': 'test-only' }, signal: AbortSignal.timeout(5000) }); } catch (_) {}
  for (let i = 0; i < 40 && child.exitCode === null; i++) await sleep(250);
  if (child.exitCode === null) child.kill();
}

async function launchBrowser(executable, profile) {
  const child = spawn(executable, ['--headless=new', '--disable-gpu', '--no-first-run', '--no-default-browser-check', '--hide-scrollbars',
    '--remote-debugging-port=0', `--user-data-dir=${profile}`, `--window-size=${PASSES[0][0]},${HEIGHT}`, 'about:blank'], { stdio: 'ignore' });
  const portFile = path.join(profile, 'DevToolsActivePort');
  for (let i = 0; i < 120; i++) {
    if (fs.existsSync(portFile)) {
      const port = Number(fs.readFileSync(portFile, 'utf8').split('\n')[0]);
      if (port > 0) {
        try {
          const targets = await (await fetch(`http://127.0.0.1:${port}/json`)).json();
          const page = targets.find((target) => target.type === 'page');
          if (page) return { child, socketURL: page.webSocketDebuggerUrl };
        } catch (_) {}
      }
    }
    await sleep(250);
  }
  child.kill();
  throw new Error('the browser did not expose a DevTools page within 30 s');
}

async function connect(socketURL) {
  if (typeof WebSocket === 'undefined') throw new Error('this script needs Node >= 22 (built-in WebSocket)');
  const socket = new WebSocket(socketURL);
  await new Promise((resolve, reject) => { socket.addEventListener('open', resolve); socket.addEventListener('error', () => reject(new Error('DevTools socket failed'))); });
  let next = 0;
  const pending = new Map(), pageErrors = [];
  socket.addEventListener('message', (event) => {
    const message = JSON.parse(event.data);
    if (message.id && pending.has(message.id)) { pending.get(message.id)(message); pending.delete(message.id); }
    else if (message.method === 'Runtime.exceptionThrown') pageErrors.push(message.params.exceptionDetails?.exception?.description || message.params.exceptionDetails?.text || 'exception');
  });
  const send = (method, params = {}) => new Promise((resolve) => { const id = ++next; pending.set(id, resolve); socket.send(JSON.stringify({ id, method, params })); });
  const evaluate = async (expression) => {
    const reply = await send('Runtime.evaluate', { expression, awaitPromise: true, returnByValue: true });
    if (reply.result?.exceptionDetails) throw new Error('page script failed: ' + (reply.result.exceptionDetails.exception?.description || reply.result.exceptionDetails.text));
    return reply.result?.result?.value;
  };
  return { send, evaluate, pageErrors, close: () => socket.close() };
}

// Runs inside the page. Returns plain data only.
const MEASURE = `(() => {
  const budgets = ${JSON.stringify(ROW_BUDGETS)};
  const visible = (el) => { const r = el.getBoundingClientRect(); return r.width > 0 && r.height > 0; };
  const label = (el) => (el.textContent || '').replace(/\\s+/g, ' ').trim().slice(0, 48);
  const out = { overflow: document.documentElement.scrollWidth - window.innerWidth, who: [], rows: [], counts: {} };
  let whoCells = 0;
  for (const who of document.querySelectorAll('main td .who')) {
    const cell = who.closest('td');
    if (!cell || !visible(cell)) continue;
    whoCells++;
    const width = Math.round(cell.getBoundingClientRect().width);
    if (width < ${MIN_WHO_CELL}) out.who.push({ width, text: label(who) });
  }
  out.counts.whoCells = whoCells;
  for (const [selector, budget] of budgets) {
    let checked = 0, tallest = 0, scrollsBy = 0;
    for (const container of document.querySelectorAll('main ' + selector)) {
      if (visible(container)) scrollsBy = Math.max(scrollsBy, container.scrollWidth - container.clientWidth);
      const incidents = container.querySelectorAll('tbody.incident');
      for (const row of incidents.length ? incidents : container.querySelectorAll('tbody > tr')) {
        if (!visible(row)) continue;
        checked++;
        const height = Math.round(row.getBoundingClientRect().height);
        tallest = Math.max(tallest, height);
        if (height > budget) out.rows.push({ selector, height, budget, text: label(row) });
      }
    }
    out.counts[selector] = { rows: checked, tallest, scrollsBy };
  }
  return out;
})()`;

const STICKY = `(async () => {
  const results = [];
  for (const selector of ['.stat-tables', '.events']) {
    const box = [...document.querySelectorAll('main ' + selector)].find((el) => el.getBoundingClientRect().width > 0 && el.scrollWidth > el.clientWidth + 4);
    if (!box) { results.push({ selector, scrollable: false }); continue; }
    const cell = box.querySelector('tbody tr td:first-child');
    box.scrollLeft = 0;
    const before = cell.getBoundingClientRect().left;
    box.scrollLeft = box.scrollWidth;
    await new Promise((resolve) => requestAnimationFrame(() => requestAnimationFrame(resolve)));
    const after = cell.getBoundingClientRect().left;
    const moved = box.scrollLeft;
    box.scrollLeft = 0;
    results.push({ selector, scrollable: true, moved, drift: Math.round(Math.abs(after - before)) });
  }
  return results;
})()`;

async function main() {
  const browserPath = findBrowser();
  if (!browserPath) throw new Error('no Edge or Chrome found; pass --browser <path> or set NEVR_UI_BROWSER');
  const externalURL = option('--url');
  const shotsDir = option('--shots');
  const shotLabel = option('--label') || 'check';
  if (shotsDir) fs.mkdirSync(shotsDir, { recursive: true });
  const baseURL = externalURL || FIXTURE_URL;
  const fixture = externalURL ? null : await startFixture();
  const profile = fs.mkdtempSync(path.join(os.tmpdir(), 'nevr-ui-check-'));
  let browser, client;
  const violations = [];
  try {
    browser = await launchBrowser(browserPath, profile);
    client = await connect(browser.socketURL);
    const { send, evaluate } = client;
    await send('Page.enable'); await send('Runtime.enable');
    const waitFor = async (expression, what, timeout = 30000) => {
      const deadline = Date.now() + timeout;
      while (Date.now() < deadline) { if (await evaluate(expression)) return; await sleep(200); }
      throw new Error('timed out waiting for ' + what);
    };
    const shot = async (name, selector, minHeight = 200) => {
      if (!shotsDir) return;
      const clip = await evaluate(`(() => { const el = document.querySelector(${JSON.stringify(selector)}); if (!el) return null; el.scrollIntoView({ block: 'start' }); const r = el.getBoundingClientRect(); return { x: 0, y: Math.max(0, r.top + scrollY - 8), width: innerWidth, height: Math.min(Math.max(r.height + 16, ${minHeight}), 2600), scale: 1 }; })()`);
      if (!clip) { console.log(`  (no ${selector} to capture for ${name})`); return; }
      const reply = await send('Page.captureScreenshot', { format: 'png', captureBeyondViewport: true, clip });
      if (reply.result?.data) fs.writeFileSync(path.join(shotsDir, `${shotLabel}-${name}.png`), Buffer.from(reply.result.data, 'base64'));
    };

    for (const [width, theme, rowBudgets] of PASSES) {
      const tag = `${width}-${theme}`;
      console.log(`--- ${width} px, ${theme} theme`);
      await send('Emulation.setDeviceMetricsOverride', { width, height: HEIGHT, deviceScaleFactor: 1, mobile: false });
      await send('Page.navigate', { url: baseURL });
      await waitFor(`!!document.getElementById('pref-theme') && !document.querySelector('#history.skeleton') && !document.querySelector('#flagged.skeleton')`, 'the history and review panels');
      await evaluate(`(() => { const s = document.getElementById('pref-theme'); s.value = ${JSON.stringify(theme)}; s.dispatchEvent(new Event('change', { bubbles: true })); return document.documentElement.dataset.theme; })()`);
      const upload = option('--upload');
      if (upload) {
        const { result: { root } } = await send('DOM.getDocument', {});
        const { result: { nodeId } } = await send('DOM.querySelector', { nodeId: root.nodeId, selector: '#files' });
        await send('DOM.setFileInputFiles', { nodeId, files: [path.resolve(upload)] });
        await evaluate(`document.getElementById('files').dispatchEvent(new Event('change', { bubbles: true }))`);
        await waitFor(`!!document.querySelector('#results details.match, #results .fail')`, 'the uploaded analysis', 30 * 60 * 1000);
      } else {
      // Open the stored match with the most detector events so the report tables are populated.
      const matchID = await evaluate(`fetch('api/matches', { cache: 'no-store' }).then((r) => r.json()).then((body) => { const list = body.matches || body || []; const best = list.slice().sort((a, b) => (b.event_count || b.events || 0) - (a.event_count || a.events || 0) || (b.player_count || 0) - (a.player_count || 0))[0]; return best ? best.match_id : null; })`);
      if (!matchID) throw new Error('the page lists no stored match; is this the seeded fixture?');
      await evaluate(`(() => { const b = document.createElement('button'); b.hidden = true; b.dataset.open = ${JSON.stringify(matchID)}; document.body.appendChild(b); b.click(); b.remove(); })()`);
      }
      await waitFor(`!!document.querySelector('#results details.match')`, 'the match report');
      await evaluate(`(() => { document.querySelectorAll('#results details.match details.sub').forEach((d) => { d.open = true; }); })()`);
      await sleep(400);

      const metrics = await evaluate(MEASURE);
      console.log('  ' + JSON.stringify(metrics.counts));
      if (metrics.overflow > 1) violations.push(`[${tag}] the page overflows horizontally by ${metrics.overflow} px`);
      if (!metrics.counts.whoCells) violations.push(`[${tag}] no player cells were rendered, so nothing was measured`);
      for (const cell of metrics.who) violations.push(`[${tag}] player cell ${cell.width} px wide (minimum ${MIN_WHO_CELL}): "${cell.text}"`);
      for (const row of rowBudgets ? metrics.rows : []) violations.push(`[${tag}] ${row.selector} row ${row.height} px tall (budget ${row.budget}): "${row.text}"`);

      const stickyResults = await evaluate(STICKY);
      console.log('  pinned column ' + JSON.stringify(stickyResults));
      for (const sticky of stickyResults) {
        if (sticky.scrollable && sticky.drift > 2) violations.push(`[${tag}] ${sticky.selector} first column moved ${sticky.drift} px while the table scrolled ${sticky.moved} px (expected pinned)`);
      }

      await shot(`results-${tag}`, '#results-block');
      await shot(`report-${tag}`, '#results details.match .report-head', 1500);
      await shot(`stats-${tag}`, '#results details.match .stat-tables');
      await shot(`signals-${tag}`, '#results details.match .assessment');
      await shot(`detections-${tag}`, '#results details.match .events');

      // Larger text: SVG text is sized in rem, so the timeline layout must make room for it.
      const setTextSize = (value) => evaluate(`(() => { const s = document.getElementById('pref-text-size'); s.value = ${JSON.stringify(value)}; s.dispatchEvent(new Event('change', { bubbles: true })); })()`);
      await setTextSize('large');
      await sleep(400);
      const large = await evaluate(`(() => { const f = document.querySelector('#results details.match figure.tl'), svg = f && f.querySelector('svg.tl-svg'); if (!svg) return null; const names = [...svg.querySelectorAll('.tl-name')], base = svg.querySelector('.tl-absent'); return { names: names.length, fontPx: names.length ? parseFloat(getComputedStyle(names[0]).fontSize) : 0, widestName: Math.ceil(Math.max(0, ...names.map((n) => { const b = n.getBBox(); return b.x + b.width; }))), plotStart: base ? +base.getAttribute('x1') : 0, overflow: document.documentElement.scrollWidth - window.innerWidth }; })()`);
      await setTextSize('normal');
      await sleep(200);
      console.log('  larger text ' + JSON.stringify(large));
      if (!large || !large.names) violations.push(`[${tag}] no match timeline with player lanes was rendered, so larger text was not measured`);
      else {
        if (large.fontPx <= 12) violations.push(`[${tag}] timeline player names stay ${large.fontPx} px with the Larger text size (expected them to grow)`);
        if (large.widestName > large.plotStart - 4) violations.push(`[${tag}] with larger text a timeline player name ends at ${large.widestName} px but the plot starts at ${large.plotStart} px`);
        if (large.overflow > 1) violations.push(`[${tag}] with larger text the page overflows horizontally by ${large.overflow} px`);
      }

      // Scroll-spy: reading the results must mark a navigation entry other than Overview.
      await evaluate(`document.getElementById('results-block').scrollIntoView({ block: 'start' })`);
      await sleep(500);
      const current = await evaluate(`(() => { const a = document.querySelector('.workspace-nav a[aria-current]'); return a ? a.getAttribute('href') : null; })()`);
      if (current !== '#results-block' && current !== '#h-results') violations.push(`[${tag}] navigation marks ${current} while the Results section is at the top of the window`);

      await evaluate(`window.scrollTo(0, 0)`);
      await sleep(500);
      const atTop = await evaluate(`(() => { const a = document.querySelector('.workspace-nav a[aria-current]'); return a ? a.getAttribute('href') : null; })()`);
      if (atTop !== '#workspace') violations.push(`[${tag}] navigation marks ${atTop} at the top of the page (expected Overview)`);

      // Physics inspector rows.
      const opened = await evaluate(`(() => { const b = document.querySelector('#results [data-physics-event], #results [data-physics-frame]'); if (!b) return false; b.click(); return true; })()`);
      if (opened) {
        try {
          await waitFor(`!!document.querySelector('#physics-dialog[open] .physics-table tbody tr')`, 'the physics inspector', 20000);
          const physics = await evaluate(`(() => { const rows = [...document.querySelectorAll('#physics-dialog .physics-table tbody tr')].map((r) => Math.round(r.getBoundingClientRect().height)); return { rows: rows.length, tallest: Math.max(0, ...rows) }; })()`);
          console.log('  physics ' + JSON.stringify(physics));
          if (physics.tallest > PHYSICS_ROW_BUDGET) violations.push(`[${tag}] physics inspector row ${physics.tallest} px tall (budget ${PHYSICS_ROW_BUDGET})`);
          if (shotsDir) { const reply = await send('Page.captureScreenshot', { format: 'png' }); if (reply.result?.data) fs.writeFileSync(path.join(shotsDir, `${shotLabel}-physics-${tag}.png`), Buffer.from(reply.result.data, 'base64')); }
        } catch (error) { console.log('  physics inspector not measured: ' + error.message); }
        await evaluate(`document.getElementById('physics-dialog').close()`);
      }
    }
    for (const error of client.pageErrors) violations.push('uncaught page exception: ' + String(error).slice(0, 300));
  } finally {
    try { client?.close(); } catch (_) {}
    if (browser) { browser.child.kill(); await sleep(500); }
    if (fixture) await stopFixture(fixture);
    try { fs.rmSync(profile, { recursive: true, force: true, maxRetries: 5, retryDelay: 200 }); } catch (_) {}
  }
  if (violations.length) {
    console.log(`\n${violations.length} layout violation(s):`);
    const grouped = new Map();
    for (const violation of violations) grouped.set(violation, (grouped.get(violation) || 0) + 1);
    for (const [violation, count] of [...grouped].slice(0, 60)) console.log('  ' + violation + (count > 1 ? `  (x${count})` : ''));
    if (grouped.size > 60) console.log(`  ... and ${grouped.size - 60} more kinds`);
    return flag('--report-only') ? 0 : 1;
  }
  console.log(`\nAll rendered layout metrics are within budget (${option('--url') ? 'page at --url' : 'synthetic fixture data'}; layout only).`);
  return 0;
}

main().then((code) => process.exit(code), (error) => { console.error('ui-render-check could not run: ' + error.message); process.exit(2); });
