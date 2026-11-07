/* app.js — view stack, key dispatch, phone channel (QR + long-poll), boot. ES5 only. */
var App = (function () {
  'use strict';

  var views = {};          /* name -> { enter(params, isBack), leave(), key(code, e) -> bool, reset() } */
  var stack = [];
  var activeName = null;

  /* ------------------------------------------------------------ views */
  function showDom(name) {
    var all = document.querySelectorAll('.view');
    for (var i = 0; i < all.length; i++) UI.toggleClass(all[i], 'active', all[i].id === name);
    UI.toggleClass(UI.$('topbar'), 'hidden', name === 'player');
  }

  function activate(entry, isBack) {
    var prev = activeName;
    if (prev && views[prev] && views[prev].leave && prev !== entry.name) views[prev].leave();
    activeName = entry.name;
    showDom(entry.name);
    views[entry.name].enter(entry.params || {}, !!isBack);
  }

  function go(name, params) {
    if (!views[name]) return;
    stack.push({ name: name, params: params || {} });
    activate(stack[stack.length - 1], false);
  }

  /* Jump straight to a view from anywhere (phone said "play"): the stack becomes home > [files] > target. */
  function goFresh(name, params) {
    stack = [{ name: 'home', params: {} }];
    if (name === 'player' && params && params.hash) stack.push({ name: 'files', params: { hash: params.hash } });
    if (name !== 'home') stack.push({ name: name, params: params || {} });
    activate(stack[stack.length - 1], false);
  }

  function back() {
    if (stack.length <= 1) {
      try { window.close(); } catch (e) { /* ignore */ }
      return;
    }
    stack.pop();
    activate(stack[stack.length - 1], true);
  }

  function currentName() { return activeName; }
  function setZones(zones, startZone) { Nav.setContext({ zones: zones, zone: startZone || 0 }); }
  function register(name, view) { views[name] = view; }

  /* ------------------------------------------------------------ key dispatch */
  function isNavKey(code) {
    return code === 37 || code === 38 || code === 39 || code === 40 || code === 13 || code === 461;
  }

  function onKeyDown(e) {
    var code = e.keyCode || e.which;
    var view = views[activeName];
    if (!view) return;
    var handled = false;
    if (view.key) handled = view.key(code, e);
    if (!handled) {
      if (code === 461 || code === 27) { back(); handled = true; }
      else handled = Nav.handleKey(code);
    }
    if (handled || isNavKey(code)) { if (e.preventDefault) e.preventDefault(); }
  }

  function errorToast(err, what) {
    var msg = (what || '请求失败') + '：' + (err && err.message ? err.message : '未知错误');
    if (err && /network|timeout/.test(err.message || '')) msg += '（' + (err.url || '') + '）';
    UI.toast(msg, 5000);
  }

  /* ------------------------------------------------------------ phone channel */
  var Remote = (function () {
    var id = '', seq = 0, url = '', polling = false, gen = 0, listeners = [];
    var lastState = null;   /* re-sent after (re)registering: a restarted sidecar forgets what the TV is doing */
    var backoff = 1000;

    function start() {
      var saved = '';
      try { saved = window.localStorage.getItem('torr.remoteId') || ''; } catch (e) { /* ignore */ }
      API.remoteNew(saved).then(function (r) {
        if (!r || !r.id) return;
        id = r.id; seq = r.seq || 0; url = r.url || '';
        try { window.localStorage.setItem('torr.remoteId', id); } catch (e2) { /* ignore */ }
        backoff = 1000;
        if (lastState) API.remoteState(id, lastState).then(null, function () {});
        notify('ready');
        if (!polling) poll(++gen);
      }, function () {
        /* sidecar down: retry with backoff; the app still works without the phone channel */
        setTimeout(start, backoff);
        backoff = Math.min(backoff * 2, 30000);
      });
    }

    function poll(g) {
      if (g !== gen || !id) { polling = false; return; }
      polling = true;
      API.remotePoll(id, seq).then(function (r) {
        if (g !== gen) { polling = false; return; }
        if (r && r.seq) seq = r.seq;
        var msgs = (r && r.messages) || [];
        for (var i = 0; i < msgs.length; i++) handle(msgs[i]);
        poll(g);
      }, function (err) {
        if (g !== gen) { polling = false; return; }
        if (/HTTP 404/.test(err.message || '')) { polling = false; id = ''; start(); return; }  /* sidecar restarted: re-register */
        setTimeout(function () { poll(g); }, 3000);
      });
    }

    function handle(m) {
      if (m.action === 'play' && m.hash) {
        UI.toast('手机：播放 ' + (m.title || ''));
        goFresh('player', { hash: m.hash, index: m.index || 0, title: m.title || '' });
      } else if (m.action === 'added') {
        notify('refresh');
      } else if (m.action === 'sub' && m.hash && m.url) {
        notify({ type: 'sub', hash: m.hash, index: m.index || 0, url: m.url, title: m.title || '' });
      }
    }

    function notify(ev) { for (var i = 0; i < listeners.length; i++) listeners[i](ev); }
    function on(fn) { listeners.push(fn); }
    function report(st) { lastState = st; if (id) API.remoteState(id, st).then(null, function () {}); }

    return { start: start, on: on, report: report,
      id: function () { return id; }, url: function () { return url; }, qr: function () { return id ? API.remoteQr(id) + '?v=' + id : ''; } };
  })();

  /* ------------------------------------------------------------ bootstrap */
  function showVersion() {
    var el = UI.$('app-version');
    if (!el) return;
    var xhr = new XMLHttpRequest();
    xhr.open('GET', 'appinfo.json', true);
    xhr.onreadystatechange = function () {
      if (xhr.readyState !== 4) return;
      try { var v = JSON.parse(xhr.responseText).version; if (v) el.textContent = 'v' + v; } catch (e) { /* ignore */ }
    };
    try { xhr.send(); } catch (e2) { /* ignore */ }
  }

  function init() {
    showVersion();
    UI.clock();
    setInterval(UI.clock, 15000);
    document.addEventListener('keydown', onKeyDown, false);
    Nav.installPointer();
    window.addEventListener('scroll', function () { window.scrollTo(0, 0); }, false);
    API.config().then(null, function () {}).then(function () {
      Remote.start();
      goFresh('home', {});
    });
  }

  return {
    register: register, go: go, goFresh: goFresh, back: back, setZones: setZones,
    currentName: currentName, errorToast: errorToast, Remote: Remote, init: init,
    view: function (name) { return views[name]; },
    stackNames: function () { var n = []; for (var i = 0; i < stack.length; i++) n.push(stack[i].name); return n; }
  };
})();

document.addEventListener('DOMContentLoaded', function () { App.init(); }, false);
