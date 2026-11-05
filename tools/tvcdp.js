/* Minimal CDP client for the TV app via `ares-inspect` (webOS 4.4 = Chrome 53: Runtime.evaluate
 * without awaitPromise; poll window vars instead).
 *   node tools/tvcdp.js <ws-url> eval '<js expression>'
 *   node tools/tvcdp.js <ws-url> watch <seconds>        # dump app/video state every 2 s
 *   node tools/tvcdp.js <ws-url> key <keyCode>          # dispatch a keydown into the app
 *   node tools/tvcdp.js <ws-url> shot <out.png>         # Page.captureScreenshot
 * Requires NODE_PATH with `ws` (e.g. /tmp/cdp/node_modules). */
const WebSocket = require('ws');
const fs = require('fs');
const [wsUrl, cmd, arg] = process.argv.slice(2);
if (!wsUrl || !cmd) { console.error('usage: tvcdp.js <ws-url> eval|watch|key|shot <arg>'); process.exit(2); }

const ws = new WebSocket(wsUrl);
let id = 0;
const pending = {};
function send(method, params) {
  return new Promise((resolve, reject) => {
    const n = ++id;
    pending[n] = { resolve, reject };
    ws.send(JSON.stringify({ id: n, method, params: params || {} }));
  });
}
ws.on('message', (d) => {
  const m = JSON.parse(d);
  if (m.id && pending[m.id]) { const p = pending[m.id]; delete pending[m.id]; m.error ? p.reject(new Error(JSON.stringify(m.error))) : p.resolve(m.result); }
  else if (m.method === 'Runtime.consoleAPICalled') console.log('[tv console]', m.params.type, m.params.args.map((a) => a.value !== undefined ? a.value : a.description).join(' '));
  else if (m.method === 'Runtime.exceptionThrown') console.log('[tv exception]', JSON.stringify(m.params.exceptionDetails).slice(0, 400));
});
async function evaluate(expr) {
  const r = await send('Runtime.evaluate', { expression: expr, returnByValue: true });
  if (r.exceptionDetails) throw new Error('eval: ' + JSON.stringify(r.exceptionDetails).slice(0, 300));
  return r.result.value;
}
const STATE = `(function(){var v=document.getElementById('video');var f=document.querySelector('.focus');return JSON.stringify({view:(document.querySelector('.view.active')||{}).id,stack:App.stackNames(),remote:App.Remote.id(),rows:document.querySelectorAll('#home-list .row').length,files:document.querySelectorAll('#files-list .file').length,focus:f?f.className+' '+(f.textContent||'').slice(0,40):null,state:document.getElementById('osd-state').textContent,toast:document.getElementById('toast').textContent,status:document.getElementById('home-status').textContent,stat:document.getElementById('osd-stat').textContent,video:{src:(v.currentSrc||v.src||'').slice(0,120),t:+v.currentTime.toFixed(1),d:+(v.duration||0).toFixed(1),paused:v.paused,rs:v.readyState,ns:v.networkState,err:v.error&&v.error.code,buf:v.buffered.length?+v.buffered.end(v.buffered.length-1).toFixed(1):0}})})()`;
ws.on('open', async () => {
  try {
    await send('Runtime.enable');
    if (cmd === 'eval') {
      console.log(JSON.stringify(await evaluate(arg)));
    } else if (cmd === 'state') {
      console.log(await evaluate(STATE));
    } else if (cmd === 'watch') {
      const until = Date.now() + (parseInt(arg || '20', 10)) * 1000;
      while (Date.now() < until) { console.log(new Date().toISOString().slice(11, 19), await evaluate(STATE)); await new Promise((r) => setTimeout(r, 2000)); }
    } else if (cmd === 'key') {
      await evaluate(`(function(){var c=${parseInt(arg, 10)};var e=document.createEvent('Event');e.initEvent('keydown',true,true);e.keyCode=c;e.which=c;document.dispatchEvent(e);var u=document.createEvent('Event');u.initEvent('keyup',true,true);u.keyCode=c;u.which=c;document.dispatchEvent(u);return 1})()`);
      console.log('key', arg, 'sent');
    } else if (cmd === 'shot') {
      const r = await send('Page.captureScreenshot', { format: 'jpeg', quality: 50 });
      fs.writeFileSync(arg, Buffer.from(r.data, 'base64'));
      console.log('saved', arg);
    }
  } catch (e) { console.error(e.message); process.exitCode = 1; }
  ws.close();
});
ws.on('error', (e) => { console.error('ws error', e.message); process.exit(1); });
