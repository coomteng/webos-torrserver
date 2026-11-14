/* Headless end-to-end check of background pre-caching on the phone page: real sidecar binary +
 * fake TorrServer + puppeteer.
 *   NODE_PATH=/tmp/cdp/node_modules node test/precache.js
 * The fake TorrServer answers add/list/get like the real one, holds GET /stream?...&preload&stat
 * open until the test "finishes" the download or a {"action":"preload_stop"} arrives (what the
 * fork does), and records both. Drives /r/<id>: paste -> 后台缓存（不播放） -> sidecar asks
 * TorrServer to preload the biggest video -> card shows 停止缓存 + 后台缓存中 -> 停止缓存 ->
 * TorrServer gets preload_stop -> 缓存 again -> total switch off stops it (item kept, label 待缓存)
 * -> on resumes -> download finishes -> 已完整缓存 -> sidecar restart re-sends unfinished items
 * -> per-file queue: two rows of the 文件 list queued, stop the waiting one, stop the running one. */
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
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'webos-torrserver-precache-'));
const GOBIN = process.env.GO || 'go';
const H1 = '2770fe270845674966e184be60ed1be0fe494f3a';
const H2 = 'ed0da850c273e3e15a819bdcbbf418bc85107ec8';
const H3 = '1234567890abcdef1234567890abcdef12345678';

const preloads = [];   // "hash/index"
const stops = [];      // hashes
const waiters = {};    // hash -> [res]
const loaded = {};     // hash -> loaded_size shown in list (also bytes_completed of the main file)
const stat = {};       // hash -> stat for list/get (2 while a preload is held open)
function torrentStatus(hash) {
  return { hash, title: 'T ' + hash.slice(0, 6), stat: stat[hash] || 3, stat_string: '', torrent_size: 1000, loaded_size: loaded[hash] || 0,
    file_stats: [{ id: 1, path: 'X/sample.mkv', length: 50, bytes_completed: 0 }, { id: 2, path: 'X/Main.Movie.2024.mkv', length: 900, bytes_completed: Math.min(900, Math.round((loaded[hash] || 0) * 0.9)) }, { id: 3, path: 'X/readme.txt', length: 1, bytes_completed: 0 }] };
}
function finishPreload(hash) {
  stat[hash] = 3; loaded[hash] = 1000;
  (waiters[hash] || []).forEach((res) => { res.writeHead(200, { 'Content-Type': 'application/json' }); res.end(JSON.stringify(torrentStatus(hash))); });
  waiters[hash] = [];
}
function fakeTorrServer() {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const u = new URL(req.url, 'http://x');
      let body = '';
      req.on('data', (d) => { body += d; });
      req.on('end', () => {
        const json = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' }); res.end(JSON.stringify(obj)); };
        if (req.method === 'OPTIONS') { res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': 'GET, POST, OPTIONS', 'Access-Control-Allow-Headers': 'Content-Type' }); return res.end(); }
        if (u.pathname === '/stream' && u.searchParams.has('preload')) {
          const hash = u.searchParams.get('link');
          preloads.push(hash + '/' + u.searchParams.get('index'));
          stat[hash] = 2; loaded[hash] = 370;
          (waiters[hash] = waiters[hash] || []).push(res);
          /* res 'close' = connection gone (sidecar cancelled); req 'close' would fire as soon as the body is read */
          res.on('close', () => { waiters[hash] = (waiters[hash] || []).filter((r) => r !== res); });
          return;
        }
        if (u.pathname === '/torrents') {
          const b = body ? JSON.parse(body) : {};
          if (b.action === 'list') return json(200, Object.keys(loaded).map(torrentStatus));
          if (b.action === 'get') return json(200, torrentStatus(b.hash));
          if (b.action === 'add') {
            const hash = (/([0-9a-f]{40})/.exec(b.link) || [])[1];
            if (!(hash in loaded)) loaded[hash] = 0;
            return json(200, torrentStatus(hash));
          }
          if (b.action === 'rem') { delete loaded[b.hash]; delete stat[b.hash]; return json(200, {}); }
          if (b.action === 'preload_stop') {
            stops.push(b.hash);
            const had = (waiters[b.hash] || []).length > 0;
            stat[b.hash] = 3;
            (waiters[b.hash] || []).forEach((r) => { r.writeHead(200, { 'Content-Type': 'application/json' }); r.end(JSON.stringify(torrentStatus(b.hash))); });
            waiters[b.hash] = [];
            return json(200, { stopped: had });
          }
          return json(200, {});
        }
        res.writeHead(404); res.end();
      });
    });
    srv.listen(0, '127.0.0.1', () => resolve(srv));
  });
}
function getJSON(url) {
  return new Promise((resolve, reject) => {
    http.get(url, (r) => { let d = ''; r.on('data', (c) => { d += c; }); r.on('end', () => { try { resolve(JSON.parse(d)); } catch (e) { reject(e); } }); }).on('error', reject);
  });
}
function postJSON(url, obj) {
  return new Promise((resolve, reject) => {
    const u = new URL(url);
    const req = http.request({ hostname: u.hostname, port: u.port, path: u.pathname + u.search, method: 'POST', headers: { 'Content-Type': 'application/json' } }, (r) => {
      let d = ''; r.on('data', (c) => { d += c; }); r.on('end', () => { try { resolve(JSON.parse(d)); } catch (e) { reject(e); } });
    });
    req.on('error', reject); req.end(JSON.stringify(obj));
  });
}
async function until(fn, ms) { const t0 = Date.now(); while (Date.now() - t0 < ms) { if (await fn()) return true; await sleep(100); } return false; }

