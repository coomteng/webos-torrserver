/* subs.js — external subtitle rendering for the player. ES5 only.
 * The sidecar already converted everything to WebVTT; here we fetch the text, parse cues, and
 * draw the active one into #subtitle on a timer (not <track>: webOS renders video on a hardware
 * plane and native text tracks are unreliable there, and we want an adjustable delay). */
var Subs = (function () {
  'use strict';

  var TICK = 150;
  var cues = [];           /* [{s, e, t}] ms, sorted by s */
  var offset = 0;          /* ms added to video time (positive = subtitles appear later) */
  var current = null;      /* { url, name, label } or null */
  var timer = null;
  var video = null;
  var box = null;
  var lastKey = '';
  var timeRe = /(?:(\d{1,2}):)?(\d{2}):(\d{2})\.(\d{3})\s*-->\s*(?:(\d{1,2}):)?(\d{2}):(\d{2})\.(\d{3})/;

  function ms(h, m, s, f) { return ((parseInt(h || '0', 10) * 60 + parseInt(m, 10)) * 60 + parseInt(s, 10)) * 1000 + parseInt(f, 10); }

  /* Parse WebVTT text into cues. Tolerates CRLF, cue identifiers, settings after the timing. */
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
  function at(t) {
    var lo = 0, hi = cues.length - 1, idx = -1;
    while (lo <= hi) { var mid = (lo + hi) >> 1; if (cues[mid].s <= t) { idx = mid; lo = mid + 1; } else hi = mid - 1; }
    var hit = [];
    for (var i = idx; i >= 0 && i > idx - 4; i--) if (cues[i].s <= t && t < cues[i].e) hit.unshift(cues[i]);
    return hit;
  }

  /* Minimal inline markup: <i>, <b> kept; everything else stripped; newlines -> <br>. */
  function html(t) {
    return t.replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/&lt;(\/?)(i|b)&gt;/g, '<$1$2>').replace(/\n/g, '<br>');
  }

  function tick() {
    if (!video || !box || !cues.length) return;
    var t = Math.round(video.currentTime * 1000) - offset;
    var hit = at(t);
    var key = '';
    for (var i = 0; i < hit.length; i++) key += hit[i].s + ':' + hit[i].t + '|';
    if (key === lastKey) return;
    lastKey = key;
    if (!hit.length) { box.innerHTML = ''; UI.removeClass(box, 'show'); return; }
    var parts = [];
    for (var k = 0; k < hit.length; k++) parts.push(html(hit[k].t));
    box.innerHTML = '<span>' + parts.join('<br>') + '</span>';
    UI.addClass(box, 'show');
  }

  function start() { stop(); timer = setInterval(tick, TICK); tick(); }
  function stop() { if (timer) { clearInterval(timer); timer = null; } }

  /* ---- public ---- */
  function attach(v, el) { video = v; box = el; }

  /* Load a VTT by url (sidecar-relative "/subs/..."), returns a promise of the cue count. */
  function load(sub, delayMs) {
    clear();
    current = sub;
    offset = delayMs || 0;
    return API.subsText(sub.url).then(function (text) {
      if (current !== sub) return 0;
      cues = parse(text);
      lastKey = '';
      if (cues.length) start();
      return cues.length;
    });
  }

  function clear() {
    stop();
    cues = [];
    current = null;
    lastKey = '';
    if (box) { box.innerHTML = ''; UI.removeClass(box, 'show'); }
  }

  function setOffset(v) { offset = v; lastKey = ''; tick(); }

  return {
    attach: attach, load: load, clear: clear, parse: parse,
    setOffset: setOffset, offset: function () { return offset; },
    current: function () { return current; }, count: function () { return cues.length; },
    active: function () { return !!current && cues.length > 0; }
  };
})();
