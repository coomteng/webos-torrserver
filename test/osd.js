/* Headless check that one-shot notices on the player's state line (#osd-state) go away by themselves
 * while persistent states stay.
 *   NODE_PATH=/tmp/cdp/node_modules node test/osd.js
 * Self-contained like test/audio.js: ffmpeg synthesises a 1-audio-track and a 2-audio-track MKV, a tiny
 * Node server plays TorrServer + sidecar, Chrome runs with --enable-blink-features=AudioVideoTracks.
 * #osd-state is a sibling of the bottom bar #osd, so hiding the bar never cleared it: "只有一条音轨"
 * used to sit over the picture until the next play/playing event. */
const http = require('http');
const fs = require('fs');
const os = require('os');
const path = require('path');
const { execFileSync } = require('child_process');
const puppeteer = require('puppeteer-core');

const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const failures = [];
function assert(cond, msg) { if (!cond) { failures.push(msg); console.log('  FAIL ' + msg); } else { console.log('  ok   ' + msg); } }

const APP = path.join(__dirname, '..', 'app');
const TMP = path.join(os.tmpdir(), 'webos-torrserver-osd');
const ONE = path.join(TMP, 'one.mkv');
const TWO = path.join(TMP, 'two.mkv');
const HASH = '0123456789abcdef0123456789abcdef01234567';
const OSD_TIMEOUT = 4000;   // app/js/player.js
let slowMs = 0;              // delays /torrents so a session can be held on its loading state

function fixture() {
  fs.mkdirSync(TMP, { recursive: true });
  const common = ['-c:v', 'libx264', '-preset', 'veryfast', '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-b:a', '96k'];
  if (!fs.existsSync(ONE)) execFileSync('ffmpeg', ['-y', '-v', 'error',
    '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=25',
    '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
    '-t', '60', '-map', '0:v', '-map', '1:a'].concat(common, ['-metadata:s:a:0', 'language=eng', ONE]));
  if (!fs.existsSync(TWO)) execFileSync('ffmpeg', ['-y', '-v', 'error',
    '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=25',
    '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
    '-f', 'lavfi', '-i', 'sine=frequency=1600:sample_rate=48000',
    '-t', '60', '-map', '0:v', '-map', '1:a', '-map', '2:a'].concat(common, [
    '-metadata:s:a:0', 'language=eng', '-metadata:s:a:1', 'language=chi', '-disposition:a:0', 'default', TWO]));
}

const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'application/javascript', '.css': 'text/css', '.png': 'image/png', '.json': 'application/json' };
function serveFile(req, res, file) {
  const st = fs.statSync(file);
  const range = /^bytes=(\d*)-(\d*)$/.exec(req.headers.range || '');
  res.setHeader('Accept-Ranges', 'bytes');
  res.setHeader('Content-Type', MIME[path.extname(file)] || 'application/octet-stream');
  if (range) {
    const start = range[1] ? parseInt(range[1], 10) : Math.max(0, st.size - parseInt(range[2], 10));
    const end = range[1] && range[2] ? Math.min(st.size - 1, parseInt(range[2], 10)) : st.size - 1;
    res.writeHead(206, { 'Content-Range': `bytes ${start}-${end}/${st.size}`, 'Content-Length': end - start + 1 });
    fs.createReadStream(file, { start, end }).pipe(res);
  } else {
    res.writeHead(200, { 'Content-Length': st.size });
    fs.createReadStream(file).pipe(res);
  }
}
function fakeServer() {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const u = new URL(req.url, 'http://x');
      let body = '';
      req.on('data', (d) => { body += d; });
      req.on('end', () => {
        const json = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' }); res.end(JSON.stringify(obj)); };
        if (u.pathname === '/config') return json(200, { torrserver: `http://127.0.0.1:${srv.address().port}` });
        if (u.pathname === '/torrents') {
          const b = body ? JSON.parse(body) : {};
          const size = fs.statSync(ONE).size + fs.statSync(TWO).size;
          const t = { hash: HASH, title: 'OSD', stat: 3, torrent_size: size, loaded_size: size,
            file_stats: [{ id: 1, path: 'OSD/one.mkv', length: fs.statSync(ONE).size }, { id: 2, path: 'OSD/two.mkv', length: fs.statSync(TWO).size }] };
          return setTimeout(() => json(200, b.action === 'list' ? [t] : t), slowMs);
        }
        if (u.pathname.indexOf('/stream/') === 0) return serveFile(req, res, u.searchParams.get('index') === '2' ? TWO : ONE);
        if (u.pathname.indexOf('/remote/') === 0 || u.pathname === '/viewed') return json(404, { error: 'not in this test' });
        let p = u.pathname.replace(/^\/tv\/?/, '') || 'index.html';
        const file = path.join(APP, p);
        if (file.indexOf(APP) === 0 && fs.existsSync(file) && fs.statSync(file).isFile()) return serveFile(req, res, file);
        res.writeHead(404); res.end('nope');
      });
    });
    srv.listen(0, '127.0.0.1', () => resolve(srv));
  });
}

