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
.t .s b{font-weight:600;margin-right:8px}.t .s b.ok{color:#22c55e}.t .s b.noseed{color:#fad778}.t .s b.nopeer{color:#ff8a80}
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
.f button.sub{background:#2a2f40;color:#e8e8ec}
.sp{background:#0f1117;border:1px solid #2a2f40;border-radius:10px;padding:10px;margin:4px 0 8px}
.sp .q{display:flex;gap:8px}.sp input[type=text]{flex:1;min-width:0;font-size:14px;padding:8px 10px;border-radius:8px;border:1px solid #2a2f40;background:#1a1d28;color:#fff}
.sp .q button{flex:none;background:#fad778;color:#111;padding:8px 12px;font-size:13px;border-radius:8px}
.sp .r{display:flex;align-items:center;gap:8px;padding:7px 0;border-top:1px solid #1f2330;font-size:13px}
.sp .r .rn{flex:1;min-width:0;overflow:hidden;text-overflow:ellipsis;white-space:nowrap;color:#d6d9e2}
.sp .r .tag{flex:none;font-size:11px;color:#fad778;border:1px solid rgba(250,215,120,.5);border-radius:5px;padding:0 5px;line-height:18px}
.sp .r button{flex:none;background:#2a2f40;color:#e8e8ec;padding:5px 10px;font-size:12px;border-radius:7px}
.sp .r.cur .rn{color:#fad778}
.sp .m{font-size:12px;color:#8f94a3;margin-top:6px;min-height:14px}.sp .m.err{color:#ff8a80}
.sp .up{display:block;margin-top:8px;font-size:12px;color:#8f94a3}.sp .up input{display:none}
.sp .up span{display:inline-block;border:1px dashed #2a2f40;border-radius:8px;padding:6px 10px}
#srch{margin-top:12px;background:#161925;border-radius:12px;padding:0 12px}
#srch summary{font-size:15px;color:#fad778;padding:12px 0;cursor:pointer;font-weight:600}
#srch .q{display:flex;gap:8px;padding-bottom:10px}
#srch input{flex:1;min-width:0;font-size:16px;padding:10px 12px;border-radius:10px;border:2px solid #2a2f40;background:#1a1d28;color:#fff;outline:none}
#srch input:focus{border-color:#fad778}
#srch .q button{flex:none;background:#fad778;color:#111;padding:10px 16px;font-size:15px}
#sm{font-size:13px;color:#8f94a3;min-height:16px;padding-bottom:8px}#sm.err{color:#ff8a80}
.sr{padding:10px 0;border-top:1px solid #1f2330}
.sr .n{font-size:14px;color:#fff;word-break:break-all;line-height:1.35}
.sr .s{font-size:12px;color:#8f94a3;margin-top:3px}
.sr .s b{font-weight:600}.sr .s b.ok{color:#22c55e}.sr .s b.mid{color:#fad778}.sr .s b.bad{color:#ff8a80}
.sr .a{display:flex;gap:8px;margin-top:8px}.sr .a button{padding:8px 6px;font-size:14px}
</style></head><body>
<h1>电视磁力</h1><div id="ctx">连接中…</div>
<textarea id="t" placeholder="粘贴 magnet:?xt=urn:btih:… 、40 位 infohash 或 .torrent 链接" autocapitalize="off" autocorrect="off" spellcheck="false"></textarea>
<button class="paste" id="pb" onclick="pasteIn()">从剪贴板粘贴</button>
<div class="btns"><button class="cache" id="bc" onclick="go(false)">缓存（不播放）</button><button class="watch" id="bw" onclick="go(true)">在电视上观看</button></div>
<div id="msg"></div>
<details id="srch"><summary>搜索种子</summary>
<form class="q" onsubmit="tsSearch();return false"><input id="ts-q" type="search" placeholder="片名，如 Dune 2024" autocapitalize="off" autocorrect="off" spellcheck="false" enterkeyhint="search"><button id="ts-b" type="submit">搜索</button></form>
<div id="sm"></div><div id="sr"></div></details>
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
/* swarm health: can it finish? (same rule as the TV app, api.js health()) */
function health(t){if(!t||t.stat!==3||!t.torrent_size||(t.loaded_size||0)>=t.torrent_size)return'';if((t.connected_seeders||0)>0)return'ok';if((t.active_peers||0)>0||(t.total_peers||0)>0)return'noseed';return'nopeer'}
var HEALTH={ok:'● 健康',noseed:'● 无做种者 · 可能下不完',nopeer:'● 没有 peer'};
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
/* torrent search through TorrServer's Torznab proxy (GET <TS>/torznab/search/?query=): Seed/Peer/Size come as strings */
var SEARCH_MAX=50,searchSeq=0;
function smsg(s,err){var m=$('sm');m.textContent=s||'';m.className=err?'err':''}
function seedCls(n){return n>=10?'ok':n>0?'mid':'bad'}
function tsSearch(){
  var q=$('ts-q').value.trim();if(!q){smsg('请输入片名',1);return}
  $('ts-q').blur();var my=++searchSeq;$('ts-b').disabled=true;$('sr').innerHTML='';smsg('正在搜索…');
  var slow=setTimeout(function(){if(my===searchSeq)smsg('正在搜索…（搜索站响应慢，再等等）')},12000);
  fetch(TS+'/torznab/search/?query='+encodeURIComponent(q)).then(function(r){if(!r.ok)throw new Error('TorrServer '+r.status);return r.text()}).then(function(t){
    var list=null;try{list=t?JSON.parse(t):[]}catch(e){throw new Error('搜索返回无法解析')}
    if(my!==searchSeq)return;
    if(!Array.isArray(list))throw new Error('服务端未开启搜索（TorrServer 设置里的 Torznab）');
    renderSearch(list,q);
  }).catch(function(e){if(my===searchSeq)smsg(e.message||String(e),1)}).then(function(){clearTimeout(slow);if(my===searchSeq)$('ts-b').disabled=false});
}
function renderSearch(list,q){
  var box=$('sr');box.innerHTML='';
  list=list.filter(function(x){return x&&(x.Magnet||x.Link)});
  list.sort(function(a,b){return(+b.Seed||0)-(+a.Seed||0)});
  if(!list.length){smsg('没有找到「'+q+'」的种子（或服务端未开启搜索）');return}
  smsg('共 '+list.length+' 条'+(list.length>SEARCH_MAX?'，显示做种最多的 '+SEARCH_MAX+' 条':''));
  list.slice(0,SEARCH_MAX).forEach(function(x){
    var seed=+x.Seed||0,d=document.createElement('div');d.className='sr';
    d.innerHTML='<div class="n"></div><div class="s"></div><div class="a"><button class="cache">缓存</button><button class="watch">观看</button></div>';
    d.children[0].textContent=x.Title||x.Name||'';
    var s=d.children[1];var b=document.createElement('b');b.className=seedCls(seed);b.textContent=seed+' 做种';s.appendChild(b);
    s.appendChild(document.createTextNode([' · '+(+x.Peer||0)+' 下载',x.Size?' · '+x.Size:'',x.Tracker?' · '+x.Tracker:''].join('')));
    d.children[2].children[0].onclick=function(){pickResult(x,false)};
    d.children[2].children[1].onclick=function(){pickResult(x,true)};
    box.appendChild(d);
  });
}
/* reuse the paste flow: fill the box and go() (add -> wait for metadata -> biggest video -> tell the TV) */
function pickResult(x,watch){$('t').value=x.Magnet||x.Link;go(watch);window.scrollTo(0,0)}
function wake(hash){return ts('/torrents',{action:'add',link:hash,save_to_db:true})}
function playFile(hash,id,title){tell({action:'play',hash:hash,index:id,title:title}).then(function(){msg('已发送到电视 ✓')},function(e){msg(e.message,1)})}
function del(hash,title){if(!confirm('从电视删除「'+title+'」？'))return;ts('/torrents',{action:'rem',hash:hash}).then(function(){tell({action:'added',hash:hash}).catch(function(){});load()},function(e){msg(e.message,1)})}
function toggle(hash){open[hash]=!open[hash];load()}
/* ---- external subtitles (sidecar /subs/*) ---- */
var SUBS=__SUBS__; /* {enabled, sources} */
var subOpen={}, subState={}, loadForced=false; /* key hash:id -> {saved,results,msg,err,q,busy} */
function skey(h,id){return h+':'+id}
function sget(h,id){var k=skey(h,id);if(!subState[k])subState[k]={saved:[],results:null,msg:'',err:false,q:'',busy:false};return subState[k]}
function sapi(path,opt){return fetch(path,opt).then(function(r){return r.text().then(function(t){var j=null;try{j=t?JSON.parse(t):null}catch(e){}if(!r.ok)throw new Error((j&&j.error)||('HTTP '+r.status));return j})})}
function subToggle(h,id,fname){var k=skey(h,id);subOpen[k]=!subOpen[k];if(subOpen[k]){var st=sget(h,id);if(!st.q)st.q=fname;sapi('/subs/list?hash='+h+'&index='+id).then(function(r){st.saved=(r&&r.subs)||[];load()},function(e){st.msg=e.message;st.err=true;load()})}load()}
function subSearch(h,id){var st=sget(h,id);var inp=document.getElementById('sq-'+skey(h,id));var q=(inp&&inp.value)||st.q;st.q=q;if(inp)inp.blur();st.busy=true;st.err=false;st.msg='正在搜索…（字幕站限速，可能要等十几秒）';loadForced=true;load();
  sapi('/subs/search?q='+encodeURIComponent(q)).then(function(r){st.busy=false;st.results=(r&&r.results)||[];st.msg=st.results.length?'搜索词：'+(r.query||q):'没有找到，换个关键词试试（片名 + 年份 / S01E02）';load()},function(e){st.busy=false;st.msg='搜索失败：'+e.message;st.err=true;load()})}
function subPick(h,id,fname,title,res){var st=sget(h,id);st.busy=true;st.err=false;st.msg='正在下载并转换…';load();
  sapi('/subs/fetch',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({hash:h,index:id,src:res.src,id:res.id,video:fname})}).then(function(r){return subDone(h,id,title,r&&r.sub)}).catch(function(e){st.busy=false;st.msg='下载失败：'+e.message;st.err=true;load()})}
function subUpload(h,id,fname,title,input){var f=input.files&&input.files[0];if(!f)return;var st=sget(h,id);st.busy=true;st.err=false;st.msg='正在上传 '+f.name+'…';load();
  sapi('/subs/upload?hash='+h+'&index='+id+'&name='+encodeURIComponent(f.name)+'&video='+encodeURIComponent(fname),{method:'POST',body:f}).then(function(r){return subDone(h,id,title,r&&r.sub)}).catch(function(e){st.busy=false;st.msg='上传失败：'+e.message;st.err=true;load()})}
function subDone(h,id,title,sub){var st=sget(h,id);st.busy=false;if(!sub)throw new Error('没有返回字幕');st.saved.push(sub);st.msg='已就绪：'+(sub.label||sub.name)+'（'+sub.cues+' 条）';
  return tell({action:'sub',hash:h,index:id,url:sub.url,title:sub.label||sub.name}).then(function(){st.msg+=' · 已发送到电视 ✓';load()},function(){st.msg+=' · 电视未在线，播放时会自动加载';load()})}
function subUse(h,id,sub){tell({action:'sub',hash:h,index:id,url:sub.url,title:sub.label||sub.name}).then(function(){var st=sget(h,id);st.msg='已发送到电视 ✓';st.err=false;load()},function(e){var st=sget(h,id);st.msg=e.message;st.err=true;load()})}
function subPanel(h,id,fname,title){var st=sget(h,id),k=skey(h,id);var d=document.createElement('div');d.className='sp';
  var q=document.createElement('div');q.className='q';var inp=document.createElement('input');inp.type='text';inp.id='sq-'+k;inp.value=st.q;inp.placeholder='片名 年份 / 片名 S01E02';inp.onkeydown=function(e){if(e.key==='Enter'){subSearch(h,id)}};var b=document.createElement('button');b.textContent='搜索';b.disabled=st.busy||!SUBS.sources.length;b.onclick=function(){subSearch(h,id)};q.appendChild(inp);q.appendChild(b);d.appendChild(q);
  if(!SUBS.sources.length){var w=document.createElement('div');w.className='m err';w.textContent='服务端未配置字幕站 token（ASSRT_TOKEN），只能上传';d.appendChild(w)}
  st.saved.forEach(function(sv){var r=document.createElement('div');r.className='r';r.innerHTML='<span class="tag"></span><span class="rn"></span><button>用这个</button>';r.children[0].textContent=sv.src==='upload'?'上传':sv.src;r.children[1].textContent=(sv.label?sv.label+' · ':'')+sv.name;r.children[2].onclick=function(){subUse(h,id,sv)};d.appendChild(r)});
  if(st.results){if(!st.results.length){}else{var hd=document.createElement('div');hd.className='m';hd.textContent='搜索结果 '+st.results.length;d.appendChild(hd)}
    st.results.forEach(function(res){var r=document.createElement('div');r.className='r';r.innerHTML='<span class="tag"></span><span class="rn"></span><button>下载</button>';r.children[0].textContent=res.label||res.format||res.src;r.children[1].textContent=res.title+(res.files&&res.files.length>1?'（'+res.files.length+' 个文件）':'');r.children[2].disabled=st.busy;r.children[2].onclick=function(){subPick(h,id,fname,title,res)};d.appendChild(r)})}
  var up=document.createElement('label');up.className='up';up.innerHTML='<span>上传字幕文件（.srt / .ass / .zip）</span><input type="file" accept=".srt,.ass,.ssa,.vtt,.zip">';up.lastChild.onchange=function(){subUpload(h,id,fname,title,this)};d.appendChild(up);
  var m=document.createElement('div');m.className='m'+(st.err?' err':'');m.textContent=st.msg;d.appendChild(m);
  return d}
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
    var hl=health(t);if(hl){var b=document.createElement('b');b.className=hl;b.textContent=HEALTH[hl]+(hl==='ok'?' · '+t.connected_seeders+' 做种':'');d.children[1].insertBefore(b,d.children[1].firstChild)}
    var a=d.children[3];
    a.children[0].onclick=function(){if(vf.length){playFile(t.hash,vf[0].id,t.title||'');return}msg('唤醒种子…');wake(t.hash).then(function(st){var v=videoFiles(st);if(!v.length)throw new Error('还没有文件信息，稍等再试');playFile(t.hash,v[0].id,st.title||'')}).catch(function(e){msg(e.message,1)})};
    a.children[0].disabled=t.stat===1;
    a.children[1].onclick=function(){if(vf.length){toggle(t.hash);return}wake(t.hash).then(function(){open[t.hash]=true;load()},function(e){msg(e.message,1)})};a.children[1].disabled=t.stat===1;
    a.children[2].onclick=function(){del(t.hash,t.title||t.name||t.hash)};
    if(open[t.hash]){var fl=d.children[4];var all=(t.file_stats||[]).filter(function(f){return VIDEO.test(f.path)});all.sort(function(x,y){return x.path<y.path?-1:1});all.forEach(function(f){var fname=f.path.split('/').pop();var r=document.createElement('div');r.className='f';r.innerHTML='<span class="fn"></span><span class="fs"></span>'+(SUBS.enabled?'<button class="sub">字幕</button>':'')+'<button>播放</button>';r.children[0].textContent=fname;r.children[1].textContent=fmtSize(f.length);if(SUBS.enabled){r.children[2].onclick=function(){subToggle(t.hash,f.id,fname)}}r.children[r.children.length-1].onclick=function(){playFile(t.hash,f.id,t.title||'')};fl.appendChild(r);if(SUBS.enabled&&subOpen[skey(t.hash,f.id)])fl.appendChild(subPanel(t.hash,f.id,fname,t.title||''))})}
    box.appendChild(d);
  });
}
function load(){var ae=document.activeElement;if(ae&&ae.tagName==='INPUT'&&ae.id&&ae.id.indexOf('sq-')===0){var k=ae.id.slice(3);if(subState[k])subState[k].q=ae.value;if(!loadForced){return}}loadForced=false;ts('/torrents',{action:'list'}).then(render,function(e){$('list').innerHTML='<div class="empty">连不上 TorrServer（'+TS+'）</div>'})}
function st(){fetch('/remote/'+ID+'/state').then(function(r){return r.ok?r.json():null}).then(function(s){
  var c=$('ctx');if(!s||!s.t){c.textContent='电视应用尚未报告状态（请在电视上打开应用）';return}
  var age=Math.round(Date.now()/1000-s.t);
  c.textContent=(age>120?'电视应用可能已关闭 · ':'电视在线 · ')+(s.view==='player'?'正在播放：'+(s.title||''):s.view==='files'?'正在看文件列表：'+(s.title||''):'在首页');
}).catch(function(){$('ctx').textContent='无法连接 sidecar'})}
st();setInterval(st,5000);load();setInterval(load,4000);
document.addEventListener('visibilitychange',function(){if(!document.hidden){st();load()}});
</script></body></html>`
