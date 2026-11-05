package main

// phonePage is served at /r/<id>. It talks to TorrServer directly (open CORS) for add / list /
// remove and only uses the sidecar channel to tell the TV what to play.
const phonePage = `<!DOCTYPE html><html lang="zh"><head><meta charset="utf-8">
<meta name="viewport" content="width=device-width,initial-scale=1,viewport-fit=cover"><title>电视磁力</title>
<style>
:root{color-scheme:dark}
body{font-family:-apple-system,"Segoe UI","Noto Sans",sans-serif;background:#0f1117;color:#e8e8ec;margin:0;padding:18px 18px 40px}
h1{font-size:22px;color:#fad778;margin:0 0 4px}
#ctx{color:#8f94a3;font-size:13px;min-height:18px;margin-bottom:14px}
textarea{width:100%;box-sizing:border-box;font-size:16px;padding:12px;border-radius:12px;border:2px solid #2a2f40;background:#1a1d28;color:#fff;min-height:96px;outline:none;font-family:ui-monospace,Menlo,monospace}
textarea:focus{border-color:#fad778}
.btns{display:flex;gap:10px;margin-top:10px}
button{flex:1;font-size:18px;padding:14px 8px;border-radius:12px;border:0;font-weight:600}
button:disabled{opacity:.45}
.cache{background:#2a2f40;color:#e8e8ec}.watch{background:#fad778;color:#111}
.paste{background:#1a1d28;color:#8f94a3;font-weight:400;font-size:14px;padding:10px;margin-top:8px;border:1px dashed #2a2f40}
#msg{margin-top:10px;color:#8f94a3;font-size:14px;min-height:18px}
#msg.err{color:#ff8a80}
h2{font-size:15px;color:#8f94a3;margin:26px 0 8px;font-weight:600}
.t{background:#161925;border-radius:12px;padding:12px;margin-bottom:10px}
.t .n{font-size:16px;color:#fff;word-break:break-all;line-height:1.35}
.t .s{font-size:12px;color:#8f94a3;margin-top:4px}
.t .bar{height:4px;background:#2a2f40;border-radius:2px;margin-top:8px;overflow:hidden}
.t .bar i{display:block;height:100%;background:#22c55e;width:0}
.t .a{display:flex;gap:8px;margin-top:10px}
.t .a button{padding:9px 6px;font-size:14px}
.t .a .del{flex:0 0 auto;background:#3a1f22;color:#ff8a80;padding:9px 14px}
.files{margin-top:8px}
.f{display:flex;align-items:center;gap:8px;padding:8px 0;border-top:1px solid #1f2330;font-size:14px}
.f .fn{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:#d6d9e2}
.f .fs{color:#6d7282;font-size:12px;flex:none}
.f button{flex:none;background:#fad778;color:#111;padding:6px 12px;font-size:13px;border-radius:8px}
.empty{color:#4f5466;font-size:14px;padding:20px 0;text-align:center}
</style></head><body>
<h1>电视磁力</h1><div id="ctx">连接中…</div>
<textarea id="t" placeholder="粘贴 magnet:?xt=urn:btih:… 、40 位 infohash 或 .torrent 链接" autocapitalize="off" autocorrect="off" spellcheck="false"></textarea>
<button class="paste" id="pb" onclick="pasteIn()">从剪贴板粘贴</button>
<div class="btns"><button class="cache" id="bc" onclick="go(false)">缓存（不播放）</button><button class="watch" id="bw" onclick="go(true)">在电视上观看</button></div>
<div id="msg"></div>
<h2>电视上的种子</h2><div id="list"><div class="empty">加载中…</div></div>
<script>
var ID=__ID__, TS=__TS__;
var VIDEO=/\.(mkv|mp4|m4v|avi|mov|ts|m2ts|mpg|mpeg|webm|wmv|flv|vob|iso|3gp)$/i;
var $=function(i){return document.getElementById(i)};
var open={}; /* hash -> files expanded */
function msg(s,err){var m=$('msg');m.textContent=s||'';m.className=err?'err':''}
function fmtSize(b){if(!b)return'';var u=['B','KB','MB','GB','TB'],i=0;while(b>=1024&&i<4){b/=1024;i++}return(i>1?b.toFixed(1):Math.round(b))+' '+u[i]}
function ts(path,body){return fetch(TS+path,{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(r){if(!r.ok)return r.text().then(function(t){throw new Error('TorrServer '+r.status+' '+t.slice(0,120))});return r.text().then(function(t){return t?JSON.parse(t):null})})}
function tell(body){return fetch('/remote/'+ID+'/send',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify(body)}).then(function(r){if(r.status===404)throw new Error('电视未连接：请在电视上重新打开应用');if(!r.ok)throw new Error('通知电视失败 '+r.status);return r.json()})}
function normalize(s){
  s=(s||'').trim();
  if(!s)return'';
  if(/^[0-9a-f]{40}$/i.test(s))return'magnet:?xt=urn:btih:'+s.toLowerCase();
  var m=s.match(/magnet:\?[^\s"'<>]+/i);if(m)return m[0];
  if(/^https?:\/\//i.test(s))return s;
  return'';
}
function pasteIn(){if(!navigator.clipboard||!navigator.clipboard.readText){msg('此浏览器不支持读剪贴板，请长按输入框粘贴',1);return}navigator.clipboard.readText().then(function(t){$('t').value=t;msg('')},function(){msg('读取剪贴板被拒绝，请长按输入框粘贴',1)})}
function busy(b){$('bc').disabled=b;$('bw').disabled=b}
function videoFiles(st){var fs=(st&&st.file_stats)||[],out=[];for(var i=0;i<fs.length;i++)if(VIDEO.test(fs[i].path))out.push(fs[i]);out.sort(function(a,b){return b.length-a.length});return out}
/* wait until TorrServer has the metadata (file list) for hash, up to ~60 s */
function waitInfo(hash,n){return ts('/torrents',{action:'add',link:hash,save_to_db:true}).then(function(st){if(st&&st.file_stats&&st.file_stats.length)return st;if(n<=0)throw new Error('获取种子信息超时（没有 peer？）');msg('正在获取种子信息… '+n);return new Promise(function(r){setTimeout(r,1500)}).then(function(){return waitInfo(hash,n-1)})})}
function go(watch){
  var link=normalize($('t').value);
  if(!link){msg('没识别出磁力链接 / infohash / 种子 URL',1);return}
  busy(true);msg('正在添加…');
  ts('/torrents',{action:'add',link:link,save_to_db:true}).then(function(st){
    var hash=st&&st.hash;if(!hash)throw new Error('TorrServer 没有返回 hash');
    tell({action:'added',hash:hash,title:st.title||''}).catch(function(){});
    if(!watch){msg('已添加到电视缓存 ✓');$('t').value='';busy(false);load();return}
    return waitInfo(hash,40).then(function(full){
      var vf=videoFiles(full);
      if(!vf.length)throw new Error('种子里没有视频文件');
      return tell({action:'play',hash:hash,index:vf[0].id,title:full.title||''}).then(function(){
        msg('已发送到电视：'+(vf[0].path.split('/').pop())+(vf.length>1?'（共 '+vf.length+' 个视频，可在下方列表选集）':'')+' ✓');$('t').value='';busy(false);load();
      });
    });
  }).catch(function(e){msg(e.message||String(e),1);busy(false)});
}
function wake(hash){return ts('/torrents',{action:'add',link:hash,save_to_db:true})}
function playFile(hash,id,title){tell({action:'play',hash:hash,index:id,title:title}).then(function(){msg('已发送到电视 ✓')},function(e){msg(e.message,1)})}
function del(hash,title){if(!confirm('从电视删除「'+title+'」？'))return;ts('/torrents',{action:'rem',hash:hash}).then(function(){tell({action:'added',hash:hash}).catch(function(){});load()},function(e){msg(e.message,1)})}
function toggle(hash){open[hash]=!open[hash];load()}
function render(list){
  var box=$('list');box.innerHTML='';
  if(!list||!list.length){box.innerHTML='<div class="empty">电视上还没有种子。在上面粘贴磁力链接开始。</div>';return}
  list.sort(function(a,b){return(b.timestamp||0)-(a.timestamp||0)});
  list.forEach(function(t){
    var d=document.createElement('div');d.className='t';
    var pct=t.torrent_size?Math.min(100,100*(t.loaded_size||0)/t.torrent_size):0;
    var vf=videoFiles(t);
    var sub=[t.stat_string||'',t.torrent_size?fmtSize(t.torrent_size):'',t.total_peers?t.total_peers+' peers':'',t.download_speed?fmtSize(t.download_speed)+'/s':''].filter(Boolean).join(' · ');
    d.innerHTML='<div class="n"></div><div class="s"></div><div class="bar"><i></i></div><div class="a"><button class="watch">观看</button><button class="cache">'+(vf.length?'文件 ('+vf.length+')':'文件')+'</button><button class="del">删除</button></div><div class="files"></div>';
    d.children[0].textContent=t.title||t.name||t.hash;d.children[1].textContent=sub;d.children[2].firstChild.style.width=pct.toFixed(1)+'%';
    var a=d.children[3];
    a.children[0].onclick=function(){if(vf.length){playFile(t.hash,vf[0].id,t.title||'');return}msg('唤醒种子…');wake(t.hash).then(function(st){var v=videoFiles(st);if(!v.length)throw new Error('还没有文件信息，稍等再试');playFile(t.hash,v[0].id,st.title||'')}).catch(function(e){msg(e.message,1)})};
    a.children[0].disabled=t.stat===1;
    a.children[1].onclick=function(){if(vf.length){toggle(t.hash);return}wake(t.hash).then(function(){open[t.hash]=true;load()},function(e){msg(e.message,1)})};a.children[1].disabled=t.stat===1;
    a.children[2].onclick=function(){del(t.hash,t.title||t.name||t.hash)};
    if(open[t.hash]){var fl=d.children[4];var all=(t.file_stats||[]).filter(function(f){return VIDEO.test(f.path)});all.sort(function(x,y){return x.path<y.path?-1:1});all.forEach(function(f){var r=document.createElement('div');r.className='f';r.innerHTML='<span class="fn"></span><span class="fs"></span><button>播放</button>';r.children[0].textContent=f.path.split('/').pop();r.children[1].textContent=fmtSize(f.length);r.children[2].onclick=function(){playFile(t.hash,f.id,t.title||'')};fl.appendChild(r)})}
    box.appendChild(d);
  });
}
function load(){ts('/torrents',{action:'list'}).then(render,function(e){$('list').innerHTML='<div class="empty">连不上 TorrServer（'+TS+'）</div>'})}
function st(){fetch('/remote/'+ID+'/state').then(function(r){return r.ok?r.json():null}).then(function(s){
  var c=$('ctx');if(!s||!s.t){c.textContent='电视应用尚未报告状态（请在电视上打开应用）';return}
  var age=Math.round(Date.now()/1000-s.t);
  c.textContent=(age>120?'电视应用可能已关闭 · ':'电视在线 · ')+(s.view==='player'?'正在播放：'+(s.title||''):s.view==='files'?'正在看文件列表：'+(s.title||''):'在首页');
}).catch(function(){$('ctx').textContent='无法连接 sidecar'})}
st();setInterval(st,5000);load();setInterval(load,4000);
document.addEventListener('visibilitychange',function(){if(!document.hidden){st();load()}});
</script></body></html>`
