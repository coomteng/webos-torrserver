/* Headless end-to-end walk of the TV app against a live sidecar + TorrServer with puppeteer-core.
 *   SIDECAR=http://127.0.0.1:8792 NODE_PATH=/tmp/cdp/node_modules node test/walk.js
 * Needs TorrServer to have at least one torrent with a video file (the walk adds Sintel if empty).
 * Screenshots go to test/shots/. Exits non-zero on a failed assertion or page error. */
const path = require('path');
const fs = require('fs');
const puppeteer = require('puppeteer-core');

const SIDECAR = process.env.SIDECAR || 'http://127.0.0.1:8792';
const OUT = path.join(__dirname, 'shots');
fs.mkdirSync(OUT, { recursive: true });
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));
const failures = [];
const pageErrors = [];
function assert(cond, msg) { if (!cond) { failures.push(msg); console.log('  FAIL ' + msg); } else { console.log('  ok   ' + msg); } }

async function info(page) {
  return page.evaluate(() => {
    const el = document.querySelector('.focus');
    const r = el && el.getBoundingClientRect();
    return { view: (document.querySelector('.view.active') || {}).id, stack: App.stackNames(),
      focus: el ? { cls: el.className, text: (el.textContent || '').slice(0, 40), x: Math.round(r.left), y: Math.round(r.top), w: Math.round(r.width), h: Math.round(r.height) } : null,
      remoteId: App.Remote.id(), rows: document.querySelectorAll('#home-list .row').length, files: document.querySelectorAll('#files-list .file').length,
      state: (document.getElementById('osd-state') || {}).textContent, toast: (document.getElementById('toast') || {}).textContent,
      videoSrc: (document.getElementById('video') || {}).currentSrc || (document.getElementById('video') || {}).src };
  });
}
async function shot(page, name) { await page.screenshot({ path: path.join(OUT, name + '.png') }); }
async function key(page, code, wait) {
  await page.evaluate((c) => {
    const ev = new KeyboardEvent('keydown', { bubbles: true, cancelable: true });
    Object.defineProperty(ev, 'keyCode', { get: () => c }); Object.defineProperty(ev, 'which', { get: () => c });
    document.dispatchEvent(ev);
    const up = new KeyboardEvent('keyup', { bubbles: true }); Object.defineProperty(up, 'keyCode', { get: () => c }); document.dispatchEvent(up);
  }, code);
  await sleep(wait || 300);
}
async function post(url, body) {
  const r = await fetch(url, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body) });
  const t = await r.text();
  return { status: r.status, json: t ? JSON.parse(t) : null };
}

