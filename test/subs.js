/* Headless end-to-end check of external subtitles: real sidecar binary + fake ASSRT + fake TorrServer.
 *   NODE_PATH=/tmp/cdp/node_modules node test/subs.js
 * Builds the sidecar (go build), starts it with TORR_SUBS_DIR in a temp dir and ASSRT pointed at a
 * fake that serves sidecar/subs/testdata/*.json plus a GBK .srt, then drives the TV app:
 * green key -> panel -> search -> pick result -> subtitle text appears at the right time -> delay
 * shifts it -> memory survives re-entering the file -> phone "sub" message loads a subtitle. */
const http = require('http');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync, spawn } = require('child_process');
const puppeteer = require('puppeteer-core');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const failures = [];
function assert(cond, msg) { if (!cond) { failures.push(msg); console.log('  FAIL ' + msg); } else { console.log('  ok   ' + msg); } }

const ROOT = path.join(__dirname, '..');
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'webos-torrserver-subs-'));
const HASH = '0123456789abcdef0123456789abcdef01234567';
const GOBIN = process.env.GO || 'go';

// GBK-encoded SRT: cue 1 at 2–4 s, cue 2 at 6–8 s
const srtText = '1\r\n00:00:02,000 --> 00:00:04,000\r\n你好，世界\r\n\r\n2\r\n00:00:06,000 --> 00:00:08,000\r\n第二句 <i>斜体</i>\r\n';
const srtEng = '1\r\n00:00:02,000 --> 00:00:04,000\r\nHello, world\r\n\r\n2\r\n00:00:06,000 --> 00:00:08,000\r\nSecond line\r\n';
const gbk = (() => { try { return new (require('util').TextEncoder)(); } catch (e) { return null; } })();
function toGBK(s) {
  // Node has no GBK encoder; write via iconv if available, else UTF-8 with BOM (still exercises the path)
  try { return execFileSync('iconv', ['-f', 'UTF-8', '-t', 'GBK'], { input: s }); } catch (e) { return Buffer.concat([Buffer.from([0xEF, 0xBB, 0xBF]), Buffer.from(s, 'utf8')]); }
}

