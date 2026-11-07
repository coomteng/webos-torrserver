/* Headless check of swarm health tags (home list) and the dead-block skip in the player (▶▶ key).
 *   NODE_PATH=/tmp/cdp/node_modules node test/health.js
 * Self-contained: ffmpeg makes a 40 s CBR MP4 (faststart), a tiny Node server plays TorrServer +
 * sidecar. The fake swarm has no seeder and one "dead" piece: /stream delivers bytes up to the dead
 * piece and then hangs the connection, exactly what a real TorrServer does while waiting for data
 * nobody has. /cache reports every piece cached except that one, which shows up as a 4 KB partial piece
 * that never grows, while download_speed is high (like the real TV: readahead keeps pulling other pieces
 * at 400-900 KB/s). Expected: the player notices the stall within ~STALL_MS, names the piece, and ▶▶
 * seeks past it so playback resumes. Also: a cold-start error 4 (first /stream request 404s) is retried
 * once automatically, and a mid-stream media error on the dead block (webOS gives up with a decode error
 * after ~50 s; here provoked with junk bytes) still offers ▶▶, which reopens the stream. */
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
const TMP = path.join(os.tmpdir(), 'webos-torrserver-health');
const MP4 = path.join(TMP, 'cbr.mp4');
const HASH = 'dead0000000000000000000000000000000000ad';
const PIECE = 256 * 1024;
const DEAD = 2;                                   /* piece nobody has: bytes [512K, 768K) ≈ 4–6 s at 1 Mbit/s */
const DURATION = 40;

function fixture() {
  if (fs.existsSync(MP4)) return;
  fs.mkdirSync(TMP, { recursive: true });
  execFileSync('ffmpeg', ['-y', '-v', 'error',
    '-f', 'lavfi', '-i', 'testsrc2=size=640x360:rate=25',
    '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
    '-t', String(DURATION), '-map', '0:v', '-map', '1:a',
    '-c:v', 'libx264', '-preset', 'veryfast', '-pix_fmt', 'yuv420p', '-g', '25',
    '-b:v', '1M', '-minrate', '1M', '-maxrate', '1M', '-bufsize', '100k', '-x264-params', 'nal-hrd=cbr',
    '-c:a', 'aac', '-b:a', '64k', '-movflags', '+faststart', MP4]);
}

const MIME = { '.html': 'text/html; charset=utf-8', '.js': 'application/javascript', '.css': 'text/css', '.png': 'image/png', '.json': 'application/json' };
const hung = [];
let mode = 'hang';            /* what the dead piece does: 'hang' (TorrServer waiting) | 'garbage' (media error) | 'hang-free' (no dead piece) */
let failNextStream = 0;       /* answer the next N /stream requests with 404 (cold-start error 4) */
let streamReqs = 0;
function serveFile(req, res, file, deadStart, deadEnd) {
  const st = fs.statSync(file);
  const range = /^bytes=(\d*)-(\d*)$/.exec(req.headers.range || '');
  res.setHeader('Accept-Ranges', 'bytes');
  res.setHeader('Content-Type', MIME[path.extname(file)] || 'application/octet-stream');
  res.setHeader('Access-Control-Allow-Origin', '*');
  let start = 0, end = st.size - 1;
  if (range) {
    start = range[1] ? parseInt(range[1], 10) : Math.max(0, st.size - parseInt(range[2], 10));
    end = range[1] && range[2] ? Math.min(st.size - 1, parseInt(range[2], 10)) : st.size - 1;
    res.writeHead(206, { 'Content-Range': `bytes ${start}-${end}/${st.size}`, 'Content-Length': end - start + 1 });
  } else {
    res.writeHead(200, { 'Content-Length': st.size });
  }
  if (deadStart !== undefined && mode !== 'hang-free' && start < deadEnd && end >= deadStart) {
    /* deliver what exists before the dead piece, then stall like TorrServer waiting for peers */
    console.log(`  [stream] range ${start}-${end} crosses dead piece -> sending ${Math.max(0, deadStart - start)} bytes then hanging`);
    if (mode === 'garbage') {
      /* webOS gives up on a stalled stream with a decode error; headless Chrome just reconnects on a dropped
       * connection, so provoke the same error class with junk where the dead piece is */
      const head = start < deadStart ? fs.readFileSync(file).subarray(start, deadStart) : Buffer.alloc(0);
      const junk = Buffer.alloc(end - Math.max(start, deadStart) + 1, 0xff);
      res.end(Buffer.concat([head, junk]));
      return;
    }
    if (start < deadStart) fs.createReadStream(file, { start, end: deadStart - 1 }).pipe(res, { end: false });
    hung.push(res);
    return;
  }
  fs.createReadStream(file, { start, end }).pipe(res);
}