async function key(page, code, wait) {
  await page.evaluate((c) => {
    const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true });
    Object.defineProperty(ev, 'keyCode', { get: () => c }); Object.defineProperty(ev, 'which', { get: () => c });
    document.dispatchEvent(ev);
  }, code);
  await sleep(wait || 300);
}
/* tap = keydown + keyup in one tick (keyup stops the seek-hold repeat); returns the state line as it is
 * right after the handler, before any media event can rewrite it */
const tap = (page, code) => page.evaluate((c) => {
  ['keydown', 'keyup'].forEach((type) => {
    const ev = new KeyboardEvent(type, { bubbles: true, cancelable: true });
    Object.defineProperty(ev, 'keyCode', { get: () => c }); Object.defineProperty(ev, 'which', { get: () => c });
    document.dispatchEvent(ev);
  });
  return document.getElementById('osd-state').textContent;
}, code);
const osd = (page) => page.evaluate(() => {
  const s = document.getElementById('osd-state'), v = document.getElementById('video');
  return { state: s.textContent, show: /(^|\s)show(\s|$)/.test(s.className), visible: getComputedStyle(s).display !== 'none',
    tracks: App.view('player').audioTracks().length, t: v.currentTime, paused: v.paused };
});
const gone = (s) => s.state === '' && !s.show && !s.visible;
async function open(page, index, again) {
  if (again) await page.evaluate(() => App.view('player').leave());
  await page.evaluate((h, i) => App.goFresh('player', { hash: h, index: i, title: 'OSD' }), HASH, index);
  await sleep(3000);
  const s = await osd(page);
  console.log('[player index ' + index + ']', JSON.stringify(s));
  return s;
}

