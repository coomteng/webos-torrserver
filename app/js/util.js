/* util.js — tiny DOM / formatting helpers shared by every view (ES5 only). */
var UI = (function () {
  'use strict';

  var loadingCount = 0;
  var toastTimer = null;

  function $(id) { return document.getElementById(id); }

  /* Create an element: el('div', 'card', {data-x: 1}) */
  function el(tag, className, attrs) {
    var e = document.createElement(tag);
    if (className) e.className = className;
    if (attrs) {
      for (var k in attrs) {
        if (attrs.hasOwnProperty(k)) e.setAttribute(k, attrs[k]);
      }
    }
    return e;
  }

  function text(tag, className, str) {
    var e = el(tag, className);
    e.textContent = str == null ? '' : String(str);
    return e;
  }

  function empty(node) { while (node.firstChild) node.removeChild(node.firstChild); }

  function toArray(list) {
    var out = [];
    for (var i = 0; i < list.length; i++) out.push(list[i]);
    return out;
  }

  function hasClass(e, c) { return (' ' + e.className + ' ').indexOf(' ' + c + ' ') >= 0; }
  function addClass(e, c) { if (e && !hasClass(e, c)) e.className = (e.className ? e.className + ' ' : '') + c; }
  function removeClass(e, c) {
    if (!e) return;
    e.className = (' ' + e.className + ' ').replace(' ' + c + ' ', ' ').replace(/^\s+|\s+$/g, '');
  }
  function toggleClass(e, c, on) { if (on) addClass(e, c); else removeClass(e, c); }

  /* Apply a translate transform with the -webkit- prefix (Chrome 53 is fine with both). */
  function translate(node, x, y) {
    var v = 'translate3d(' + Math.round(x) + 'px,' + Math.round(y) + 'px,0)';
    node.style.webkitTransform = v;
    node.style.transform = v;
    node.setAttribute('data-tx', String(Math.round(x)));
    node.setAttribute('data-ty', String(Math.round(y)));
  }
  function getTx(node) { return parseInt(node.getAttribute('data-tx') || '0', 10) || 0; }
  function getTy(node) { return parseInt(node.getAttribute('data-ty') || '0', 10) || 0; }

  /* Position of `node` relative to `container`, independent of the container's transform. */
  function relRect(node, container) {
    var a = node.getBoundingClientRect();
    var b = container.getBoundingClientRect();
    return { left: a.left - b.left, top: a.top - b.top, width: a.width, height: a.height,
             right: a.right - b.left, bottom: a.bottom - b.top };
  }

  function pad2(n) { return (n < 10 ? '0' : '') + n; }

  function fmtTime(sec) {
    if (!isFinite(sec) || sec < 0) sec = 0;
    sec = Math.floor(sec);
    var h = Math.floor(sec / 3600), m = Math.floor((sec % 3600) / 60), s = sec % 60;
    if (h > 0) return h + ':' + pad2(m) + ':' + pad2(s);
    return pad2(m) + ':' + pad2(s);
  }

  function fmtSize(b) {
    if (!b || b < 0) return '';
    var u = ['B', 'KB', 'MB', 'GB', 'TB'], i = 0;
    while (b >= 1024 && i < 4) { b /= 1024; i++; }
    return (i > 1 ? b.toFixed(1) : Math.round(b)) + ' ' + u[i];
  }

  function cleanText(s) {
    return String(s || '').replace(/<[^>]+>/g, '').replace(/[\u00a0\u3000\s]+/g, ' ').replace(/^\s+|\s+$/g, '');
  }

  /* Loading overlay with a counter so overlapping requests do not flicker. */
  function loading(on) {
    loadingCount += on ? 1 : -1;
    if (loadingCount < 0) loadingCount = 0;
    toggleClass($('loading'), 'show', loadingCount > 0);
  }

  function toast(msg, ms) {
    var t = $('toast');
    t.textContent = msg;
    addClass(t, 'show');
    if (toastTimer) clearTimeout(toastTimer);
    toastTimer = setTimeout(function () { removeClass(t, 'show'); }, ms || 3500);
  }

  function clock() {
    var d = new Date();
    var el = $('clock');
    if (el) el.textContent = pad2(d.getHours()) + ':' + pad2(d.getMinutes());
  }

  return {
    $: $, el: el, text: text, empty: empty, toArray: toArray,
    hasClass: hasClass, addClass: addClass, removeClass: removeClass, toggleClass: toggleClass,
    translate: translate, getTx: getTx, getTy: getTy, relRect: relRect,
    fmtTime: fmtTime, pad2: pad2, cleanText: cleanText,
    loading: loading, toast: toast, clock: clock, fmtSize: fmtSize
  };
})();
