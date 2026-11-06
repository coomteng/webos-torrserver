/* subs.js — external subtitle rendering for the player. ES5 only.
 * The sidecar already converted everything to WebVTT; here we fetch the text, parse cues, and
 * draw the active ones on a timer (not <track>: webOS renders video on a hardware plane and native
 * text tracks are unreliable there, and we want an adjustable delay and two languages at once).
 * Two slots: 0 = primary (bottom, big), 1 = secondary (top of the screen, smaller) — e.g. 简中 + 英文. */
var Subs = (function () {
  'use strict';

  var TICK = 150;
  var slots = [newSlot(), newSlot()];
  var offset = 0;          /* ms added to video time (positive = subtitles appear later), shared */
  var timer = null;
  var video = null;
  var boxes = [null, null];
  var timeRe = /(?:(\d{1,2}):)?(\d{2}):(\d{2})\.(\d{3})\s*-->\s*(?:(\d{1,2}):)?(\d{2}):(\d{2})\.(\d{3})/;

  function newSlot() { return { sub: null, cues: [], lastKey: '' }; }
  function ms(h, m, s, f) { return ((parseInt(h || '0', 10) * 60 + parseInt(m, 10)) * 60 + parseInt(s, 10)) * 1000 + parseInt(f, 10); }

  /* Parse WebVTT text into cues [{s, e, t}] sorted by start. Tolerates CRLF, identifiers, settings. */
  function parse(text) {
    var out = [];
    var blocks = String(text || '').replace(/\r\n?/g, '\n').split(/\n{2,}/);
    for (var i = 0; i < blocks.length; i++) {
      var lines = blocks[i].split('\n');
      var ti = -1, m = null;
      for (var k = 0; k < lines.length && k < 3; k++) { m = timeRe.exec(lines[k]); if (m) { ti = k; break; } }
      if (!m) continue;
      var s = ms(m[1], m[2], m[3], m[4]), e = ms(m[5], m[6], m[7], m[8]);
      var t = [];
      for (var j = ti + 1; j < lines.length; j++) if (lines[j]) t.push(lines[j]);
      if (!t.length || e <= s) continue;
      out.push({ s: s, e: e, t: t.join('\n') });
    }
    out.sort(function (a, b) { return a.s - b.s; });
    return out;
  }

  /* Largest cue index with s <= t (binary search), then walk back a little for overlapping cues. */
  function at(cues, t) {
    var lo = 0, hi = cues.length - 1, idx = -1;
    while (lo <= hi) { var mid = (lo + hi) >> 1; if (cues[mid].s <= t) { idx = mid; lo = mid + 1; } else hi = mid - 1; }
    var hit = [];
    for (var i = idx; i >= 0 && i > idx - 4; i--) if (cues[i].s <= t && t < cues[i].e) hit.unshift(cues[i]);
    return hit;
  }

  /* Minimal inline markup: <i>, <b> kept; everything else escaped; newlines -> <br>. */
  function html(t) {
    return t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/&lt;(\/?)(i|b)&gt;/g, '<$1$2>').replace(/\n/g, '<br>');
  }

  function draw(n, t) {
    var sl = slots[n], box = boxes[n];
    if (!box) return;
    if (!sl.cues.length) { if (sl.lastKey !== '') { sl.lastKey = ''; box.innerHTML = ''; UI.removeClass(box, 'show'); } return; }
    var hit = at(sl.cues, t);
    var key = '';
    for (var i = 0; i < hit.length; i++) key += hit[i].s + ':' + hit[i].t + '|';
    if (key === sl.lastKey) return;
    sl.lastKey = key;
    if (!hit.length) { box.innerHTML = ''; UI.removeClass(box, 'show'); return; }
    var parts = [];
    for (var k = 0; k < hit.length; k++) parts.push(html(hit[k].t));
    box.innerHTML = '<span>' + parts.join('<br>') + '</span>';
    UI.addClass(box, 'show');
  }

  function tick() {
    if (!video) return;
    var t = Math.round(video.currentTime * 1000) - offset;
    draw(0, t);
    draw(1, t);
  }
  function anyCues() { return slots[0].cues.length > 0 || slots[1].cues.length > 0; }
  function start() { stop(); timer = setInterval(tick, TICK); tick(); }
  function stop() { if (timer) { clearInterval(timer); timer = null; } }
  function resetSlot(n) { slots[n] = newSlot(); if (boxes[n]) { boxes[n].innerHTML = ''; UI.removeClass(boxes[n], 'show'); } }

  /* ---- public ---- */
  function attach(v, primaryEl, secondaryEl) { video = v; boxes = [primaryEl, secondaryEl]; }

  /* Load a VTT into slot n; sub = {url, name, label}. Resolves to the cue count. */
  function load(n, sub) {
    resetSlot(n);
    var sl = slots[n];
    sl.sub = sub;
    return API.subsText(sub.url).then(function (text) {
      if (slots[n] !== sl) return 0;
      sl.cues = parse(text);
      if (anyCues()) start(); else stop();
      tick();
      return sl.cues.length;
    });
  }

  function clear(n) {
    if (n === undefined) { resetSlot(0); resetSlot(1); stop(); return; }
    resetSlot(n);
    if (!anyCues()) stop();
  }

  function swap() {
    var a = slots[0], b = slots[1];
    slots = [b, a];
    slots[0].lastKey = ''; slots[1].lastKey = '';
    tick();
  }

  function setOffset(v) { offset = v; slots[0].lastKey = ''; slots[1].lastKey = ''; tick(); }

  return {
    attach: attach, load: load, clear: clear, swap: swap, parse: parse,
    setOffset: setOffset, offset: function () { return offset; },
    current: function (n) { return slots[n === undefined ? 0 : n].sub; },
    count: function (n) { return slots[n === undefined ? 0 : n].cues.length; },
    active: function () { return !!slots[0].sub || !!slots[1].sub; },
    slots: function () { var o = []; for (var i = 0; i < 2; i++) o.push(slots[i].sub ? { url: slots[i].sub.url, name: slots[i].sub.name || '', label: slots[i].sub.label || '', cues: slots[i].cues.length } : null); return o; }
  };
})();