(async () => {
  fixture();
  const srv = await fakeServer();
  const BASE = `http://127.0.0.1:${srv.address().port}`;
  console.log('fake TorrServer/sidecar at', BASE, 'fixtures', TMP);

  const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new',
    args: ['--no-sandbox', '--window-size=1920,1080', '--autoplay-policy=no-user-gesture-required', '--enable-blink-features=AudioVideoTracks'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1920, height: 1080 });
  page.on('pageerror', (e) => { failures.push('pageerror ' + e); console.log('  PAGE ERROR ' + e); });

  await page.goto(BASE + '/tv/', { waitUntil: 'load' });
  await sleep(1500);
  await page.evaluate(() => localStorage.removeItem('torr.lang'));

  /* 1. single audio track: the yellow key only says so, and the notice must leave by itself */
  let s = await open(page, 1);
  assert(s.tracks === 1, 'file exposes 1 audio track (' + s.tracks + ')');
  assert(!s.paused && s.t > 0.5 && gone(s), 'playing with an empty state line');
  await key(page, 405, 500);  // yellow
  s = await osd(page);
  assert(s.state === '只有一条音轨' && s.show && s.visible, 'yellow key shows the single-track notice: ' + JSON.stringify(s.state));
  await sleep(OSD_TIMEOUT + 500);
  s = await osd(page);
  assert(!s.paused, 'still playing ' + (OSD_TIMEOUT + 1000) + ' ms after the key');
  assert(gone(s), 'single-track notice cleared after the OSD timeout: ' + JSON.stringify(s));

  /* 2. repeating the key restarts the countdown instead of cutting the newer notice short */
  await key(page, 405, 3000);
  await key(page, 405, 2000);
  s = await osd(page);
  assert(s.state === '只有一条音轨' && s.show, 'notice repeated at +3 s is still up 2 s later: ' + JSON.stringify(s.state));
  await sleep(3000);
  s = await osd(page);
  assert(gone(s), 'and cleared ' + OSD_TIMEOUT + ' ms after the last press: ' + JSON.stringify(s));

  /* 3. persistent state: paused stays up past the timeout, also when a notice's timer was pending */
  await key(page, 405, 500);
  await key(page, 19, 500);   // pause
  s = await osd(page);
  assert(s.paused && s.state === '已暂停' && s.show, 'pause shows 已暂停: ' + JSON.stringify(s.state));
  await sleep(OSD_TIMEOUT + 1000);
  s = await osd(page);
  assert(s.paused && s.state === '已暂停' && s.show && s.visible, '已暂停 is still shown past the OSD timeout: ' + JSON.stringify(s));
  await key(page, 415, 800);  // play
  s = await osd(page);
  assert(!s.paused && gone(s), 'resuming clears 已暂停');

  /* 3b. a notice over a persistent state gives the line back to that state when it expires */
  await key(page, 19, 500);
  await key(page, 405, 500);
  s = await osd(page);
  assert(s.paused && s.state === '只有一条音轨' && s.show, 'notice shown over 已暂停: ' + JSON.stringify(s.state));
  await sleep(OSD_TIMEOUT + 100);
  s = await osd(page);
  assert(s.paused && s.state === '已暂停' && s.show && s.visible, '已暂停 is back once the notice expired: ' + JSON.stringify(s));

  /* 3c. ...but not when the state ended meanwhile: notice, then resume before it expires */
  await key(page, 405, 500);
  s = await osd(page);
  assert(s.paused && s.state === '只有一条音轨', 'notice shown again while paused: ' + JSON.stringify(s.state));
  await key(page, 415, 800);
  s = await osd(page);
  assert(!s.paused && gone(s), 'resuming clears the notice');
  await sleep(OSD_TIMEOUT);
  s = await osd(page);
  assert(!s.paused && gone(s), 'no stale 已暂停 after the old timeout while playing: ' + JSON.stringify(s));

  /* 3d. seek hint while playing: shown, then gone */
  const before = s.t;
  let hint = await tap(page, 39);
  assert(hint === '▶▶ +10 秒', 'right key shows the seek hint: ' + JSON.stringify(hint));
  await sleep(OSD_TIMEOUT + 600);
  s = await osd(page);
  assert(!s.paused && s.t > before + 10 && gone(s), 'seek hint gone after the OSD timeout, still playing: ' + JSON.stringify(s));

  /* 3e. seek hint while paused: 已暂停 comes back */
  await key(page, 19, 500);
  hint = await tap(page, 39);
  assert(hint === '▶▶ +10 秒', 'right key shows the seek hint while paused: ' + JSON.stringify(hint));
  await sleep(OSD_TIMEOUT + 600);
  s = await osd(page);
  assert(s.paused && s.state === '已暂停' && s.show, '已暂停 is back after the seek hint: ' + JSON.stringify(s));
  await key(page, 415, 800);
  s = await osd(page);
  assert(!s.paused && gone(s), 'resuming clears 已暂停 again');

  /* 4. two audio tracks: the "音轨：…" announcement leaves by itself too */
  s = await open(page, 2, true);
  assert(s.tracks === 2, 'file exposes 2 audio tracks (' + s.tracks + ')');
  assert(!s.paused && s.t > 0.5, 'playing');
  await key(page, 405, 500);
  s = await osd(page);
  assert(/^音轨：/.test(s.state) && s.show && s.visible, 'yellow key announces the new track: ' + JSON.stringify(s.state));
  await sleep(OSD_TIMEOUT + 500);
  s = await osd(page);
  assert(!s.paused, 'still playing after the switch');
  assert(gone(s), 'track announcement cleared after the OSD timeout: ' + JSON.stringify(s));

  /* 5. leaving with a notice pending: its timeout must not touch the next session's state line */
  await key(page, 19, 500);
  await key(page, 405, 300);
  s = await osd(page);
  assert(s.paused && /^音轨：/.test(s.state), 'notice pending over 已暂停 before leaving: ' + JSON.stringify(s.state));
  slowMs = OSD_TIMEOUT + 2500;
  await page.evaluate(() => App.view('player').leave());
  s = await osd(page);
  assert(gone(s), 'leave clears the state line: ' + JSON.stringify(s));
  await page.evaluate((h) => App.goFresh('player', { hash: h, index: 1, title: 'OSD' }), HASH);
  await sleep(300);
  s = await osd(page);
  assert(s.state === '加载中…' && s.show, 'new session shows 加载中…: ' + JSON.stringify(s.state));
  await sleep(OSD_TIMEOUT + 500);
  s = await osd(page);
  assert(s.state === '加载中…' && s.show && s.visible, '加载中… untouched when the old notice would have expired: ' + JSON.stringify(s));
  slowMs = 0;
  await sleep(5000);
  s = await osd(page);
  assert(!s.paused && s.t > 0.5 && gone(s), 'new session plays with an empty state line: ' + JSON.stringify(s));

  await browser.close();
  srv.close();
  console.log(failures.length ? `\n${failures.length} FAILED` : '\nall ok');
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
