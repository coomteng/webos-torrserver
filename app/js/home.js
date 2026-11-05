/* home.js — torrent list from TorrServer + the phone QR code. ES5 only.
 * Empty list: big QR in the middle. Otherwise: list on the left, QR panel on the right.
 * OK = open file list; RED twice = remove from TorrServer; GREEN = refresh. */
(function () {
  'use strict';

  var REFRESH_MS = 3000;
  var list = [];
  var timer = null;
  var active = false;
  var pendingDelete = null;   /* { hash, t } for the two-press red key */
  var VIDEO = /\.(mkv|mp4|m4v|avi|mov|ts|m2ts|mpg|mpeg|webm|wmv|flv|vob|iso|3gp)$/i;

  function $(id) { return UI.$(id); }

  function videoCount(t) {
    var fs = t.file_stats || [], n = 0;
    for (var i = 0; i < fs.length; i++) if (VIDEO.test(fs[i].path)) n++;
    return n;
  }

  function statText(t) {
    var parts = [];
    var pct = t.torrent_size ? Math.min(100, 100 * (t.loaded_size || 0) / t.torrent_size) : 0;
    if (t.stat === 1) parts.push('获取种子信息…');
    else if (t.stat === 2) parts.push('预读中…');
    else if (t.stat === 5) parts.push('未启动');
    if (t.torrent_size) parts.push(UI.fmtSize(t.torrent_size));
    if (t.torrent_size && t.stat !== 5) parts.push('已缓存 ' + pct.toFixed(pct < 10 ? 1 : 0) + '%');
    if (t.active_peers || t.total_peers) parts.push((t.active_peers || 0) + '/' + (t.total_peers || 0) + ' peers');
    if (t.download_speed > 1024) parts.push('↓ ' + UI.fmtSize(t.download_speed) + '/s');
    var n = videoCount(t);
    if (n > 1) parts.push(n + ' 个视频');
    return parts.join(' · ');
  }

  function render() {
    var box = $('home-list');
    var focusHash = null;
    var cur = Nav.current();
    if (cur && cur.el && cur.el.getAttribute) focusHash = cur.el.getAttribute('data-hash');
    UI.empty(box);
    for (var i = 0; i < list.length; i++) {
      var t = list[i];
      var row = UI.el('div', 'row', { 'data-hash': t.hash || '' });
      row.appendChild(UI.text('div', 'name', t.title || t.name || t.hash));
      row.appendChild(UI.text('div', 'meta', statText(t)));
      var bar = UI.el('div', 'bar');
      var fill = UI.el('div', 'fill');
      fill.style.width = (t.torrent_size ? Math.min(100, 100 * (t.loaded_size || 0) / t.torrent_size) : 0).toFixed(1) + '%';
      bar.appendChild(fill);
      row.appendChild(bar);
      if (pendingDelete && pendingDelete.hash === t.hash) UI.addClass(row, 'danger');
      box.appendChild(row);
    }
    UI.toggleClass($('home'), 'empty', list.length === 0);
    $('home-count').textContent = list.length ? list.length + ' 个种子' : '';
    /* keep focus on the same torrent across refreshes */
    var zone = zoneDef();
    var idx = 0;
    if (focusHash) for (var j = 0; j < list.length; j++) if (list[j].hash === focusHash) idx = j;
    zone.index = idx;
    App.setZones([zone], 0);
    if (!list.length) Nav.clearFocus();
  }

  function zoneDef() {
    return {
      name: 'list', cols: 1, remember: true, index: 0,
      getItems: function () { return UI.toArray($('home-list').children); },
      onFocus: function (el) { Nav.scrollY($('home-list'), el, 940, 20, 20); },
      onEnter: function (el) {
        var hash = el.getAttribute('data-hash');
        var t = byHash(hash);
        if (!t) return;
        if (t.stat === 1) { UI.toast('还在获取种子信息，请稍候'); return; }
        App.go('files', { hash: hash, title: t.title || t.name || '' });
      }
    };
  }

  function byHash(hash) {
    for (var i = 0; i < list.length; i++) if (list[i].hash === hash) return list[i];
    return null;
  }

  function sortList(l) {
    l.sort(function (a, b) { return (b.timestamp || 0) - (a.timestamp || 0); });
    return l;
  }

  function refresh(showErr) {
    API.list().then(function (l) {
      if (!active) return;
      list = sortList(l || []);
      $('home-status').textContent = '';
      render();
    }, function (err) {
      if (!active) return;
      $('home-status').textContent = '连不上 TorrServer：' + API.torrServer() + '（' + err.message + '）';
      if (showErr) App.errorToast(err, 'TorrServer');
    });
  }

  function startTimer() { stopTimer(); timer = setInterval(function () { refresh(false); }, REFRESH_MS); }
  function stopTimer() { if (timer) { clearInterval(timer); timer = null; } }

  function renderQr() {
    var img = $('home-qr'), big = $('home-qr-big');
    var src = App.Remote.qr();
    var url = (App.Remote.url() || '').replace(/^https?:\/\//, '');
    if (src) { img.src = src; big.src = src; }
    $('home-qr-url').textContent = url;
    $('home-qr-big-url').textContent = url;
    UI.toggleClass($('home-qr-panel'), 'off', !src);
  }

  function removeFocused() {
    var cur = Nav.current();
    if (!cur || !cur.el) return;
    var hash = cur.el.getAttribute('data-hash');
    var t = byHash(hash);
    if (!t) return;
    var now = new Date().getTime();
    if (pendingDelete && pendingDelete.hash === hash && now - pendingDelete.t < 4000) {
      pendingDelete = null;
      API.remove(hash).then(function () { UI.toast('已删除：' + (t.title || t.name || '')); refresh(false); }, function (err) { App.errorToast(err, '删除失败'); });
    } else {
      pendingDelete = { hash: hash, t: now };
      UI.toast('再按一次红键删除「' + (t.title || t.name || '') + '」');
      render();
      setTimeout(function () { if (pendingDelete && now === pendingDelete.t) { pendingDelete = null; if (active) render(); } }, 4000);
    }
  }

  function onKey(code) {
    if (!active) return false;
    switch (code) {
      case 403: removeFocused(); return true;                         /* red */
      case 404: refresh(true); UI.toast('已刷新'); return true;        /* green */
      case 406: UI.toggleClass($('home'), 'show-big-qr', !UI.hasClass($('home'), 'show-big-qr')); return true; /* blue: big QR */
    }
    if (UI.hasClass($('home'), 'show-big-qr')) { UI.removeClass($('home'), 'show-big-qr'); return code === 461 || code === 27; }
    return false;
  }

  App.Remote.on(function (ev) {
    if (ev === 'ready') renderQr();
    if (ev === 'refresh' && active) refresh(false);
  });

  App.register('home', {
    enter: function () {
      active = true;
      renderQr();
      render();
      refresh(true);
      startTimer();
      App.Remote.report({ view: 'home', title: '' });
    },
    leave: function () {
      active = false;
      stopTimer();
      UI.removeClass($('home'), 'show-big-qr');
    },
    key: onKey
  });
})();
