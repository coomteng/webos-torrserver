/* player.js — fullscreen <video> on a TorrServer /stream URL with OSD and a file strip. ES5 only.
 * Reduced from coomteng/moontv-webos app/js/player.js: plain <video src> (progressive MP4/MKV over
 * HTTP ranges), no HLS/mediaOption, resume positions in localStorage, TorrServer stat in the OSD. */
(function () {
  'use strict';

  var OSD_TIMEOUT = 4000;
  var SAVE_INTERVAL = 10000;
  var STAT_INTERVAL = 2000;
  var SEEK_STEP = 10;
  var VIDEO = /\.(mkv|mp4|m4v|avi|mov|ts|m2ts|mpg|mpeg|webm|wmv|flv|vob|iso|3gp)$/i;

  var video = null;
  var hash = '', title = '', files = [], index = 0, fileIdx = 0;
  var pendingSeek = 0;
  var osdTimer = null, saveTimer = null, statTimer = null;
  var active = false;
  var seekHold = { timer: null, dir: 0 };
  var osdFocus = false;
  var lastErr = '';
  var audioN = 0;          /* audio tracks seen on the current file (0 = API absent or single) */
  var LANG_KEY = 'torr.lang';
  var SUB_KEY = function () { return 'torr.sub.' + hash; };   /* { "<index>": { url, name, label, offset } } */
  var subPanel = false;    /* subtitle panel open (green key) */
  var subItems = [];       /* rows in the panel: {kind:'off'|'saved'|'search'|'result'|'delay-'|'delay+'|'head', ...} */
  var subResults = null;   /* last search results for this file */
  var subBusy = false;

  function $(id) { return UI.$(id); }

  function fileById(id) { for (var i = 0; i < files.length; i++) if (files[i].id === id) return i; return -1; }
  function shortName(p) { var s = String(p || ''); var i = s.lastIndexOf('/'); return i >= 0 ? s.slice(i + 1) : s; }
  function current() { return files[fileIdx]; }

  /* ---- OSD ---- */
  function showOsd(sticky) {
    UI.addClass($('osd'), 'show');
    if (osdTimer) clearTimeout(osdTimer);
    if (!sticky) osdTimer = setTimeout(hideOsd, OSD_TIMEOUT);
  }
  function hideOsd() {
    if (osdFocus) { osdTimer = setTimeout(hideOsd, OSD_TIMEOUT); return; }
    UI.removeClass($('osd'), 'show');
  }
  function setState(msg) {
    var s = $('osd-state');
    s.textContent = msg || '';
    UI.toggleClass(s, 'show', !!msg);
  }

  function updateTime() {
    var cur = video.currentTime || 0, dur = video.duration;
    if (!isFinite(dur)) dur = 0;
    $('osd-time').textContent = UI.fmtTime(cur) + ' / ' + UI.fmtTime(dur);
    var pos = $('osd-bar').children[1], buf = $('osd-bar').children[0];
    pos.style.width = dur ? Math.min(100, 100 * cur / dur) + '%' : '0%';
    var b = 0;
    try { if (video.buffered.length) b = video.buffered.end(video.buffered.length - 1); } catch (e) { /* ignore */ }
    buf.style.width = dur ? Math.min(100, 100 * b / dur) + '%' : '0%';
  }

  function renderTitle() {
    var t = $('osd-title');
    t.children[0].textContent = title || '';
    var f = current();
    t.children[1].textContent = f && files.length > 1 ? ' > ' + shortName(f.path) : '';
  }

  function renderEps() {
    var box = $('osd-eps');
    UI.empty(box);
    UI.translate(box, 0, 0);
    for (var i = 0; i < files.length; i++) {
      var e = UI.text('div', 'ep', files.length > 1 ? String(i + 1) : '正片');
      e.setAttribute('title', shortName(files[i].path));
      if (i === fileIdx) UI.addClass(e, 'current');
      box.appendChild(e);
    }
    UI.toggleClass($('osd-eps-wrap'), 'hidden', files.length <= 1);
  }
  function markCurrentEp() {
    var kids = $('osd-eps').children;
    for (var i = 0; i < kids.length; i++) UI.toggleClass(kids[i], 'current', i === fileIdx);
  }

  /* ---- audio tracks (HTMLMediaElement.audioTracks; webOS >= 4 exposes it for MKV/MP4) ---- */
  var LANG_NAMES = { zh: '中文', chi: '中文', zho: '中文', cmn: '普通话', yue: '粤语', en: '英语', eng: '英语', ja: '日语', jpn: '日语',
    ko: '韩语', kor: '韩语', fr: '法语', fra: '法语', fre: '法语', de: '德语', deu: '德语', ger: '德语', es: '西班牙语', spa: '西班牙语',
    ru: '俄语', rus: '俄语', it: '意大利语', ita: '意大利语', th: '泰语', tha: '泰语', hi: '印地语', hin: '印地语', und: '' };
  function audioTracks() { var t = video && video.audioTracks; return t && t.length ? t : null; }
  function trackName(t, i) {
    var lang = String(t.language || '').toLowerCase().split('-')[0];
    var name = LANG_NAMES.hasOwnProperty(lang) ? LANG_NAMES[lang] : (t.language || '');
    var label = String(t.label || '');
    if (/^(soundhandler|stereo|surround|audio)$/i.test(label)) label = '';
    var s = name || label || ('音轨 ' + (i + 1));
    if (name && label && label.toLowerCase().indexOf(name.toLowerCase()) < 0) s += ' · ' + label;
    return s;
  }
  function currentAudio() { var t = audioTracks(); if (!t) return -1; for (var i = 0; i < t.length; i++) if (t[i].enabled) return i; return -1; }
  function renderAudio() {
    var t = audioTracks(), el = $('osd-audio');
    audioN = t ? t.length : 0;
    if (audioN < 2) { UI.addClass(el, 'hidden'); el.textContent = ''; return; }
    var cur = currentAudio();
    el.textContent = '音轨 ' + (cur + 1) + '/' + audioN + ' · ' + (cur >= 0 ? trackName(t[cur], cur) : '—');
    UI.removeClass(el, 'hidden');
  }
  function setAudio(i, announce) {
    var t = audioTracks();
    if (!t || !t[i]) return;
    for (var k = 0; k < t.length; k++) t[k].enabled = (k === i);
    try { window.localStorage.setItem(LANG_KEY, String(t[i].language || '').toLowerCase()); } catch (e) { /* ignore */ }
    renderAudio();
    if (announce) { setState('音轨：' + trackName(t[i], i)); showOsd(false); }
  }
  function cycleAudio() {
    var t = audioTracks();
    if (!t || t.length < 2) { setState(t ? '只有一条音轨' : '此文件不支持切换音轨'); showOsd(false); return; }
    setAudio((currentAudio() + 1) % t.length, true);
  }
  /* On a new file: prefer the language picked last time (season packs), else leave the default. */
  function applyPreferredAudio() {
    var t = audioTracks();
    if (!t || t.length < 2) { renderAudio(); return; }
    var want = '';
    try { want = window.localStorage.getItem(LANG_KEY) || ''; } catch (e) { /* ignore */ }
    var cur = currentAudio();
    if (want && !(cur >= 0 && String(t[cur].language || '').toLowerCase() === want)) {
      for (var i = 0; i < t.length; i++) if (String(t[i].language || '').toLowerCase() === want) { for (var k = 0; k < t.length; k++) t[k].enabled = (k === i); break; }
    }
    renderAudio();
  }
  function bindAudio() {
    var t = video.audioTracks;
    if (!t || !t.addEventListener) return;
    t.addEventListener('addtrack', function () { if (active) applyPreferredAudio(); }, false);
    t.addEventListener('removetrack', function () { if (active) renderAudio(); }, false);
    t.addEventListener('change', function () { if (active) renderAudio(); }, false);
  }

  /* ---- external subtitles (sidecar /subs/*, drawn by Subs) ----
   * Two slots: primary (bottom) + secondary (top of the screen), e.g. 简中 + 英文 for language learning.
   * Memory per file: torr.sub.<hash>[index] = { slots: [sub|null, sub|null], offset } or { off: true }. */
  function subMem() { try { return JSON.parse(window.localStorage.getItem(SUB_KEY()) || '{}') || {}; } catch (e) { return {}; } }
  function subRemember() {
    var m = subMem(), sl = Subs.slots();
    if (!sl[0] && !sl[1]) m[index] = { off: true };
    else m[index] = { slots: [sl[0] ? { url: sl[0].url, name: sl[0].name, label: sl[0].label } : null, sl[1] ? { url: sl[1].url, name: sl[1].name, label: sl[1].label } : null], offset: Subs.offset() };
    try { window.localStorage.setItem(SUB_KEY(), JSON.stringify(m)); } catch (e) { /* ignore */ }
  }
  function subName(s) { return s ? (s.label || s.name || '字幕') : ''; }
  function renderSubBadge() {
    var el = $('osd-sub'), sl = Subs.slots();
    if (!sl[0] && !sl[1]) { UI.addClass(el, 'hidden'); el.textContent = ''; return; }
    var t = sl[0] ? subName(sl[0]) : '';
    if (sl[1]) t += (t ? ' + ' : '') + subName(sl[1]);
    if (Subs.offset()) t += ' ' + (Subs.offset() > 0 ? '+' : '') + (Subs.offset() / 1000).toFixed(1) + 's';
    el.textContent = t;
    UI.removeClass(el, 'hidden');
  }
  function subAnnounce() {
    var sl = Subs.slots();
    if (!sl[0] && !sl[1]) setState('字幕：关闭');
    else setState('字幕：' + subName(sl[0] || sl[1]) + (sl[0] && sl[1] ? ' + ' + subName(sl[1]) : ''));
    showOsd(false);
  }
  function subLoad(n, sub, announce) {
    return Subs.load(n, sub).then(function (cnt) {
      if (!active || Subs.current(n) !== sub) return cnt;
      subRemember();
      renderSubBadge();
      if (announce) { if (cnt) subAnnounce(); else { setState('字幕文件为空'); showOsd(false); } }
      return cnt;
    }, function (err) {
      Subs.clear(n); renderSubBadge();
      if (announce) { setState('字幕加载失败：' + err.message); showOsd(false); }
      return 0;
    });
  }
  function slotOf(url) { var sl = Subs.slots(); for (var i = 0; i < 2; i++) if (sl[i] && sl[i].url === url) return i; return -1; }
  /* Toggle a subtitle: selected -> remove; else fill the first free slot; both full -> replace secondary. */
  function subToggle(sub) {
    var i = slotOf(sub.url);
    if (i >= 0) {
      Subs.clear(i);
      if (i === 0 && Subs.current(1)) Subs.swap();   /* keep the remaining one as primary */
      subRemember(); renderSubBadge(); subAnnounce();
      return Promise.resolve(0);
    }
    var n = !Subs.current(0) ? 0 : 1;
    return subLoad(n, sub, true);
  }
  function subOff() { Subs.clear(); subRemember(); renderSubBadge(); subAnnounce(); }
  function subSetOffset(v) { Subs.setOffset(v); subRemember(); renderSubBadge(); }
  /* On a new file: restore what was chosen before; nothing chosen yet but the sidecar has subtitles
   * for it (phone uploaded) -> take the newest one as primary. */
  function subAutoLoad() {
    Subs.clear(); Subs.setOffset(0); renderSubBadge();
    if (!API.subsEnabled()) return;
    var m = subMem()[index];
    if (m && m.off) return;
    if (m && (m.slots || m.url)) {
      var sl = m.slots || [{ url: m.url, name: m.name, label: m.label }, null];   /* 0.3.0 single-slot format */
      Subs.setOffset(m.offset || 0);
      if (sl[0]) subLoad(0, sl[0], false);
      if (sl[1]) subLoad(1, sl[1], false);
      return;
    }
    var myIndex = index;
    API.subsList(hash, index).then(function (r) {
      if (!active || index !== myIndex || !r || !r.subs || !r.subs.length) return;
      subLoad(0, r.subs[r.subs.length - 1], false);
    }, function () {});
  }
  function onRemoteSub(ev) {
    if (!active || ev.hash !== hash || (ev.index && ev.index !== index)) return;
    UI.toast('手机：字幕 ' + (ev.title || ''));
    var sub = { url: ev.url, name: ev.title, label: ev.title };
    if (slotOf(sub.url) >= 0) return;
    subLoad(!Subs.current(0) ? 0 : 1, sub, true);
  }

  function subRow(kind, text, extra) {
    var el = UI.el('div', 'sub-row' + (kind === 'head' ? ' head' : ''), { 'data-kind': kind });
    if (extra && extra.mark) el.appendChild(UI.text('span', 'mark', extra.mark));
    if (extra && extra.tag) el.appendChild(UI.text('span', 'tag', extra.tag));
    el.appendChild(document.createTextNode(text));
    if (extra && extra.dim) el.appendChild(UI.text('span', 'dim', extra.dim));
    if (extra && extra.current) UI.addClass(el, 'current');
    return el;
  }
  function subMsg(t) { $('osd-subs-msg').textContent = t || ''; }
  function renderSubPanel(saved, focusKind, focusUrl) {
    var list = $('osd-subs-list');
    UI.empty(list);
    var inner = UI.el('div', 'inner');
    list.appendChild(inner);
    subItems = [];
    var sl = Subs.slots();
    var any = !!(sl[0] || sl[1]);
    function add(item, el) { subItems.push(item); inner.appendChild(el); }
    add({ kind: 'off' }, subRow('off', '关闭字幕', { current: !any }));
    if (saved.length) add({ kind: 'head' }, subRow('head', any && sl[0] && sl[1] ? '已选两条（下 + 上）· 再按一次取消' : '可选两条：第一条在下、第二条在上'));
    for (var i = 0; i < saved.length; i++) {
      var sv = saved[i], si = slotOf(sv.url);
      add({ kind: 'saved', sub: sv }, subRow('saved', sv.label || sv.name, {
        mark: si === 0 ? '① 下' : si === 1 ? '② 上' : '',
        tag: sv.src === 'upload' ? '上传' : sv.src, dim: sv.name && sv.label ? sv.name : (sv.cues + ' 条'), current: si >= 0 }));
    }
    if (any) {
      if (sl[0] && sl[1]) add({ kind: 'swap' }, subRow('swap', '交换上下', { dim: subName(sl[0]) + ' ⇄ ' + subName(sl[1]) }));
      add({ kind: 'delay-' }, subRow('delay-', '字幕提前 0.5 秒', { dim: '当前 ' + (Subs.offset() / 1000).toFixed(1) + 's' }));
      add({ kind: 'delay+' }, subRow('delay+', '字幕延后 0.5 秒'));
    }
    if (API.subsSources().length) {
      add({ kind: 'search' }, subRow('search', subResults ? '重新搜索字幕' : '在线搜索字幕', { dim: shortName(current() ? current().path : '') }));
    }
    if (subResults) {
      add({ kind: 'head' }, subRow('head', subResults.length ? '搜索结果（' + subResults.length + '）· 下载后在上面选' : '没有找到字幕'));
      for (var k = 0; k < subResults.length; k++) {
        var r = subResults[k];
        add({ kind: 'result', res: r }, subRow('result', r.title, { tag: r.label || r.format || r.src, dim: r.files && r.files.length > 1 ? r.files.length + ' 个文件' : (r.format || '') }));
      }
    }
    var start = 0;
    for (var j = 0; j < subItems.length; j++) {
      if (focusUrl && subItems[j].kind === 'saved' && subItems[j].sub.url === focusUrl) { start = j; break; }
      if (!focusUrl && subItems[j].kind === focusKind) { start = j; break; }
    }
    App.setZones([{
      name: 'subs', cols: 1, remember: false, index: start,
      getItems: function () { var out = [], kids = inner.children; for (var q = 0; q < kids.length; q++) if (!UI.hasClass(kids[q], 'head')) out.push(kids[q]); return out; },
      onFocus: function (el) { Nav.scrollY(inner, el, list.clientHeight || 760, 8, 8); },
      onEnter: function (el) { subActivate(el); }
    }], 0);
    Nav.focusZone(0, indexInZone(start));
  }
  function indexInZone(itemIdx) { var n = 0; for (var i = 0; i < itemIdx; i++) if (subItems[i].kind !== 'head') n++; return n; }
  function itemOf(el) { var kids = $('osd-subs-list').firstChild.children; for (var i = 0; i < kids.length; i++) if (kids[i] === el) return subItems[i]; return null; }
  var subSaved = [];   /* last /subs/list for the panel */
  function subActivate(el) {
    var it = itemOf(el);
    if (!it || subBusy) return;
    switch (it.kind) {
      case 'off': subOff(); closeSubPanel(); break;
      case 'saved':
        subBusy = true;
        subToggle(it.sub).then(function () { subBusy = false; if (subPanel) renderSubPanel(subSaved, 'saved', it.sub.url); }, function () { subBusy = false; });
        break;
      case 'swap': Subs.swap(); subRemember(); renderSubBadge(); subAnnounce(); renderSubPanel(subSaved, 'swap'); break;
      case 'delay-': subSetOffset(Subs.offset() - 500); renderSubPanel(subSaved, 'delay-'); break;
      case 'delay+': subSetOffset(Subs.offset() + 500); renderSubPanel(subSaved, 'delay+'); break;
      case 'search': subSearch(); break;
      case 'result': subPick(it.res); break;
    }
  }
  function subSearch() {
    var f = current();
    if (!f) return;
    subBusy = true;
    subMsg('正在搜索「' + shortName(f.path) + '」…（字幕站限速，可能要等十几秒）');
    var myIndex = index;
    API.subsSearch(shortName(f.path), '').then(function (r) {
      subBusy = false;
      if (!subPanel || index !== myIndex) return;
      subResults = (r && r.results) || [];
      subMsg(r && r.query ? '搜索词：' + r.query + (r.errors && r.errors.length ? ' · ' + r.errors.join('; ') : '') : '');
      renderSubPanel(subSaved, subResults.length ? 'result' : 'search');
    }, function (err) {
      subBusy = false;
      if (!subPanel) return;
      subMsg('搜索失败：' + err.message);
    });
  }
  /* Download a result: it becomes a saved subtitle and is selected (first free slot). Panel stays
   * open so a second language can be picked right away. */
  function subPick(res) {
    var f = current();
    subBusy = true;
    subMsg('正在下载并转换「' + res.title + '」…');
    var myIndex = index;
    API.subsFetch(hash, index, res, f ? shortName(f.path) : '').then(function (r) {
      subBusy = false;
      if (index !== myIndex || !r || !r.sub) return;
      subSaved.push(r.sub);
      if (slotOf(r.sub.url) < 0) {
        var n = !Subs.current(0) ? 0 : (!Subs.current(1) ? 1 : 1);
        subLoad(n, r.sub, true).then(function () { if (subPanel) { subMsg('已加入：' + subName(r.sub) + (Subs.slots()[1] ? '' : ' · 还可以再选一条做第二字幕')); renderSubPanel(subSaved, 'saved', r.sub.url); } });
      } else if (subPanel) renderSubPanel(subSaved, 'saved', r.sub.url);
    }, function (err) {
      subBusy = false;
      if (subPanel) subMsg('下载失败：' + err.message);
    });
  }
  function openSubPanel(focusKind) {
    if (!API.subsEnabled()) { setState('未配置字幕服务'); showOsd(false); return; }
    var f = current();
    subPanel = true;
    osdFocus = true;
    $('osd-subs-file').textContent = f ? shortName(f.path) : '';
    UI.removeClass($('osd-subs'), 'hidden');
    showOsd(true);
    if (!focusKind) subMsg('');
    renderSubPanel(subSaved, focusKind || 'off');
    API.subsList(hash, index).then(function (r) {
      if (!subPanel) return;
      subSaved = (r && r.subs) || [];
      renderSubPanel(subSaved, focusKind || (Subs.active() ? 'saved' : 'off'), focusKind ? '' : (Subs.slots()[0] || {}).url);
    }, function (err) { if (subPanel) subMsg('读取已有字幕失败：' + err.message); });
  }
  function closeSubPanel() {
    subPanel = false;
    UI.addClass($('osd-subs'), 'hidden');
    subMsg('');
    zones();
    showOsd(false);
  }

  function zones() {
    App.setZones([{
      name: 'osd-eps', cols: 0, remember: true, index: fileIdx,
      getItems: function () { return UI.toArray($('osd-eps').children); },
      onFocus: function (el) { Nav.scrollX($('osd-eps'), el, 1760, 0, 0); },
      onEnter: function (el, i) { loadFile(i, 0); }
    }], 0);
    Nav.clearFocus();
    osdFocus = false;
  }
  function enterStrip() { if (files.length <= 1) return; osdFocus = true; Nav.focusZone(0, fileIdx); showOsd(true); }
  function leaveStrip() { osdFocus = false; Nav.clearFocus(); showOsd(false); }

  /* ---- resume positions (localStorage) ---- */
  function savePos(force) {
    if (!video || !hash) return;
    var cur = Math.floor(video.currentTime || 0), dur = video.duration;
    if (!force && cur < 3) return;
    var key = 'torr.pos.' + hash, m = {};
    try { m = JSON.parse(window.localStorage.getItem(key) || '{}') || {}; } catch (e) { /* ignore */ }
    if (isFinite(dur) && dur > 0 && cur > dur - 60) delete m[index]; else m[index] = cur;
    try { window.localStorage.setItem(key, JSON.stringify(m)); } catch (e2) { /* ignore */ }
  }
  function startSaveTimer() { stopSaveTimer(); saveTimer = setInterval(function () { if (!video.paused && !video.ended) savePos(false); }, SAVE_INTERVAL); }
  function stopSaveTimer() { if (saveTimer) { clearInterval(saveTimer); saveTimer = null; } }

  /* ---- TorrServer stat in the OSD (speed / peers / cache) ---- */
  function pollStat() {
    API.get(hash).then(function (st) {
      if (!active || !st) return;
      var parts = [];
      if (st.download_speed > 1024) parts.push('↓ ' + UI.fmtSize(st.download_speed) + '/s');
      if (st.active_peers || st.total_peers) parts.push((st.active_peers || 0) + '/' + (st.total_peers || 0) + ' peers');
      if (st.torrent_size) parts.push('已缓存 ' + Math.min(100, 100 * (st.loaded_size || 0) / st.torrent_size).toFixed(0) + '%');
      $('osd-stat').textContent = parts.join(' · ');
    }, function () {});
  }
  function startStat() { stopStat(); pollStat(); statTimer = setInterval(pollStat, STAT_INTERVAL); }
  function stopStat() { if (statTimer) { clearInterval(statTimer); statTimer = null; } }

  /* ---- playback ---- */
  function loadFile(i, startTime) {
    if (!files[i]) return;
    if (i !== fileIdx && video.currentTime > 3) savePos(true);
    fileIdx = i;
    index = files[i].id;
    pendingSeek = startTime || 0;
    lastErr = '';
    renderTitle();
    markCurrentEp();
    audioN = 0;
    UI.addClass($('osd-audio'), 'hidden');
    subResults = null;
    subSaved = [];
    if (subPanel) closeSubPanel();
    subAutoLoad();
    setState('加载中…');
    var url = API.streamUrl(hash, index, shortName(files[i].path));
    video.removeAttribute('src');
    video.src = url;
    try { video.load(); } catch (e) { /* ignore */ }
    startPlay();
    if (osdFocus) Nav.focusZone(0, fileIdx);
    showOsd(false);
    updateTime();
    App.Remote.report({ view: 'player', title: title + (files.length > 1 ? ' > ' + shortName(files[i].path) : ''), hash: hash, index: index });
  }

  function startPlay() {
    var p = null;
    try { p = video.play(); } catch (e) { /* ignore */ }
    if (p && p.then) p.then(null, function () {});
  }

  function togglePlay() {
    if (video.paused || video.ended) startPlay(); else video.pause();
    showOsd(false);
  }

  function seekBy(delta) {
    var dur = video.duration;
    var t = (video.currentTime || 0) + delta;
    if (t < 0) t = 0;
    if (isFinite(dur) && dur > 0 && t > dur - 1) t = dur - 1;
    try { video.currentTime = t; } catch (e) { /* ignore */ }
    setState((delta > 0 ? '▶▶ +' : '◀◀ −') + Math.abs(delta) + ' 秒');
    updateTime();
    showOsd(false);
  }

  function nextFile() {
    if (fileIdx + 1 < files.length) loadFile(fileIdx + 1, 0);
    else { setState('已播放完毕'); savePos(true); }
  }

  /* ---- pointer (Magic Remote) ---- */
  function bindPointer() {
    $('player').addEventListener('mousemove', function () { if (active) showOsd(false); }, false);
    video.addEventListener('click', function (e) { if (active) { e.preventDefault(); togglePlay(); } }, false);
    $('osd-state').addEventListener('click', function (e) { if (active) { e.preventDefault(); togglePlay(); } }, false);
    $('osd-audio').addEventListener('click', function (e) { if (active) { e.preventDefault(); e.stopPropagation(); cycleAudio(); } }, false);
    $('osd-sub').addEventListener('click', function (e) { if (active) { e.preventDefault(); e.stopPropagation(); if (subPanel) closeSubPanel(); else openSubPanel(); } }, false);
    $('osd-bar').addEventListener('click', function (e) {
      if (!active || !isFinite(video.duration) || !video.duration) return;
      e.preventDefault(); e.stopPropagation();
      var r = this.getBoundingClientRect();
      var frac = Math.max(0, Math.min(1, (e.clientX - r.left) / r.width));
      video.currentTime = frac * video.duration;
      updateTime(); showOsd(false);
    }, false);
    $('player').addEventListener('wheel', function (e) {
      if (!active) return;
      e.preventDefault(); e.stopPropagation();
      seekBy((e.deltaY || 0) > 0 ? 10 : -10);
    }, { passive: false });
  }

  /* ---- video events ---- */
  function bindVideo() {
    video.addEventListener('loadedmetadata', function () {
      if (pendingSeek > 0 && pendingSeek < (video.duration || 1e9) - 5) { try { video.currentTime = pendingSeek; } catch (e) { /* ignore */ } }
      pendingSeek = 0;
      updateTime();
      applyPreferredAudio();
    }, false);
    video.addEventListener('playing', function () { setState(''); startSaveTimer(); }, false);
    video.addEventListener('waiting', function () { setState('缓冲中…'); }, false);
    video.addEventListener('pause', function () { if (active && !video.ended) { setState('已暂停'); showOsd(true); savePos(true); } }, false);
    video.addEventListener('play', function () { setState(''); showOsd(false); }, false);
    video.addEventListener('timeupdate', function () { if (active) updateTime(); }, false);
    video.addEventListener('ended', function () { if (active) nextFile(); }, false);
    video.addEventListener('error', function () {
      if (!active) return;
      var code = video.error && video.error.code;
      var why = code === 4 ? '格式不支持或源不可达' : code === 3 ? '解码失败' : code === 2 ? '网络错误' : '错误 ' + (code || '?');
      lastErr = why;
      setState('播放失败（' + why + '）\n按返回键退出');
      showOsd(true);
      stopSaveTimer();
    }, false);
  }

  /* ---- keys ---- */
  function startSeekHold(dir) {
    seekBy(dir * SEEK_STEP);
    if (seekHold.timer) return;
    seekHold.dir = dir;
    seekHold.timer = setInterval(function () { seekBy(seekHold.dir * SEEK_STEP); }, 400);
  }
  function stopSeekHold() { if (seekHold.timer) { clearInterval(seekHold.timer); seekHold.timer = null; } }

  function onKey(code, e) {
    if (!active) return false;
    if (code !== 37 && code !== 39) stopSeekHold();
    if (subPanel) {
      switch (code) {
        case 38: case 40: case 13: showOsd(true); return Nav.handleKey(code);
        case 37: if (Subs.active()) { subSetOffset(Subs.offset() - 500); renderSubPanel(subSaved, focusedSubKind(), focusedSubUrl()); } return true;
        case 39: if (Subs.active()) { subSetOffset(Subs.offset() + 500); renderSubPanel(subSaved, focusedSubKind(), focusedSubUrl()); } return true;
        case 404: case 461: case 27: closeSubPanel(); return true;
      }
      return true;
    }
    if (osdFocus) {
      switch (code) {
        case 37: case 39: case 13: showOsd(true); return Nav.handleKey(code);
        case 38: case 40: case 461: case 27: leaveStrip(); return true;
      }
    }
    showOsd(false);
    switch (code) {
      case 13: togglePlay(); return true;
      case 37: if (e && e.repeat) seekBy(-SEEK_STEP); else startSeekHold(-1); return true;
      case 39: if (e && e.repeat) seekBy(SEEK_STEP); else startSeekHold(1); return true;
      case 38: case 40: enterStrip(); return true;
      case 415: if (video.paused) togglePlay(); return true;
      case 19: if (!video.paused) togglePlay(); return true;
      case 10252: togglePlay(); return true;
      case 404: openSubPanel(); return true;
      case 405: cycleAudio(); return true;
      case 413: video.pause(); savePos(true); App.back(); return true;
      case 417: seekBy(60); return true;
      case 412: seekBy(-60); return true;
      case 461: case 27: App.back(); return true;
    }
    return false;
  }
  function focusedSubKind() { var c = Nav.current(); var it = c && c.el ? itemOf(c.el) : null; return it ? it.kind : 'off'; }
  function focusedSubUrl() { var c = Nav.current(); var it = c && c.el ? itemOf(c.el) : null; return it && it.kind === 'saved' ? it.sub.url : ''; }
  function onKeyUp(e) { var code = e.keyCode || e.which; if (code === 37 || code === 39) stopSeekHold(); }

  /* Entering from the phone ("play" message) only carries hash+index: fetch the file list first. */
  function resolveFiles(params) {
    if (params.files && params.files.length) return Promise.resolve(params.files);
    return API.getFull(params.hash).then(function (st) {
      var fs = (st && st.file_stats) || [], out = [];
      for (var i = 0; i < fs.length; i++) if (VIDEO.test(fs[i].path)) out.push(fs[i]);
      out.sort(function (a, b) { return a.path < b.path ? -1 : a.path > b.path ? 1 : 0; });
      if (st && st.title && !params.title) title = st.title;
      return out;
    });
  }

  App.register('player', {
    enter: function (params) {
      if (!video) {
        video = $('video'); bindVideo(); bindAudio(); bindPointer(); document.addEventListener('keyup', onKeyUp, false);
        Subs.attach(video, $('subtitle'), $('subtitle2'));
        App.Remote.on(function (ev) { if (ev && ev.type === 'sub') onRemoteSub(ev); });
      }
      active = true;
      hash = params.hash;
      title = params.title || '';
      files = [];
      $('osd-stat').textContent = '';
      setState('加载中…');
      resolveFiles(params).then(function (fs) {
        if (!active) return;
        files = fs;
        if (!files.length) { setState('种子里没有视频文件\n按返回键退出'); showOsd(true); return; }
        var i = params.index ? fileById(params.index) : -1;
        if (i < 0) i = 0;
        fileIdx = i;
        renderTitle();
        renderEps();
        zones();
        startStat();
        var start = params.startTime || 0;
        if (!start) {
          try { var m = JSON.parse(window.localStorage.getItem('torr.pos.' + hash) || '{}'); if (m && m[files[i].id] > 30) start = m[files[i].id]; } catch (e) { /* ignore */ }
        }
        loadFile(i, start);
      }, function (err) {
        if (!active) return;
        setState('读取种子失败：' + err.message + '\n按返回键退出');
        showOsd(true);
      });
    },
    leave: function () {
      active = false;
      stopSeekHold();
      stopSaveTimer();
      stopStat();
      if (video) {
        if (video.currentTime > 3) savePos(true);
        try { video.pause(); } catch (e) { /* ignore */ }
        video.removeAttribute('src');
        try { video.load(); } catch (e2) { /* ignore */ }
      }
      setState('');
      UI.removeClass($('osd'), 'show');
      UI.addClass($('osd-audio'), 'hidden');
      audioN = 0;
      if (subPanel) { subPanel = false; UI.addClass($('osd-subs'), 'hidden'); }
      Subs.clear();
      renderSubBadge();
      subResults = null;
      subSaved = [];
      osdFocus = false;
    },
    subtitle: function () { var sl = Subs.slots(); return { current: sl[0], second: sl[1], offset: Subs.offset(), cues: Subs.count(0), cues2: Subs.count(1), panel: subPanel, items: subItems.length, text: $('subtitle').textContent, text2: $('subtitle2').textContent }; },
    openSubs: function () { openSubPanel(); },
    audioTracks: function () { var t = audioTracks(), out = []; if (t) for (var i = 0; i < t.length; i++) out.push({ language: t[i].language || '', label: t[i].label || '', enabled: !!t[i].enabled, name: trackName(t[i], i) }); return out; },
    setAudio: function (i) { setAudio(i, true); },
    key: onKey
  });
})();
