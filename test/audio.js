/* Headless check of audio-track switching in the player (yellow key / #osd-audio badge).
 *   NODE_PATH=/tmp/cdp/node_modules node test/audio.js
 * Self-contained: ffmpeg synthesises a 2-audio-track MKV (eng = 440 Hz sine, chi = 1600 Hz sine), a
 * tiny Node server plays TorrServer + sidecar (/config, /torrents, /stream with HTTP ranges, /tv/),
 * and Chrome runs with --enable-blink-features=AudioVideoTracks (desktop Chrome hides
 * HTMLMediaElement.audioTracks behind that flag; webOS >= 4 exposes it by default). A WebAudio
 * analyser on the <video> proves the audible track really changes, not just the `enabled` bits. */
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
const TMP = path.join(os.tmpdir(), 'webos-torrserver-audio');
const MKV = path.join(TMP, 'two.mkv');
const HASH = '0123456789abcdef0123456789abcdef01234567';

function fixture() {
  if (fs.existsSync(MKV)) return;
  fs.mkdirSync(TMP, { recursive: true });
  execFileSync('ffmpeg', ['-y', '-v', 'error',
    '-f', 'lavfi', '-i', 'testsrc2=size=320x180:rate=25',
    '-f', 'lavfi', '-i', 'sine=frequency=440:sample_rate=48000',
    '-f', 'lavfi', '-i', 'sine=frequency=1600:sample_rate=48000',
    '-t', '40', '-map', '0:v', '-map', '1:a', '-map', '2:a',
    '-c:v', 'libx264', '-preset', 'veryfast', '-pix_fmt', 'yuv420p', '-c:a', 'aac', '-b:a', '96k',
    '-metadata:s:a:0', 'language=eng', '-metadata:s:a:0', 'title=English',
    '-metadata:s:a:1', 'language=chi', '-metadata:s:a:1', 'title=Chinese',
    '-disposition:a:0', 'default', MKV]);
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
          const t = { hash: HASH, title: 'Two tracks', stat: 3, torrent_size: fs.statSync(MKV).size, loaded_size: fs.statSync(MKV).size,
            file_stats: [{ id: 1, path: 'Two tracks/two.mkv', length: fs.statSync(MKV).size }] };
          return json(200, b.action === 'list' ? [t] : t);
        }
        if (u.pathname.indexOf('/stream/') === 0) return serveFile(req, res, MKV);
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
/* dominant frequency of what the <video> is actually outputting */
async function hz(page) {
  return page.evaluate(async () => {
    const v = document.getElementById('video');
    if (!window.__an) {
      const ctx = new AudioContext(); await ctx.resume();
      const src = ctx.createMediaElementSource(v); const an = ctx.createAnalyser(); an.fftSize = 4096;
      src.connect(an); an.connect(ctx.destination);
      window.__an = an; window.__ctx = ctx;
      await new Promise((r) => setTimeout(r, 800));
    }
    const buf = new Float32Array(window.__an.frequencyBinCount); window.__an.getFloatFrequencyData(buf);
    let mi = 0; for (let i = 1; i < buf.length; i++) if (buf[i] > buf[mi]) mi = i;
    return Math.round(mi * window.__ctx.sampleRate / window.__an.fftSize);
  });
}
const near = (a, b) => Math.abs(a - b) < 40;
const osd = (page) => page.evaluate(() => ({
  badge: document.getElementById('osd-audio').textContent, hidden: /hidden/.test(document.getElementById('osd-audio').className),
  state: document.getElementById('osd-state').textContent, tracks: App.view('player').audioTracks(),
  t: document.getElementById('video').currentTime, paused: document.getElementById('video').paused, lang: localStorage.getItem('torr.lang') }));

(async () => {
  fixture();
  const srv = await fakeServer();
  const BASE = `http://127.0.0.1:${srv.address().port}`;
  console.log('fake TorrServer/sidecar at', BASE, 'fixture', MKV);

  const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new',
    args: ['--no-sandbox', '--window-size=1920,1080', '--autoplay-policy=no-user-gesture-required', '--enable-blink-features=AudioVideoTracks'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1920, height: 1080 });
  page.on('pageerror', (e) => { failures.push('pageerror ' + e); console.log('  PAGE ERROR ' + e); });

  await page.goto(BASE + '/tv/', { waitUntil: 'load' });
  await sleep(1500);
  await page.evaluate(() => localStorage.removeItem('torr.lang'));
  await page.evaluate((h) => App.goFresh('player', { hash: h, index: 1, title: 'Two tracks' }), HASH);
  await sleep(3000);

  let s = await osd(page);
  console.log('[player]', JSON.stringify(s));
  assert(s.tracks.length === 2, 'video exposes 2 audio tracks (' + s.tracks.length + ')');
  assert(!s.hidden && /音轨 1\/2/.test(s.badge) && /英语/.test(s.badge), 'OSD badge shows track 1/2 英语: ' + s.badge);
  assert(!s.paused && s.t > 0.5, 'playing');
  let f = await hz(page);
  assert(near(f, 440), 'default track audible at 440 Hz (got ' + f + ')');

  await key(page, 405, 1500);  // yellow
  s = await osd(page);
  assert(s.tracks[1].enabled && !s.tracks[0].enabled, 'yellow key enables track 2');
  assert(/音轨 2\/2/.test(s.badge) && /中文/.test(s.badge), 'badge now 2/2 中文: ' + s.badge);
  assert(/音轨：中文/.test(s.state), 'state line announces the track: ' + s.state);
  assert(s.lang === 'chi', 'language remembered in localStorage: ' + s.lang);
  f = await hz(page);
  assert(near(f, 1600), 'audible output switched to 1600 Hz (got ' + f + ')');
  assert(!s.paused, 'still playing after switch');

  await key(page, 405, 1500);  // wraps around
  s = await osd(page);
  f = await hz(page);
  assert(s.tracks[0].enabled && near(f, 440), 'second press wraps back to track 1 (' + f + ' Hz)');

  // preference survives a reload of the file (next episode in a season pack)
  await key(page, 405, 800);
  await page.evaluate(() => App.view('player').leave());
  await page.evaluate((h) => App.goFresh('player', { hash: h, index: 1, title: 'Two tracks' }), HASH);
  await sleep(3000);
  s = await osd(page);
  f = await hz(page);
  assert(s.tracks[1].enabled && near(f, 1600), 'preferred language (chi) auto-selected on the next file (' + f + ' Hz)');

  // pointer: clicking the badge cycles too
  await page.evaluate(() => document.getElementById('osd-audio').click());
  await sleep(1200);
  s = await osd(page);
  assert(s.tracks[0].enabled, 'clicking the badge cycles the track');

  await page.screenshot({ path: path.join(__dirname, 'shots', '08-audio-track.png') }).catch(() => {});
  await browser.close();
  srv.close();
  console.log(failures.length ? `\n${failures.length} FAILED` : '\nall ok');
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
