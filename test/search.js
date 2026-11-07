/* Headless end-to-end check of torrent search on the phone page: real sidecar binary + fake TorrServer.
 *   NODE_PATH=/tmp/cdp/node_modules node test/search.js
 * The fake TorrServer answers GET /torznab/search/?query= like TorrServer's Torznab proxy (strings for
 * Seed/Peer/Size, one Link-only item, one item with neither link), records POST /torrents add links,
 * and returns metadata on the second add (as when TorrServer is still fetching it). Drives /r/<id>:
 * search -> rows sorted by seeders with colour classes -> 观看 on the top row -> TorrServer gets that
 * magnet -> the TV channel receives "play" for the biggest video. Then 缓存 on a Link-only row ->
 * TorrServer gets the Link and the TV only "added". Also: empty results and a non-array answer. */
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
const TMP = fs.mkdtempSync(path.join(os.tmpdir(), 'webos-torrserver-search-'));
const GOBIN = process.env.GO || 'go';
const H1 = '2770fe270845674966e184be60ed1be0fe494f3a', H2 = 'ed0da850c273e3e15a819bdcbbf418bc85107ec8', H3 = 'c2f364ba7f59b22a1845b16fd8a1fc1783568f8b';
const RESULTS = [
  { Title: 'Dune (2021) [1080p]', Size: '2.7 GiB', Seed: '5', Peer: '40', Tracker: '', Magnet: 'magnet:?xt=urn:btih:' + H2, Link: 'magnet:?xt=urn:btih:' + H2, Hash: H2 },
  { Title: 'Dune Part Two (2024) [2160p]', Size: '9.1 GiB', Seed: '1260', Peer: '232', Tracker: 'yts', Magnet: 'magnet:?xt=urn:btih:' + H1, Link: '', Hash: H1 },
  { Title: 'Dune Part Two (2024) [1080p] proxy link', Size: '3.1 GiB', Seed: '0', Peer: '3', Tracker: '', Magnet: '', Link: 'http://lodestarr.example/indexers/7/dl?link=abc', Hash: H3 },
  { Title: 'Dune 1984 dead', Size: '700 MiB', Seed: '0', Peer: '0', Tracker: '', Magnet: 'magnet:?xt=urn:btih:' + 'e'.repeat(40), Link: '', Hash: 'e'.repeat(40) },
  { Title: 'broken item without links', Size: '1 GiB', Seed: '99', Peer: '1', Magnet: '', Link: '' },
];