function fakeServers() {
  const search = fs.readFileSync(path.join(ROOT, 'sidecar/subs/testdata/assrt_search.json'), 'utf8');
  const detail = fs.readFileSync(path.join(ROOT, 'sidecar/subs/testdata/assrt_detail.json'), 'utf8');
  let base = '';
  const srv = http.createServer((req, res) => {
    const u = new URL(req.url, 'http://x');
    let body = '';
    req.on('data', (d) => { body += d; });
    req.on('end', () => {
      const json = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' }); res.end(typeof obj === 'string' ? obj : JSON.stringify(obj)); };
      if (req.method === 'OPTIONS') { res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': 'GET, POST, OPTIONS', 'Access-Control-Allow-Headers': 'Content-Type' }); return res.end(); }
      // ---- fake ASSRT
      if (u.pathname === '/v1/sub/search') return json(200, search);
      if (u.pathname === '/v1/sub/detail') {
        // id 159430 (the 中英双字幕 entry) -> an English file; everything else the recorded detail
        if (u.searchParams.get('id') === '159430') return json(200, { status: 0, sub: { subs: [{ id: 159430, filelist: [{ f: 'Sintel.2010.eng.srt', url: base + '/dl/Sintel.2010.eng.srt' }] }] } });
        return json(200, detail.split('http://file1.assrt.net').join(base));
      }
      if (u.pathname.endsWith('.eng.srt')) { res.writeHead(200); return res.end(srtEng); }
      if (u.pathname.endsWith('.srt')) { res.writeHead(200, { 'Content-Type': 'application/octet-stream' }); return res.end(toGBK(srtText)); }
      // ---- fake TorrServer (the TV only needs list/get + a stream)
      if (u.pathname === '/torrents') {
        const b = body ? JSON.parse(body) : {};
        const t = { hash: HASH, title: 'Sintel', stat: 3, torrent_size: 1000, loaded_size: 1000, file_stats: [{ id: 1, path: 'Sintel/Sintel.2010.1080p.x264.mkv', length: 1000 }, { id: 2, path: 'Sintel/Sintel.2010.720p.mp4', length: 900 }] };
        return json(200, b.action === 'list' ? [t] : t);
      }
      if (u.pathname.startsWith('/stream/')) {
        // a 20 s silent mp4 fixture so currentTime advances
        const f = path.join(TMP, 'clip.mp4');
        const st = fs.statSync(f);
        const range = /^bytes=(\d*)-(\d*)$/.exec(req.headers.range || '');
        res.setHeader('Accept-Ranges', 'bytes'); res.setHeader('Content-Type', 'video/mp4');
        if (range) {
          const start = range[1] ? +range[1] : 0, end = range[2] ? Math.min(st.size - 1, +range[2]) : st.size - 1;
          res.writeHead(206, { 'Content-Range': `bytes ${start}-${end}/${st.size}`, 'Content-Length': end - start + 1 });
          return fs.createReadStream(f, { start, end }).pipe(res);
        }
        res.writeHead(200, { 'Content-Length': st.size }); return fs.createReadStream(f).pipe(res);
      }
      res.writeHead(404); res.end('nope');
    });
  });
  return new Promise((resolve) => srv.listen(0, '127.0.0.1', () => { base = `http://127.0.0.1:${srv.address().port}`; resolve({ srv, base }); }));
}

async function key(page, code, wait) {
  await page.evaluate((c) => {
    const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true });
    Object.defineProperty(ev, 'keyCode', { get: () => c }); Object.defineProperty(ev, 'which', { get: () => c });
    document.dispatchEvent(ev);
  }, code);
  await sleep(wait || 300);
}
const state = (page) => page.evaluate(() => {
  const v = document.getElementById('video'), f = document.querySelector('.focus');
  const s = App.view('player').subtitle();
  return { t: +v.currentTime.toFixed(1), paused: v.paused, sub: s, badge: document.getElementById('osd-sub').textContent, marks: Array.from(document.querySelectorAll('.sub-row .mark')).map((m) => m.textContent),
    focus: f ? f.textContent.slice(0, 30) : null, msg: document.getElementById('osd-subs-msg').textContent, state: document.getElementById('osd-state').textContent,
    rows: Array.from(document.querySelectorAll('.sub-row')).map((r) => r.textContent.slice(0, 40)) };
});
async function focusRow(page, re) {
  for (let i = 0; i < 15; i++) await key(page, 38, 60);          // go to top
  for (let i = 0; i < 20; i++) { const f = (await state(page)).focus || ''; if (re.test(f)) return true; await key(page, 40, 100); }
  return false;
}
async function waitFor(page, pred, ms) {
  const until = Date.now() + (ms || 8000);
  let s;
  while (Date.now() < until) { s = await state(page); if (pred(s)) return s; await sleep(200); }
  return s;
}