(async () => {
  const cfg = await (await fetch(SIDECAR + '/config')).json();
  const TS = cfg.torrserver;
  console.log('sidecar', SIDECAR, 'torrserver', TS);

  // make sure a video torrent exists
  let list = (await post(TS + '/torrents', { action: 'list' })).json || [];
  if (!list.length) {
    await post(TS + '/torrents', { action: 'add', link: 'magnet:?xt=urn:btih:08ada5a7a6183aae1e09d831df6748d566095a10&dn=Sintel', title: 'Sintel', save_to_db: true });
    for (let i = 0; i < 30; i++) { await sleep(2000); list = (await post(TS + '/torrents', { action: 'list' })).json || []; if (list[0] && list[0].file_stats) break; }
  }
  let sintel = list.find((t) => /sintel/i.test(t.title || t.name || '')) || list[0];
  if (!(sintel.file_stats && sintel.file_stats.length)) sintel = (await post(TS + '/torrents', { action: 'add', link: sintel.hash, save_to_db: true })).json;
  assert(sintel && sintel.file_stats && sintel.file_stats.length, 'TorrServer has a torrent with files: ' + (sintel && sintel.title));
  const mp4 = sintel.file_stats.filter((f) => /\.mp4$/i.test(f.path)).sort((a, b) => b.length - a.length)[0];

  const browser = await puppeteer.launch({ executablePath: '/usr/bin/google-chrome', headless: 'new',
    args: ['--no-sandbox', '--window-size=1920,1080', '--autoplay-policy=no-user-gesture-required', '--mute-audio'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 1920, height: 1080 });
  page.on('pageerror', (e) => { pageErrors.push(String(e)); console.log('  PAGE ERROR ' + e); });
  page.on('console', (m) => { if (m.type() === 'error') console.log('  console.error ' + m.text()); });

  await page.goto(SIDECAR + '/tv/', { waitUntil: 'load' });
  await sleep(2500);
  let i = await info(page);
  await shot(page, '01-home');
  console.log('[home]', JSON.stringify({ view: i.view, rows: i.rows, remoteId: i.remoteId, focus: i.focus }));
  assert(i.view === 'home', 'home view active');
  assert(i.remoteId && i.remoteId.length === 6, 'phone channel registered: ' + i.remoteId);
  assert(i.rows === list.length, 'home shows ' + list.length + ' torrents (got ' + i.rows + ')');
  assert(i.focus && i.focus.cls.indexOf('row') >= 0, 'a torrent row is focused');
  const qr = await page.evaluate(() => ({ src: document.getElementById('home-qr').src, w: document.getElementById('home-qr').naturalWidth }));
  assert(/\/remote\/.*\/qr\.svg/.test(qr.src) && qr.w > 0, 'QR image loaded ' + qr.src);
  const phone = await (await fetch(SIDECAR + '/r/' + i.remoteId)).text();
  assert(phone.indexOf('var ID="' + i.remoteId + '"') > 0 && phone.indexOf('TS="' + TS + '"') > 0, 'phone page carries id + TorrServer URL');

  // focus Sintel row, OK -> files
  const rows = await page.$$eval('#home-list .row', (els) => els.map((e) => e.getAttribute('data-hash')));
  const idx = rows.indexOf(sintel.hash);
  for (let k = 0; k < idx; k++) await key(page, 40);
  await key(page, 13, 1500);
  i = await info(page);
  await shot(page, '02-files');
  console.log('[files]', JSON.stringify({ view: i.view, files: i.files, focus: i.focus }));
  assert(i.view === 'files', 'OK opens the file list');
  assert(i.files >= 1, 'file list shows video files (' + i.files + ')');

  // OK -> player
  await key(page, 13, 4000);
  i = await info(page);
  await shot(page, '03-player');
  console.log('[player]', JSON.stringify({ view: i.view, state: i.state, src: i.videoSrc }));
  assert(i.view === 'player', 'OK on a file opens the player');
  assert(i.videoSrc.indexOf(TS + '/stream/') === 0 && /&index=\d+&play/.test(i.videoSrc), 'video src is a TorrServer /stream URL: ' + i.videoSrc);
  let v = await page.evaluate(() => { const v = document.getElementById('video'); return { t: v.currentTime, d: v.duration, paused: v.paused, err: v.error && v.error.code, rs: v.readyState }; });
  console.log('  video', JSON.stringify(v));
  assert(!v.err, 'no video error (desktop Chrome, mp4)');
  assert(v.d > 0, 'duration known: ' + v.d);
  // seek +10 twice
  await key(page, 39, 200); await key(page, 39, 1500);
  v = await page.evaluate(() => { const v = document.getElementById('video'); return { t: v.currentTime, paused: v.paused, err: v.error && v.error.code }; });
  assert(v.t >= 15, 'seek forward worked: t=' + v.t.toFixed(1));
  await shot(page, '04-player-seek');
  // back -> files -> back -> home
  await key(page, 461, 800);
  i = await info(page);
  assert(i.view === 'files', 'Back from player returns to files');
  await key(page, 461, 800);
  i = await info(page);
  assert(i.view === 'home', 'Back from files returns to home');

  // phone "play" message jumps into the player from home
  const r = await post(SIDECAR + '/remote/' + i.remoteId + '/send', { action: 'play', hash: sintel.hash, index: mp4.id, title: sintel.title });
  assert(r.status === 200, 'phone send accepted');
  await sleep(4000);
  i = await info(page);
  await shot(page, '05-phone-play');
  console.log('[phone-play]', JSON.stringify({ view: i.view, stack: i.stack, state: i.state, src: i.videoSrc }));
  assert(i.view === 'player', 'phone play message opens the player');
  assert(i.stack.join('>') === 'home>files>player', 'stack is home>files>player: ' + i.stack.join('>'));
  v = await page.evaluate(() => { const v = document.getElementById('video'); return { t: v.currentTime, d: v.duration, err: v.error && v.error.code }; });
  assert(v.d > 0 && !v.err, 'phone-started playback has duration and no error');
  // state reported to sidecar
  const st = await (await fetch(SIDECAR + '/remote/' + i.remoteId + '/state')).json();
  assert(st.view === 'player' && st.hash === sintel.hash, 'TV reported player state to the sidecar: ' + JSON.stringify(st));
  // resume position saved after leaving
  await page.evaluate(() => { document.getElementById('video').currentTime = 120; });
  await sleep(800);
  await key(page, 461, 800);
  const pos = await page.evaluate((h) => JSON.parse(localStorage.getItem('torr.pos.' + h) || '{}'), sintel.hash);
  assert(pos[mp4.id] >= 100, 'resume position saved in localStorage: ' + JSON.stringify(pos));
  i = await info(page);
  await shot(page, '06-files-resume');
  const resumeText = await page.$eval('#files-list', (el) => el.textContent);
  assert(/看到/.test(resumeText), 'file list shows the resume hint');

  // "added" message refreshes; big QR via blue key on home
  await key(page, 461, 800);
  await key(page, 406, 500);
  const big = await page.evaluate(() => getComputedStyle(document.getElementById('home-big')).display);
  await shot(page, '07-big-qr');
  assert(big === 'block', 'blue key shows the big QR');
  await key(page, 461, 500);
  i = await info(page);
  assert(i.view === 'home' && (await page.evaluate(() => getComputedStyle(document.getElementById('home-big')).display)) === 'none', 'Back closes the big QR and stays on home');

  await browser.close();
  console.log('\n' + (failures.length ? failures.length + ' FAILURES' : 'ALL OK') + (pageErrors.length ? ', ' + pageErrors.length + ' page errors' : ''));
  process.exit(failures.length || pageErrors.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(2); });