const added = [];
const addCount = {};
function fakeTorrServer() {
  return new Promise((resolve) => {
    const srv = http.createServer((req, res) => {
      const u = new URL(req.url, 'http://x');
      let body = '';
      req.on('data', (d) => { body += d; });
      req.on('end', () => {
        const json = (code, obj) => { res.writeHead(code, { 'Content-Type': 'application/json', 'Access-Control-Allow-Origin': '*' }); res.end(JSON.stringify(obj)); };
        if (req.method === 'OPTIONS') { res.writeHead(204, { 'Access-Control-Allow-Origin': '*', 'Access-Control-Allow-Methods': 'GET, POST, OPTIONS', 'Access-Control-Allow-Headers': 'Content-Type' }); return res.end(); }
        if (u.pathname === '/torznab/search/') {
          const q = u.searchParams.get('query') || '';
          if (q === 'nothing') return json(200, []);
          if (q === 'off') return json(200, null);
          return json(200, RESULTS);
        }
        if (u.pathname === '/torrents') {
          const b = body ? JSON.parse(body) : {};
          if (b.action === 'list') return json(200, []);
          if (b.action === 'add') {
            added.push(b.link);
            const hash = b.link.indexOf('lodestarr') >= 0 ? H3 : (/([0-9a-f]{40})/.exec(b.link) || [])[1];   /* magnet, or bare hash from waitInfo */
            addCount[hash] = (addCount[hash] || 0) + 1;
            const st = { hash, title: 'T ' + hash.slice(0, 6), stat: 3 };
            if (addCount[hash] >= 2) st.file_stats = [{ id: 1, path: 'Dune/sample.mkv', length: 50 }, { id: 2, path: 'Dune/Dune.Part.Two.2160p.mkv', length: 9000 }, { id: 3, path: 'Dune/readme.txt', length: 1 }];
            return json(200, st);
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

(async () => {
  const ts = await fakeTorrServer();
  const TS = `http://127.0.0.1:${ts.address().port}`;
  const bin = path.join(TMP, 'torr-remote');
  execFileSync(GOBIN, ['build', '-o', bin, '.'], { cwd: path.join(ROOT, 'sidecar'), env: Object.assign({}, process.env, { TMPDIR: process.env.TMPDIR || path.join(os.homedir(), '.cache/gotmp'), CGO_ENABLED: '0' }), stdio: 'inherit' });
  const PORT = 18000 + Math.floor(Math.random() * 1000);
  const side = spawn(bin, [], { env: Object.assign({}, process.env, { TORR_SERVER: TS, TORR_PORT: String(PORT), TORR_LISTEN: '127.0.0.1' }), stdio: ['ignore', 'pipe', 'pipe'] });
  side.stderr.on('data', (d) => process.stdout.write('  [sidecar] ' + d));
  const SIDE = `http://127.0.0.1:${PORT}`;
  for (let i = 0; i < 50; i++) { try { await getJSON(SIDE + '/health'); break; } catch (e) { await sleep(100); } }

  const ch = await postJSON(SIDE + '/remote/new', { id: 'srchtest' });
  const since = ch.seq;
  console.log(`fake TorrServer ${TS}, sidecar ${SIDE}, channel ${ch.id}`);

  const browser = await puppeteer.launch({ executablePath: process.env.CHROME || '/usr/bin/google-chrome', headless: 'new', args: ['--no-sandbox'] });
  const page = await browser.newPage();
  await page.setViewport({ width: 400, height: 860, isMobile: true });
  page.on('pageerror', (e) => { failures.push('pageerror ' + e); console.log('  PAGE ERROR ' + e); });
  page.on('dialog', (d) => d.accept());
  await page.goto(SIDE + '/r/' + ch.id, { waitUntil: 'load' });

  /* --- search --- */
  await page.click('#srch summary');
  await page.type('#ts-q', 'dune');
  await page.keyboard.press('Enter');
  await page.waitForFunction(() => document.querySelectorAll('#sr .sr').length > 0, { timeout: 5000 }).catch(() => {});
  const rows = await page.evaluate(() => Array.from(document.querySelectorAll('#sr .sr')).map((r) => ({ n: r.querySelector('.n').textContent, cls: r.querySelector('.s b').className, s: r.querySelector('.s').textContent })));
  const sm = await page.$eval('#sm', (e) => e.textContent);
  console.log('[results]', JSON.stringify(rows), sm);
  assert(rows.length === 4, 'items without Magnet/Link are dropped: ' + rows.length + ' rows');
  assert(rows[0] && /2160p/.test(rows[0].n) && rows[1] && /2021/.test(rows[1].n), 'sorted by seeders (string Seed parsed as number)');
  assert(rows[0] && rows[0].cls === 'ok' && rows[1].cls === 'ok', 'any seeder -> green 健康 tag');
  assert(rows[2] && rows[2].cls === 'noseed' && /无做种者 · 可能下不完/.test(rows[2].s), 'seed 0, peers 3 -> yellow 无做种者 · 可能下不完: ' + (rows[2] && rows[2].s));
  assert(rows[3] && rows[3].cls === 'nopeer' && /没有 peer/.test(rows[3].s), 'seed 0, peer 0 -> red 没有 peer: ' + (rows[3] && rows[3].s));
  assert(rows[0] && /健康 · 1260 做种.*232 下载 · 9\.1 GiB · yts/.test(rows[0].s), 'meta line: health · seeders / leechers / size / tracker: ' + (rows[0] && rows[0].s));
  assert(/共 4 条.*健康度/.test(sm), 'count message mentions health source: ' + sm);
  await page.screenshot({ path: path.join(__dirname, 'shots', '16-phone-search.png'), fullPage: true }).catch(() => {});

  /* --- 观看 on the top row: magnet to TorrServer, play to the TV --- */
  await page.evaluate(() => document.querySelectorAll('#sr .sr')[0].querySelector('.watch').click());
  let poll = null;
  for (let i = 0; i < 40; i++) {
    poll = await getJSON(`${SIDE}/remote/${ch.id}/poll?since=${since}&wait=0`);
    if ((poll.msgs || poll.messages || []).some((m) => m.action === 'play')) break;
    await sleep(250);
  }
  const msgs = (poll && (poll.msgs || poll.messages)) || [];
  console.log('[tv msgs]', JSON.stringify(msgs), 'added', JSON.stringify(added));
  assert(added[0] === 'magnet:?xt=urn:btih:' + H1, 'TorrServer got the Magnet of the picked row (Link was empty)');
  const play = msgs.find((m) => m.action === 'play');
  assert(play && play.hash === H1 && play.index === 2, 'TV got play for the biggest video file: ' + JSON.stringify(play));
  const msg = await page.$eval('#msg', (e) => e.textContent);
  assert(/已发送到电视/.test(msg), 'phone says 已发送到电视: ' + msg);

  /* --- 缓存 on a Link-only row: Link to TorrServer, no play --- */
  const lastSeq = msgs.length ? msgs[msgs.length - 1].seq : since;
  added.length = 0;
  await page.click('#srch summary').catch(() => {});
  const open = await page.$eval('#srch', (e) => e.open);
  if (!open) await page.click('#srch summary');
  await page.evaluate(() => document.querySelectorAll('#sr .sr')[2].querySelector('.cache').click());
  await sleep(1000);
  const poll2 = await getJSON(`${SIDE}/remote/${ch.id}/poll?since=${lastSeq}&wait=0`);
  const msgs2 = poll2.msgs || poll2.messages || [];
  console.log('[cache]', JSON.stringify(added), JSON.stringify(msgs2));
  assert(added[0] === 'http://lodestarr.example/indexers/7/dl?link=abc', 'Link-only row: TorrServer got the Link');
  assert(msgs2.some((m) => m.action === 'added') && !msgs2.some((m) => m.action === 'play'), 'cache only tells the TV "added"');

  /* --- empty and disabled --- */
  await page.evaluate(() => { document.getElementById('ts-q').value = 'nothing'; tsSearch(); });
  await sleep(800);
  let s2 = await page.$eval('#sm', (e) => e.textContent);
  assert(/没有找到「nothing」/.test(s2) && (await page.$$('#sr .sr')).length === 0, 'empty result message: ' + s2);
  await page.evaluate(() => { document.getElementById('ts-q').value = 'off'; tsSearch(); });
  await sleep(800);
  s2 = await page.$eval('#sm', (e) => e.textContent);
  assert(/未开启搜索/.test(s2), 'non-array answer -> 服务端未开启搜索: ' + s2);

  await browser.close();
  side.kill();
  ts.close();
  console.log(failures.length ? `\n${failures.length} FAILED` : '\nall ok');
  process.exit(failures.length ? 1 : 0);
})().catch((e) => { console.error(e); process.exit(1); });