(async () => {
  // fixtures
  execFileSync('ffmpeg', ['-y', '-v', 'error', '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=25', '-f', 'lavfi', '-i', 'anullsrc=r=48000:cl=stereo', '-t', '20', '-c:v', 'libx264', '-preset', 'veryfast', '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-movflags', '+faststart', path.join(TMP, 'clip.mp4')]);
  const { srv: fake, base } = await fakeServers();
  console.log('fake ASSRT/TorrServer at', base);

  // sidecar
  const bin = path.join(TMP, 'torr-remote');
  execFileSync(GOBIN, ['build', '-o', bin, '.'], { cwd: path.join(ROOT, 'sidecar'), env: Object.assign({}, process.env, { TMPDIR: process.env.TMPDIR || path.join(os.homedir(), '.cache/gotmp'), CGO_ENABLED: '0' }), stdio: 'inherit' });
  const PORT = 18791 + Math.floor(Math.random() * 1000);
  const side = spawn(bin, [], { env: Object.assign({}, process.env, { TORR_SERVER: base, TORR_PORT: String(PORT), TORR_APP_DIR: path.join(ROOT, 'app'), TORR_SUBS_DIR: path.join(TMP, 'subs'), ASSRT_TOKEN: 'tok', ASSRT_BASE: base + '/v1', ASSRT_SPACING_MS: '50' }), stdio: ['ignore', 'pipe', 'pipe'] });
  side.stdout.on('data', (d) => process.stdout.write('  [sidecar] ' + d));
  side.stderr.on('data', (d) => process.stdout.write('  [sidecar] ' + d));
  const SIDECAR = `http://127.0.0.1:${PORT}`;
  for (let i = 0; i < 50; i++) { try { await (await fetch(SIDECAR + '/health')).json(); break; } catch (e) { await sleep(100); } }
  const cfg = await (await fetch(SIDECAR + '/config')).json();
  assert(cfg.subs && cfg.subs.enabled && cfg.subs.sources.indexOf('assrt') >= 0, '/config reports subtitles enabled with assrt: ' + JSON.stringify(cfg.subs));

  const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new',
    args: ['--no-sandbox', '--window-size=1920,1080', '--autoplay-policy=no-user-gesture-required', '--mute-audio'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1920, height: 1080 });
  page.on('pageerror', (e) => { failures.push('pageerror ' + e); console.log('  PAGE ERROR ' + e); });
  await page.goto(SIDECAR + '/tv/', { waitUntil: 'load' });
  await sleep(1500);
  await page.evaluate((h) => { localStorage.removeItem('torr.sub.' + h); App.goFresh('player', { hash: h, index: 1, title: 'Sintel' }); }, HASH);
  let s = await waitFor(page, (x) => !x.paused && x.t > 0.5);
  if (s.paused) console.log('  [debug]', JSON.stringify(await page.evaluate(() => { const v = document.getElementById('video'); return { src: v.currentSrc, err: v.error && v.error.code, rs: v.readyState, state: document.getElementById('osd-state').textContent, ts: API.torrServer() }; })));
  assert(!s.paused && s.t > 0.5, 'player is playing the fixture');
  assert(!s.sub.current, 'no subtitle yet');

  // green key -> panel
  await key(page, 404, 800);
  s = await state(page);
  assert(s.sub.panel && s.rows.length >= 2, 'green key opens the subtitle panel: ' + JSON.stringify(s.rows));
  assert(s.rows.some((r) => /在线搜索字幕/.test(r)), 'panel offers online search');

  // move focus to "在线搜索字幕" and press OK
  for (let i = 0; i < 5 && !/在线搜索字幕/.test((await state(page)).focus || ''); i++) await key(page, 40, 150);
  await key(page, 13, 300);
  s = await waitFor(page, (x) => x.rows.some((r) => /搜索结果/.test(r)), 15000);
  assert(s.rows.some((r) => /搜索结果（4）/.test(r)), 'search results listed: ' + s.msg);
  assert(s.msg.indexOf('Sintel 2010') >= 0, 'search query derived from the file name: ' + s.msg);
  assert(/2010\.Sintel\.UTF-8\.tw/.test(s.focus || ''), 'focus jumps to the first result: ' + s.focus);

  await page.screenshot({ path: path.join(__dirname, 'shots', '09-subtitle-panel.png') }).catch(() => {});
  // pick it -> fetched, converted, loaded
  await key(page, 13, 300);
  s = await waitFor(page, (x) => x.sub.current && x.sub.cues > 0, 15000);
  assert(s.sub.current && s.sub.cues === 2, 'result fetched and loaded (' + JSON.stringify(s.sub) + ')');
  assert(s.sub.panel, 'panel stays open after picking (so a second language can be added)');
  assert(/繁中/.test(s.badge), 'OSD badge shows the language: ' + s.badge);
  assert(s.marks.length === 1 && /①/.test(s.marks[0]), 'saved row marked as primary ①: ' + JSON.stringify(s.marks));

  // second language: pick the 中英双字幕 result (served as an English file by the fake)
  assert(await focusRow(page, /中英双字幕/), 'focus on the second result');
  await key(page, 13, 300);
  s = await waitFor(page, (x) => x.sub.second && x.sub.cues2 > 0, 15000);
  assert(s.sub.second && s.sub.cues2 === 2, 'second subtitle loaded into slot 2: ' + JSON.stringify(s.sub.second));
  assert(/繁中 \+ 英文/.test(s.badge), 'badge shows both: ' + s.badge);
  assert(s.marks.length === 2, 'two rows marked ① and ②: ' + JSON.stringify(s.marks));
  assert(s.rows.some((r) => /交换上下/.test(r)), 'swap row offered when both slots are used');
  await key(page, 404, 400); // close panel
  await page.evaluate(() => { document.getElementById('video').currentTime = 2.5; });
  s = await waitFor(page, (x) => /你好/.test(x.sub.text) && /Hello/.test(x.sub.text2), 3000);
  assert(/你好，世界/.test(s.sub.text) && /Hello, world/.test(s.sub.text2), 'both languages rendered at t=2.5: ' + JSON.stringify([s.sub.text, s.sub.text2]));
  const pos = await page.evaluate(() => { const a = document.getElementById('subtitle').getBoundingClientRect(), b = document.getElementById('subtitle2').getBoundingClientRect(); return { primaryBottom: a.bottom, secondaryBottom: b.bottom }; });
  assert(pos.secondaryBottom < 200 && pos.primaryBottom > 600, 'secondary at the top, primary at the bottom: ' + JSON.stringify(pos));
  await page.screenshot({ path: path.join(__dirname, 'shots', '11-two-subtitles.png') }).catch(() => {});

  // swap: English becomes primary
  await key(page, 404, 600);
  await focusRow(page, /交换上下/);
  await key(page, 13, 500);
  s = await state(page);
  assert(s.sub.current && /eng/.test(s.sub.current.name) && s.sub.second && /tw/.test(s.sub.second.name), 'swap exchanges the slots: ' + JSON.stringify([s.sub.current.name, s.sub.second.name]));
  await key(page, 13, 500); // swap back
  // toggle the primary off via its row -> the remaining one becomes primary
  await focusRow(page, /①/);
  await key(page, 13, 600);
  s = await state(page);
  assert(s.sub.current && /eng/.test(s.sub.current.name) && !s.sub.second, 'deselecting the primary promotes the secondary: ' + JSON.stringify(s.sub));
  // re-select 繁中 -> goes to slot 2
  await focusRow(page, /2010\.Sintel\.UTF-8\.tw/);
  await key(page, 13, 600);
  s = await waitFor(page, (x) => x.sub.second && x.sub.cues2 > 0, 5000);
  assert(s.sub.second && /tw/.test(s.sub.second.name), 'reselected subtitle fills slot 2');
  await key(page, 13, 600); // same row again -> deselect
  s = await state(page);
  assert(!s.sub.second, 'toggling a selected row removes it');
  // put 繁中 back as the only subtitle for the remaining single-slot checks:
  // currently eng is primary alone -> select tw (slot 2), swap, deselect eng
  assert(await focusRow(page, /2010\.Sintel\.UTF-8\.tw/), 'focus tw row');
  await key(page, 13, 600);
  assert(await focusRow(page, /交换上下/), 'focus swap row');
  await key(page, 13, 400);
  assert(await focusRow(page, /②.*eng/), 'focus eng row (now ②)');
  await key(page, 13, 600);
  s = await waitFor(page, (x) => x.sub.current && /tw/.test(x.sub.current.name) && !x.sub.second, 5000);
  assert(s.sub.current && /tw/.test(s.sub.current.name) && !s.sub.second, 'back to 繁中 only: ' + JSON.stringify([s.sub.current && s.sub.current.name, s.sub.second]));
  await key(page, 404, 400);

  // text appears during 2–4 s
  await page.evaluate(() => { document.getElementById('video').currentTime = 2.5; });
  s = await waitFor(page, (x) => /你好，世界/.test(x.sub.text), 3000);
  assert(/你好，世界/.test(s.sub.text), 'GBK subtitle rendered at t=2.5: ' + JSON.stringify(s.sub.text));
  await page.evaluate(() => { document.getElementById('video').currentTime = 5; });
  s = await waitFor(page, (x) => !x.sub.text, 3000);
  assert(!s.sub.text, 'no text between cues at t=5');
  await page.evaluate(() => { document.getElementById('video').currentTime = 6.5; });
  s = await waitFor(page, (x) => /第二句/.test(x.sub.text), 3000);
  assert(/第二句/.test(s.sub.text), 'second cue at t=6.5');
  await key(page, 13, 100); await key(page, 13, 400); // toggle pause twice to show the OSD alongside the subtitle
  await page.screenshot({ path: path.join(__dirname, 'shots', '10-subtitle-osd.png') }).catch(() => {});
  const italic = await page.evaluate(() => !!document.querySelector('#subtitle i'));
  assert(italic, '<i> preserved in rendering');

  // delay: +1 s via panel's right key, cue 1 should now show at t=3.3 .. 5
  await key(page, 404, 600);
  await key(page, 39, 200); await key(page, 39, 200);
  s = await state(page);
  assert(s.sub.offset === 1000, 'two right presses = +1.0 s delay (' + s.sub.offset + ')');
  assert(/\+1\.0s/.test(s.badge), 'badge shows the delay: ' + s.badge);
  await key(page, 461, 400); // back closes panel
  await page.evaluate(() => { document.getElementById('video').pause(); document.getElementById('video').currentTime = 4.5; });
  s = await waitFor(page, (x) => /你好/.test(x.sub.text), 3000);
  assert(/你好/.test(s.sub.text), 'with +1 s delay the first cue is visible at t=4.5');

  // memory: leave and re-enter the same file -> same subtitle + offset restored
  await page.evaluate((h) => { App.view('player').leave(); App.goFresh('player', { hash: h, index: 1, title: 'Sintel' }); }, HASH);
  s = await waitFor(page, (x) => x.sub.current && x.sub.cues > 0, 8000);
  assert(s.sub.current && s.sub.offset === 1000, 'subtitle + delay restored on re-entry: ' + JSON.stringify(s.sub));
  // add English as second again, re-enter: both restored
  await key(page, 404, 600);
  await focusRow(page, /eng/);
  await key(page, 13, 600);
  s = await waitFor(page, (x) => x.sub.second, 5000);
  await key(page, 404, 400);
  await page.evaluate((h) => { App.view('player').leave(); App.goFresh('player', { hash: h, index: 1, title: 'Sintel' }); }, HASH);
  s = await waitFor(page, (x) => x.sub.current && x.sub.second && x.sub.cues > 0 && x.sub.cues2 > 0, 8000);
  assert(s.sub.current && s.sub.second && s.sub.offset === 1000, 'both subtitles + delay restored on re-entry: ' + s.badge);

  // switch to file 2 (no memory, nothing saved) -> no subtitle; phone "sub" message loads one
  await page.evaluate((h) => { App.view('player').leave(); App.goFresh('player', { hash: h, index: 2, title: 'Sintel' }); }, HASH);
  await sleep(1500);
  s = await state(page);
  assert(!s.sub.current, 'file 2 starts without subtitle');
  const up = await fetch(SIDECAR + `/subs/upload?hash=${HASH}&index=2&name=phone.chs.srt`, { method: 'POST', body: toGBK(srtText) });
  const upj = await up.json();
  assert(up.status === 200 && upj.sub && upj.sub.label === '简中', 'phone upload accepted: ' + JSON.stringify(upj.sub));
  const rid = await page.evaluate(() => App.Remote.id());
  const sent = await fetch(SIDECAR + `/remote/${rid}/send`, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ action: 'sub', hash: HASH, index: 2, url: upj.sub.url, title: '简中' }) });
  assert(sent.status === 200, 'remote sub message accepted');
  s = await waitFor(page, (x) => x.sub.current && x.sub.cues === 2, 8000);
  assert(s.sub.current && s.sub.current.url === upj.sub.url, 'TV loaded the phone-provided subtitle: ' + JSON.stringify(s.sub.current));

  // "off" is remembered too
  await key(page, 404, 600);
  s = await state(page);
  assert(s.rows.some((r) => /上传/.test(r)), 'panel lists the uploaded subtitle with its source tag');
  for (let i = 0; i < 6 && !/关闭字幕/.test((await state(page)).focus || ''); i++) await key(page, 38, 150);
  await key(page, 13, 500);
  s = await state(page);
  assert(!s.sub.current && !s.sub.panel, 'off closes the subtitle and the panel');
  await page.evaluate((h) => { App.view('player').leave(); App.goFresh('player', { hash: h, index: 2, title: 'Sintel' }); }, HASH);
  await sleep(1500);
  s = await state(page);
  assert(!s.sub.current, '"off" remembered: re-entering file 2 does not auto-load the saved subtitle');

  await page.screenshot({ path: path.join(__dirname, 'shots', '09-subtitles.png') }).catch(() => {});
  await browser.close();
  side.kill();
  fake.close();
  fs.rmSync(TMP, { recursive: true, force: true });
  console.log(failures.length ? `\n${failures.length} FAILED` : '\nall ok');
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