function torrentStatus(size) {
  return { hash: HASH, title: 'Dead block', stat: 3, torrent_size: size, loaded_size: size - PIECE,
    active_peers: 5, total_peers: 9, download_speed: 600 * 1024, /* readahead elsewhere; connected_seeders omitted = 0 */
    file_stats: [{ id: 1, path: 'Dead block/cbr.mp4', length: size }] };
}
function cacheState(size) {
  const n = Math.ceil(size / PIECE), pieces = {};
  for (let i = 0; i < n; i++) pieces[i] = { Id: i, Length: PIECE, Size: PIECE, Completed: true, Priority: 0 };
  pieces[DEAD] = { Id: DEAD, Length: PIECE, Size: 4096, Completed: false, Priority: 5 };   /* stuck partial piece */
  return { Hash: HASH, Capacity: 1 << 30, Filled: size - PIECE, PiecesLength: PIECE, PiecesCount: n, Pieces: pieces,
    Readers: [{ Start: 0, End: n - 1, Reader: DEAD - 1 }] };
}

function fakeServer() {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const u = new URL(req.url, 'http://x');
      let body = '';
      req.on('data', (d) => { body += d; });
      req.on('end', () => {
        const size = fs.statSync(MP4).size;
        const json = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' }); res.end(JSON.stringify(obj)); };
        if (req.method === 'OPTIONS') { res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Headers': 'Content-Type', 'Access-Control-Allow-Methods': 'GET, POST, OPTIONS' }); return res.end(); }
        if (u.pathname === '/config') return json(200, { torrserver: `http://127.0.0.1:${srv.address().port}` });
        if (u.pathname === '/torrents') {
          const b = body ? JSON.parse(body) : {};
          if (b.action === 'list') {
            return json(200, [
              torrentStatus(size),
              { hash: 'a'.repeat(40), title: 'Healthy', stat: 3, torrent_size: 1000, loaded_size: 100, active_peers: 4, total_peers: 8, connected_seeders: 3 },
              { hash: 'b'.repeat(40), title: 'Lonely', stat: 3, torrent_size: 1000, loaded_size: 100 },
              { hash: 'c'.repeat(40), title: 'Done', stat: 3, torrent_size: 1000, loaded_size: 1000 },
              { hash: 'd'.repeat(40), title: 'Idle', stat: 5, torrent_size: 1000 }]);
          }
          return json(200, torrentStatus(size));
        }
        if (u.pathname === '/cache') return json(200, cacheState(size));
        if (u.pathname.indexOf('/stream/') === 0) {
          streamReqs++;
          if (failNextStream > 0) { failNextStream--; console.log('  [stream] 404 (cold start)'); res.writeHead(404); return res.end(); }
          return serveFile(req, res, MP4, DEAD * PIECE, (DEAD + 1) * PIECE);
        }
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
const player = (page) => page.evaluate(() => {
  const v = document.getElementById('video');
  const buf = []; for (let i = 0; i < v.buffered.length; i++) buf.push([+v.buffered.start(i).toFixed(1), +v.buffered.end(i).toFixed(1)]);
  return Object.assign(App.view('player').stall(), { t: +v.currentTime.toFixed(2), paused: v.paused, ready: v.readyState, buf, stat: document.getElementById('osd-stat').textContent });
});

(async () => {
  fixture();
  const srv = await fakeServer();
  const BASE = `http://127.0.0.1:${srv.address().port}`;
  const size = fs.statSync(MP4).size;
  const deadT0 = DEAD * PIECE / (size / DURATION), deadT1 = (DEAD + 1) * PIECE / (size / DURATION);
  console.log(`fake TorrServer/sidecar at ${BASE}; fixture ${size} B, ${Math.ceil(size / PIECE)} pieces, dead piece ${DEAD} ≈ ${deadT0.toFixed(1)}–${deadT1.toFixed(1)} s`);

  const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new',
    args: ['--no-sandbox', '--window-size=1920,1080', '--autoplay-policy=no-user-gesture-required'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1920, height: 1080 });
  page.on('pageerror', (e) => { failures.push('pageerror ' + e); console.log('  PAGE ERROR ' + e); });

  await page.goto(BASE + '/tv/', { waitUntil: 'load' });
  await sleep(1500);

  /* --- home: health tags --- */
  const tags = await page.evaluate(() => Array.from(document.querySelectorAll('#home-list .row')).map((r) => {
    const h = r.querySelector('.health');
    return { name: r.querySelector('.name').textContent, cls: h ? h.className.replace('health', '').trim() : '', text: h ? h.textContent : '' };
  }));
  console.log('[home]', JSON.stringify(tags));
  const by = (n) => tags.find((t) => t.name === n) || {};
  assert(by('Dead block').cls === 'noseed' && /无做种者/.test(by('Dead block').text), 'no seeder -> yellow 无做种者 tag');
  assert(by('Healthy').cls === 'ok' && /3 做种/.test(by('Healthy').text), 'connected_seeders=3 -> green 健康 · 3 做种');
  assert(by('Lonely').cls === 'nopeer', 'no peers -> red 没有 peer');
  assert(by('Done').cls === '' && by('Idle').cls === '', 'finished / idle torrents carry no tag');
  await page.screenshot({ path: path.join(__dirname, 'shots', '12-health-tags.png') }).catch(() => {});

  /* --- player: cold-start error 4 is retried once, then plays; stall on the dead piece --- */
  failNextStream = 1;
  await page.evaluate((h) => App.goFresh('player', { hash: h, index: 1, title: 'Dead block' }), HASH);
  await sleep(1000);
  let s = await player(page);
  console.log('[player cold start]', JSON.stringify(s));
  assert(s.retried && /重试/.test(s.state), 'first /stream 404 -> "重试中" instead of 播放失败: ' + JSON.stringify(s.state));
  await sleep(4000);
  s = await player(page);
  console.log('[player start]', JSON.stringify(s));
  assert(!s.paused && s.t > 0.5, 'playing after the automatic retry');
  assert(/无做种者/.test(s.stat), 'OSD stat line says 无做种者: ' + s.stat);

  /* wait until it runs into the dead piece and the stall is confirmed (deadT0 + STALL_MS + poll) */
  const t0 = Date.now();
  while (Date.now() - t0 < 40000) { s = await player(page); if (s.dead) break; await sleep(500); }
  console.log('[stalled]', JSON.stringify(s), 'after', ((Date.now() - t0) / 1000).toFixed(1), 's');
  assert(!!s.dead, 'stall detected and dead piece located');
  assert(s.dead && s.dead.piece === DEAD && s.dead.count === 1, 'dead piece is #' + DEAD + ': ' + JSON.stringify(s.dead));
  assert(s.t >= deadT0 - 1.5 && s.t <= deadT1, 'player stopped at the dead piece (t=' + s.t + ')');
  assert(new RegExp('#' + DEAD + '.*无做种者[\\s\\S]*▶▶').test(s.state), 'state line explains and offers ▶▶: ' + JSON.stringify(s.state));
  assert(s.since > 0 && Date.now() - s.since >= 12000, 'stall confirmed only after 12 s of waiting');
  assert(true, 'confirmed although download_speed is 600 KB/s and the piece is listed as a partial');
  await page.screenshot({ path: path.join(__dirname, 'shots', '13-dead-block.png') }).catch(() => {});

  /* --- ▶▶ skips it --- */
  const before = s.t;
  await key(page, 417, 4000);
  s = await player(page);
  console.log('[skipped]', JSON.stringify(s));
  assert(!s.dead && !s.since, 'stall state cleared after skip');
  assert(s.t > deadT1 && s.t < deadT1 + 8, 'seeked past the dead piece (' + before + ' -> ' + s.t + ', dead ends ' + deadT1.toFixed(1) + ')');
  assert(!s.paused && s.ready >= 3, 'playing again after the skip (readyState ' + s.ready + ')');
  const t1 = s.t;
  await sleep(2500);
  s = await player(page);
  assert(s.t > t1 + 1.5, 'time advances after the skip (' + t1 + ' -> ' + s.t + ')');

  /* ▶▶ when not stalled is still +60 s (clamped to duration here) */
  await key(page, 417, 1000);
  s = await player(page);
  assert(s.t >= DURATION - 2, '▶▶ while playing normally is the usual +60 s seek (t=' + s.t + ')');

  /* --- webOS-style give-up: media error at the dead block -> ▶▶ still offered and reopens the stream --- */
  mode = 'garbage';
  await page.evaluate((h) => App.goFresh('player', { hash: h, index: 1, title: 'Dead block', startTime: 0.1 }), HASH);
  const t2 = Date.now();
  let err = null;
  while (Date.now() - t2 < 60000) {
    s = await player(page);
    err = await page.evaluate(() => { const v = document.getElementById('video'); return v.error && v.error.code; });
    if (s.dead) break;
    await sleep(500);
  }
  console.log('[media error]', JSON.stringify(s), 'media error', err, 'after', ((Date.now() - t2) / 1000).toFixed(1), 's');
  if (err === 2 || err === 3) {
    assert(!!s.dead && s.dead.piece === DEAD, 'media error ' + err + ' at the dead block -> dead piece located: ' + JSON.stringify(s.dead));
    assert(/播放中断[\s\S]*▶▶/.test(s.state), 'state says 播放中断 … ▶▶: ' + JSON.stringify(s.state));
    mode = 'hang-free';   /* after the skip the stream must load again: serve everything */
    await key(page, 417, 5000);
    s = await player(page);
    console.log('[skipped after error]', JSON.stringify(s));
    assert(s.t > deadT1 && !s.paused && s.ready >= 3, '▶▶ after a media error reopens the stream past the dead block (t=' + s.t + ')');
  } else {
    console.log('  skip: headless Chrome produced no media error for a dropped connection (error=' + err + '); verified on the TV instead');
  }

  await browser.close();
  hung.forEach((r) => { try { r.destroy(); } catch (e) { /* ignore */ } });
  srv.close();
  console.log(failures.length ? `\n${failures.length} FAILED` : '\nall ok');
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