(async () => {
  const ts = await fakeTorrServer();
  const TS = `http://127.0.0.1:${ts.address().port}`;
  const bin = path.join(TMP, 'torr-remote');
  execFileSync(GOBIN, ['build', '-o', bin, './cmd/torr-remote'], { cwd: path.join(ROOT, 'sidecar'), env: Object.assign({}, process.env, { TMPDIR: process.env.TMPDIR || path.join(os.homedir(), '.cache/gotmp'), CGO_ENABLED: '0' }), stdio: 'inherit' });
  const PORT = 19000 + Math.floor(Math.random() * 1000);
  const DATA = path.join(TMP, 'data');
  const env = Object.assign({}, process.env, { TORR_SERVER: TS, TORR_PORT: String(PORT), TORR_LISTEN: '127.0.0.1', TORR_APP_DIR: path.join(ROOT, 'app'), TORR_DATA_DIR: DATA });
  let side = spawn(bin, [], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  side.stderr.on('data', (d) => process.stdout.write('  [sidecar] ' + d));
  const SIDE = `http://127.0.0.1:${PORT}`;
  for (let i = 0; i < 50; i++) { try { await getJSON(SIDE + '/health'); break; } catch (e) { await sleep(100); } }
  const ch = await postJSON(SIDE + '/remote/new', { id: 'pctest' });
  console.log(`fake TorrServer ${TS}, sidecar ${SIDE}, channel ${ch.id}`);

  const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new', args: ['--no-sandbox'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 400, height: 860, isMobile: true });
  page.on('pageerror', (e) => { failures.push('pageerror ' + e); console.log('  PAGE ERROR ' + e); });
  page.on('dialog', (d) => d.accept());
  await page.goto(SIDE + '/r/' + ch.id, { waitUntil: 'load' });
  await sleep(500);
  assert((await page.$eval('#pcsw', (e) => e.textContent)) === '后台缓存：开', 'switch shows 后台缓存：开 by default');

  /* --- paste + 后台缓存（不播放） --- */
  await page.type('#t', H1);
  await page.click('#bc');
  assert(await until(() => preloads.length === 1, 5000), 'sidecar asked TorrServer to preload: ' + JSON.stringify(preloads));
  assert(preloads[0] === H1 + '/2', 'biggest video file (index 2) is preloaded: ' + preloads[0]);
  const m1 = await page.$eval('#msg', (e) => e.textContent);
  assert(/已加入后台缓存：Main\.Movie/.test(m1), 'phone says 已加入后台缓存 with the file name: ' + m1);
  const poll = await getJSON(`${SIDE}/remote/${ch.id}/poll?since=${ch.seq}&wait=0`);
  const msgs = poll.msgs || poll.messages || [];
  assert(msgs.some((m) => m.action === 'added') && !msgs.some((m) => m.action === 'play'), 'TV only told "added", no play');

  const card = async () => page.evaluate(() => { const c = document.querySelector('.t'); if (!c) return null; const b = c.querySelector('.a .pc'); const l = c.querySelector('.s b.pc,.s b.pcdone,.s b.pcoff,.s b.pcerr'); return { btn: b && b.textContent, on: b && b.classList.contains('on'), label: l && l.textContent }; });
  assert(await until(async () => { const c = await card(); return c && c.btn === '停止缓存'; }, 6000), 'card button turns into 停止缓存');
  let c = await card();
  assert(c.on && /后台缓存中 · 37%/.test(c.label || ''), 'card label 后台缓存中 · 37%: ' + JSON.stringify(c));
  await page.screenshot({ path: path.join(__dirname, 'shots', '17-phone-precache.png'), fullPage: true }).catch(() => {});

  /* --- 停止缓存 --- */
  await page.evaluate(() => document.querySelector('.t .a .pc').click());
  assert(await until(() => stops.length === 1 && stops[0] === H1, 3000), 'TorrServer got preload_stop for H1');
  assert(await until(async () => { const c = await card(); return c && c.btn === '缓存' && !c.label; }, 6000), 'card back to 缓存, no label');
  let pc = await getJSON(SIDE + '/precache');
  assert(pc.items.length === 0, 'item removed from the sidecar list');
  await sleep(500);
  assert(preloads.length === 1, 'no preload restarted after stop: ' + preloads.length);

  /* --- 缓存 on the card again, then the total switch --- */
  await page.evaluate(() => document.querySelector('.t .a .pc').click());
  assert(await until(() => preloads.length === 2, 5000), 'card 缓存 starts a preload again');
  await page.click('#pcsw');
  assert(await until(() => stops.length === 2, 3000), 'switch off -> preload_stop');
  assert(await until(async () => (await page.$eval('#pcsw', (e) => e.textContent)) === '后台缓存：关', 3000), 'switch shows 后台缓存：关');
  assert(await until(async () => { const c = await card(); return c && /待缓存（总开关已关）/.test(c.label || ''); }, 6000), 'card label 待缓存 while switched off');
  pc = await getJSON(SIDE + '/precache');
  assert(!pc.enabled && pc.items.length === 1 && !pc.items[0].done, 'item kept, not done, switch off: ' + JSON.stringify(pc));
  await sleep(500);
  assert(preloads.length === 2, 'nothing restarted while off');

  /* --- add a second torrent while off: registered only --- */
  await page.type('#t', H2);
  await page.click('#bc');
  assert(await until(async () => (await getJSON(SIDE + '/precache')).items.length === 2, 5000), 'second item registered while off');
  await sleep(300);
  assert(preloads.length === 2, 'no preload for an item added while off');
  const m2 = await page.$eval('#msg', (e) => e.textContent);
  assert(/总开关已关/.test(m2), 'phone mentions the switch is off: ' + m2);

  /* --- switch on: both start; finish one -> 已完整缓存 --- */
  await page.click('#pcsw');
  assert(await until(() => preloads.length === 4, 5000), 'switch on -> both unfinished items preload: ' + JSON.stringify(preloads));
  finishPreload(H1);
  assert(await until(async () => (await getJSON(SIDE + '/precache')).items.some((i) => i.hash === H1 && i.done), 5000), 'H1 marked done when TorrServer returns stat 3');
  assert(await until(async () => page.evaluate((h) => { const cs = Array.from(document.querySelectorAll('.t')); const c = cs.find((x) => x.querySelector('.n').textContent.indexOf('T ' + h.slice(0, 6)) >= 0); const l = c && c.querySelector('.s b.pcdone'); return !!(l && /已完整缓存/.test(l.textContent)) && c.querySelector('.a .pc').textContent === '缓存'; }, H1), 6000), 'H1 card: 已完整缓存 label, button back to 缓存');
  assert(stops.length === 2, 'a normally finished preload is not stopped');

  /* --- sidecar restart: unfinished H2 re-sent (TorrServer still at stat 2 -> watched), done H1 not --- */
  side.kill();
  await sleep(300);
  const before = preloads.length;
  side = spawn(bin, [], { env, stdio: ['ignore', 'pipe', 'pipe'] });
  side.stderr.on('data', (d) => process.stdout.write('  [sidecar2] ' + d));
  for (let i = 0; i < 50; i++) { try { await getJSON(SIDE + '/health'); break; } catch (e) { await sleep(100); } }
  assert(await until(() => preloads.length === before + 1 && preloads[preloads.length - 1] === H2 + '/2', 5000), 'after restart only H2 is re-sent: ' + JSON.stringify(preloads.slice(before)));
  await sleep(500);
  assert(preloads.length === before + 1, 'done item H1 not re-sent');
  pc = await getJSON(SIDE + '/precache');
  assert(pc.enabled && pc.items.length === 2, 'list survived the restart (precache.json): ' + JSON.stringify(pc.items.map((i) => [i.hash.slice(0, 6), i.done])));

  /* --- delete from the phone page drops the precache entry too --- */
  await page.reload({ waitUntil: 'load' });
  await sleep(600);
  await page.evaluate((h) => { const cs = Array.from(document.querySelectorAll('.t')); const c = cs.find((x) => x.querySelector('.n').textContent.indexOf('T ' + h.slice(0, 6)) >= 0); c.querySelector('.a .del').click(); }, H2);
  assert(await until(async () => (await getJSON(SIDE + '/precache')).items.length === 1, 5000), 'delete removes the precache item');
  assert(await until(() => stops.indexOf(H2) >= 0, 3000), 'and stops its preload');

  /* --- per-file queue: 文件 list -> 缓存 on two rows -> one torrent entry, files in click order, one preload at a time --- */
  await postJSON(TS + '/torrents', { action: 'add', link: 'magnet:?xt=urn:btih:' + H3 });
  await page.reload({ waitUntil: 'load' });
  const h3card = (fn) => page.evaluate((h, src) => { const c = Array.from(document.querySelectorAll('.t')).find((x) => x.querySelector('.n').textContent.indexOf('T ' + h.slice(0, 6)) >= 0); return c ? (0, eval)('(' + src + ')')(c) : null; }, H3, fn.toString());
  const rowBtn = (name) => h3card(`function (c) { var r = Array.from(c.querySelectorAll('.files .f')).find(function (x) { return x.querySelector('.fn').textContent === '${name}'; }); var b = r && r.querySelector('button.pc'); return b ? b.textContent : null; }`);
  const rowClick = (name) => h3card(`function (c) { var r = Array.from(c.querySelectorAll('.files .f')).find(function (x) { return x.querySelector('.fn').textContent === '${name}'; }); r.querySelector('button.pc').click(); return true; }`);
  const h3 = async () => { const it = (await getJSON(SIDE + '/precache')).items.filter((i) => i.hash === H3); return { n: it.length, files: it.length ? (it[0].files || []).map((f) => f.index).join(',') : '', item: it[0] }; };
  const h3pre = () => preloads.filter((x) => x.indexOf(H3) === 0).map((x) => x.slice(41)).join(',');
  const h3stops = () => stops.filter((x) => x === H3).length;
  assert(await until(async () => (await h3card('function (c) { return true; }')) === true, 6000), 'H3 card listed');
  await h3card(`function (c) { c.querySelector('.a .cache').click(); return true; }`);
  assert(await until(async () => (await rowBtn('Main.Movie.2024.mkv')) === '缓存' && (await rowBtn('sample.mkv')) === '缓存', 6000), 'file list open: two video rows, both 缓存');
  await rowClick('Main.Movie.2024.mkv');
  assert(await until(async () => h3pre() === '2' && (await rowBtn('Main.Movie.2024.mkv')) === '停止', 6000), 'row 缓存 preloads that file, row button turns into 停止: ' + h3pre());
  await rowClick('sample.mkv');
  assert(await until(async () => (await h3()).files === '2,1' && (await rowBtn('sample.mkv')) === '停止', 6000), 'second row is queued behind the first: files ' + (await h3()).files);
  let q = await h3();
  assert(q.n === 1 && q.item.index === 2 && !q.item.done, 'still one items[] entry for the torrent, index = the file being cached: ' + JSON.stringify(q.item));
  await sleep(500);
  assert(h3pre() === '2' && (waiters[H3] || []).length === 1, 'one preload in flight, the queued file waits: ' + h3pre());
  assert(await until(async () => / · 0\/2$/.test((await h3card(`function (c) { var l = c.querySelector('.s b.pc'); return l ? l.textContent : ''; }`)) || ''), 6000), 'card label counts done/total files (· 0/2)');

  /* stop the waiting row: it leaves the queue, the running preload is not touched */
  await rowClick('sample.mkv');
  assert(await until(async () => (await h3()).files === '2' && (await rowBtn('sample.mkv')) === '缓存', 6000), 'stopping the waiting row drops it from the queue');
  await sleep(500);
  assert(h3stops() === 0 && h3pre() === '2' && (waiters[H3] || []).length === 1, 'no preload_stop for a waiting file, the running preload goes on: stops ' + h3stops());

  /* queue it again, then stop the running row: one preload_stop, the next file starts */
  await rowClick('sample.mkv');
  assert(await until(async () => (await h3()).files === '2,1', 6000), 'row queued again');
  await rowClick('Main.Movie.2024.mkv');
  assert(await until(() => h3pre() === '2,1', 6000), 'stopping the running row starts the next file: ' + h3pre());
  assert(await until(() => h3stops() === 1, 3000), 'exactly one preload_stop for the running file: ' + h3stops());
  q = await h3();
  assert(q.n === 1 && q.files === '1' && q.item.index === 1 && !q.item.done, 'queue is down to the next file: ' + JSON.stringify(q.item));
  assert(await until(async () => (await rowBtn('Main.Movie.2024.mkv')) === '缓存' && (await rowBtn('sample.mkv')) === '停止', 6000), 'rows show the stopped file as 缓存, the running one as 停止');
  await sleep(500);
  assert(h3stops() === 1 && h3pre() === '2,1' && (waiters[H3] || []).length === 1, 'nothing else stopped or restarted: stops ' + h3stops() + ', preloads ' + h3pre());

  await browser.close();
  side.kill();
  ts.close();
  console.log(failures.length ? `\n${failures.length} FAILED` : '\nall ok');
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
