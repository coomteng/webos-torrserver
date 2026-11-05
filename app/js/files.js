/* files.js — files inside one torrent (video files only, sorted by path). ES5 only.
 * OK = play; the row remembers a resume position from localStorage. */
(function () {
  'use strict';

  var VIDEO = /\.(mkv|mp4|m4v|avi|mov|ts|m2ts|mpg|mpeg|webm|wmv|flv|vob|iso|3gp)$/i;
  var hash = '', title = '', files = [], status = null, active = false, timer = null;

  function $(id) { return UI.$(id); }

  /* Resume positions: localStorage 'torr.pos.<hash>' = { "<index>": seconds } (TorrServer's
   * TrackTimecode is off by default, so keep this on the TV). */
  function positions() {
    try { return JSON.parse(window.localStorage.getItem('torr.pos.' + hash) || '{}') || {}; } catch (e) { return {}; }
  }

  function videoFiles(st) {
    var fs = (st && st.file_stats) || [], out = [];
    for (var i = 0; i < fs.length; i++) if (VIDEO.test(fs[i].path)) out.push(fs[i]);
    out.sort(function (a, b) { return a.path < b.path ? -1 : a.path > b.path ? 1 : 0; });
    return out;
  }

  function shortName(p) {
    var s = String(p || '');
    var i = s.lastIndexOf('/');
    return i >= 0 ? s.slice(i + 1) : s;
  }

  function render() {
    var box = $('files-list');
    var cur = Nav.current();
    var focusId = cur && cur.el && cur.el.getAttribute ? cur.el.getAttribute('data-id') : null;
    UI.empty(box);
    var pos = positions();
    for (var i = 0; i < files.length; i++) {
      var f = files[i];
      var row = UI.el('div', 'file', { 'data-id': String(f.id) });
      row.appendChild(UI.text('div', 'name', shortName(f.path)));
      var meta = UI.fmtSize(f.length);
      if (pos[f.id] > 30) meta += ' · 看到 ' + UI.fmtTime(pos[f.id]);
      row.appendChild(UI.text('div', 'meta', meta));
      box.appendChild(row);
    }
    $('files-title').textContent = title;
    $('files-sub').textContent = status ? (files.length + ' 个视频 · ' + UI.fmtSize(status.torrent_size) + (status.download_speed > 1024 ? ' · ↓ ' + UI.fmtSize(status.download_speed) + '/s' : '')) : '';
    UI.toggleClass($('files-empty'), 'hidden', files.length > 0 || !status);
    var zone = {
      name: 'files', cols: 1, remember: true, index: 0,
      getItems: function () { return UI.toArray($('files-list').children); },
      onFocus: function (el) { Nav.scrollY($('files-list'), el, 860, 20, 20); },
      onEnter: function (el) { play(parseInt(el.getAttribute('data-id'), 10)); }
    };
    if (focusId) for (var j = 0; j < files.length; j++) if (String(files[j].id) === focusId) zone.index = j;
    App.setZones([zone], 0);
  }

  function play(id) {
    var f = null;
    for (var i = 0; i < files.length; i++) if (files[i].id === id) f = files[i];
    if (!f) return;
    var pos = positions();
    App.go('player', { hash: hash, index: id, title: title, files: files, startTime: pos[id] > 30 ? pos[id] : 0 });
  }

  function load() {
    API.getFull(hash).then(function (st) {
      if (!active) return;
      status = st;
      if (st && st.title) title = st.title;
      files = videoFiles(st);
      render();
    }, function (err) { if (active) App.errorToast(err, '读取种子'); });
  }

  App.register('files', {
    enter: function (params, isBack) {
      active = true;
      hash = params.hash;
      title = params.title || title || '';
      files = [];
      status = null;
      render();
      load();
      timer = setInterval(load, 5000);
      App.Remote.report({ view: 'files', title: title, hash: hash });
    },
    leave: function () { active = false; if (timer) { clearInterval(timer); timer = null; } },
    key: function (code) {
      if (code === 404) { load(); return true; }  /* green: refresh */
      return false;
    }
  });
})();
