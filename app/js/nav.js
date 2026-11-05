/* nav.js — deterministic focus navigation for the remote control (ES5 only).
 * Copied from coomteng/moontv-webos app/js/nav.js (only FOCUSABLE differs).
 *
 * A view hands Nav a "context": { zones: [Zone, ...], zone: <active zone index> }.
 * Zone = {
 *   name:     'tabs' | ...                       (debug only)
 *   getItems: function () { return [el, ...]; }  focusable elements, in DOM order
 *   cols:     0 = horizontal strip (←/→ move, ↑/↓ leave the zone)
 *             1 = vertical list   (↑/↓ move, ←/→ do nothing)
 *             n = grid with n columns (←/→ within the row, ↑/↓ across rows then leave)
 *   flow:     true = wrapped chips of varying width; rows are derived from offsetTop
 *   remember: keep the last index when re-entering (default true for strips/flow, false for grids)
 *   onFocus:  function (el, i, zone) {}   e.g. scroll the container
 *   onEnter:  function (el, i, zone) {}   OK pressed
 *   onDownExit / onUpExit: function () -> true if handled (e.g. "load more")
 * }
 * Focus is expressed purely by the `.focus` class; nothing receives DOM focus except the
 * search input (deliberately, to open the IME).
 *
 * Magic Remote (pointer) support lives here too, so every view gets it for free:
 *   mousemove over a zone item  -> that item becomes the focused one (zone switches if needed)
 *   click on the focused item   -> same as OK
 *   wheel                       -> move within the zone (vertical lists / grids / flow rows) or,
 *                                  for horizontal strips, ←/→; between zones at the edges
 * Views that need extra pointer behaviour (player OSD, keyboard) call Nav.pointer.* or listen
 * to the 'nav:pointer' custom events.
 */
var Nav = (function () {
  'use strict';

  var ctx = null;
  var focusedEl = null;
  var hoverFocus = false;   /* set while a pointer hover/click is changing focus (see scroll helpers) */
  var lastHover = null;
  var lastTarget = null;

  function items(z) { return z.getItems ? z.getItems() : (z.items || []); }
  function usable(z) { return !(z.disabled && z.disabled()) && items(z).length > 0; }

  function setFocusEl(el, z, i) {
    if (!hoverFocus) { lastHover = null; lastTarget = null; }   /* key/zone-driven change: forget pointer memo */
    if (focusedEl && focusedEl !== el) UI.removeClass(focusedEl, 'focus');
    focusedEl = el;
    if (el) UI.addClass(el, 'focus');
    z.index = i;
    if (el && z.onFocus) z.onFocus(el, i, z);
  }

  function clearFocus() {
    if (focusedEl) UI.removeClass(focusedEl, 'focus');
    focusedEl = null;
  }

  function colOf(z, i) {
    if (z.cols > 1) return i % z.cols;
    if (z.flow) return 0;
    return 0;
  }

  /* Choose the index to land on when entering zone j from direction dir (+1 = moving down). */
  function enterZone(j, dir, fromCol) {
    var z = ctx.zones[j], list = items(z), n = list.length, i;
    var remember = z.remember === undefined ? !(z.cols > 1) : z.remember;
    if (remember && z.index >= 0 && z.index < n && z.index !== undefined) {
      i = z.index;
    } else if (z.cols > 1) {
      var col = Math.min(fromCol || 0, z.cols - 1);
      if (dir > 0 || dir === 0) {
        i = Math.min(col, n - 1);
      } else {
        var lastStart = Math.floor((n - 1) / z.cols) * z.cols;
        i = Math.min(lastStart + col, n - 1);
      }
    } else {
      i = Math.min(Math.max(z.index || 0, 0), n - 1);
    }
    ctx.zone = j;
    setFocusEl(list[i], z, i);
  }

  function moveZone(dir, fromCol) {
    var j = ctx.zone + dir;
    while (j >= 0 && j < ctx.zones.length && !usable(ctx.zones[j])) j += dir;
    if (j < 0 || j >= ctx.zones.length) return false;
    enterZone(j, dir, fromCol);
    return true;
  }

  /* Wrapped chips: rows derived from offsetTop, pick the nearest item horizontally. */
  function moveFlow(z, list, dx, dy) {
    var n = list.length, i = z.index;
    if (dx) {
      var ni = i + dx;
      if (ni < 0 || ni >= n) return false;
      setFocusEl(list[ni], z, ni);
      return true;
    }
    var tops = [], k;
    for (k = 0; k < n; k++) tops.push(list[k].offsetTop);
    var curTop = tops[i];
    var targetTop = null;
    for (k = 0; k < n; k++) {
      var t = tops[k];
      if (dy > 0 && t > curTop && (targetTop === null || t < targetTop)) targetTop = t;
      if (dy < 0 && t < curTop && (targetTop === null || t > targetTop)) targetTop = t;
    }
    if (targetTop === null) return false;
    var cx = list[i].offsetLeft + list[i].offsetWidth / 2, best = -1, bestD = 1e9;
    for (k = 0; k < n; k++) {
      if (tops[k] !== targetTop) continue;
      var d = Math.abs(list[k].offsetLeft + list[k].offsetWidth / 2 - cx);
      if (d < bestD) { bestD = d; best = k; }
    }
    setFocusEl(list[best], z, best);
    return true;
  }

  function move(dx, dy) {
    if (!ctx) return false;
    var z = ctx.zones[ctx.zone];
    if (!z) return false;
    var list = items(z), n = list.length, i = z.index || 0;
    if (!n) return moveZone(dy || 1, 0);
    if (i >= n) i = n - 1;

    var handled = false;
    if (z.flow) {
      handled = moveFlow(z, list, dx, dy);
    } else if (dx) {
      if (z.cols === 1) return false;
      var nx = i + dx;
      if (nx >= 0 && nx < n && (!(z.cols > 1) || Math.floor(nx / z.cols) === Math.floor(i / z.cols))) {
        setFocusEl(list[nx], z, nx);
        handled = true;
      }
      if (!handled && z.onEdge) z.onEdge(dx);
      return true; /* horizontal edges never leave the zone */
    } else if (dy) {
      if (z.cols >= 1) {
        var step = z.cols === 1 ? 1 : z.cols;
        var ny = i + dy * step;
        if (ny >= 0 && ny < n) {
          setFocusEl(list[ny], z, ny);
          handled = true;
        } else if (dy > 0 && z.cols > 1 && Math.floor(i / z.cols) < Math.floor((n - 1) / z.cols)) {
          /* partial last row: land on the last item */
          setFocusEl(list[n - 1], z, n - 1);
          handled = true;
        }
      }
    }
    if (handled) return true;
    if (dy > 0 && z.onDownExit && z.onDownExit()) return true;
    if (dy < 0 && z.onUpExit && z.onUpExit()) return true;
    if (dy) return moveZone(dy, colOf(z, i));
    return false;
  }

  function enter() {
    if (!ctx) return false;
    var z = ctx.zones[ctx.zone];
    if (!z) return false;
    var list = items(z);
    var el = list[z.index || 0];
    if (!el) return false;
    if (z.onEnter) z.onEnter(el, z.index || 0, z);
    return true;
  }

  /* Re-apply focus after the DOM of the active zone changed. */
  function refresh() {
    if (!ctx) return;
    var z = ctx.zones[ctx.zone];
    if (!z || !usable(z)) {
      for (var j = 0; j < ctx.zones.length; j++) {
        if (usable(ctx.zones[j])) { ctx.zone = j; z = ctx.zones[j]; break; }
      }
    }
    if (!z || !usable(z)) { clearFocus(); return; }
    var list = items(z);
    var i = Math.min(Math.max(z.index || 0, 0), list.length - 1);
    setFocusEl(list[i], z, i);
  }

  function setContext(c) {
    clearFocus();
    ctx = c;
    if (!ctx) return;
    if (ctx.zone === undefined) ctx.zone = 0;
    refresh();
  }

  function focusZone(j, i) {
    if (!ctx || !ctx.zones[j]) return;
    var z = ctx.zones[j];
    var list = items(z);
    if (!list.length) return;
    ctx.zone = j;
    if (i === undefined) i = z.index || 0;
    i = Math.min(Math.max(i, 0), list.length - 1);
    setFocusEl(list[i], z, i);
  }

  function focusZoneByName(name, i) {
    if (!ctx) return;
    for (var j = 0; j < ctx.zones.length; j++) {
      if (ctx.zones[j].name === name) { focusZone(j, i); return; }
    }
  }

  function handleKey(code) {
    switch (code) {
      case 37: return move(-1, 0);
      case 39: return move(1, 0);
      case 38: return move(0, -1);
      case 40: return move(0, 1);
      case 13: return enter();
    }
    return false;
  }

  /* ---- scroll helpers: translate `container` so `el` is inside the viewport ----
   * `el` must be a direct (offset) child of `container`, and `container` is position:relative. */
  /* While a focus change is caused by the pointer hovering, views must not scroll the page: the
   * pointer can only reach what is on screen, so auto-scrolling would push things out of reach.
   * Scrolling then happens only through the wheel (pointer mode) or ↑↓/←→ (key mode). */
  function scrollX(container, el, viewW, padL, padR) {
    if (hoverFocus) return;
    var tx = UI.getTx(container);
    var left = el.offsetLeft, w = el.offsetWidth;
    if (left + tx < padL) tx = padL - left;
    else if (left + w + tx > viewW - padR) tx = viewW - padR - left - w;
    if (tx > 0) tx = 0;
    UI.translate(container, tx, UI.getTy(container));
  }

  function scrollY(container, el, viewH, padT, padB) {
    if (hoverFocus) return;
    var ty = UI.getTy(container);
    var top = el.offsetTop, h = el.offsetHeight;
    if (top + ty < padT) ty = padT - top;
    else if (top + h + ty > viewH - padB) ty = viewH - padB - top - h;
    if (ty > 0) ty = 0;
    UI.translate(container, UI.getTx(container), ty);
  }

  /* Pin `el` (a direct child) to the top of its container. */
  function pinTop(container, el, offset) {
    if (hoverFocus) return;
    var ty = -(el.offsetTop) + (offset || 0);
    if (ty > 0) ty = 0;
    UI.translate(container, UI.getTx(container), ty);
  }

  /* ---------------------------------------------------------------- pointer (Magic Remote) */
  var pointerActive = false;          /* a mousemove was seen recently: show hover feedback */
  var pointerTimer = null;

  /* Focusable things all carry one of these classes. Walking up from the event target to the
   * nearest one is O(depth); only then do we look it up in the zones (O(items), but only when the
   * hovered element actually changed, not on every mousemove). */
  var FOCUSABLE = { row: 1, file: 1, btn: 1, ep: 1 };
  function closestFocusable(node) {
    var depth = 0;
    while (node && node !== document.body && depth < 8) {
      if (node.nodeType === 1 && node.className) {
        var cls = String(node.className).split(' ');
        for (var i = 0; i < cls.length; i++) if (FOCUSABLE[cls[i]]) return node;
      }
      node = node.parentNode;
      depth++;
    }
    return null;
  }

  function zoneAndIndexOf(target) {
    if (!ctx) return null;
    var el = closestFocusable(target);
    if (!el) return null;
    for (var zi = 0; zi < ctx.zones.length; zi++) {
      var z = ctx.zones[zi];
      if (!usable(z)) continue;
      var list = items(z);
      for (var i = 0; i < list.length; i++) if (list[i] === el) return { zone: zi, z: z, index: i, el: el };
    }
    return null;
  }

  var pointerLastBump = 0;
  function bumpPointerTimer() {
    /* re-arm at most every 500 ms: clearTimeout/setTimeout per mousemove is wasteful on the TV */
    var now = new Date().getTime();
    if (now - pointerLastBump < 500) return;
    pointerLastBump = now;
    if (pointerTimer) clearTimeout(pointerTimer);
    pointerTimer = setTimeout(pointerIdle, 3000);
  }
  function pointerIdle() {
    pointerActive = false;
    lastTarget = null;
    lastHover = null;
    UI.removeClass(document.body, 'pointer');
  }
  function markPointerActive() {
    pointerActive = true;
    UI.addClass(document.body, 'pointer');
    pointerLastBump = 0;
    bumpPointerTimer();
  }

  function onMouseMove(e) {
    if (!pointerActive) markPointerActive(); else bumpPointerTimer();
    if (e.target === lastTarget) return;
    lastTarget = e.target;
    var el = closestFocusable(e.target);
    if (!el || el === lastHover) return;
    var hit = zoneAndIndexOf(e.target);
    if (!hit) return;
    lastHover = hit.el;
    if (hit.el === focusedEl) return;
    ctx.zone = hit.zone;
    hoverFocus = true;
    try { setFocusEl(hit.el, hit.z, hit.index); } finally { hoverFocus = false; }
  }

  function onClick(e) {
    if (!ctx) return;
    var hit = zoneAndIndexOf(e.target);
    if (!hit) return;
    e.preventDefault();
    ctx.zone = hit.zone;
    if (hit.el !== focusedEl) {
      hoverFocus = true;
      try { setFocusEl(hit.el, hit.z, hit.index); } finally { hoverFocus = false; }
    }
    enter();
  }

  /* Wheel: one notch = one step down/up — rows on home, rows in a grid, chips rows, lists; at the
   * edge of a zone it moves to the next zone, like the ↓/↑ keys. (Horizontal movement is what the
   * pointer itself is for, so the wheel never maps to ←/→.) */
  var wheelAcc = 0;
  function onWheel(e) {
    if (!ctx) return;
    markPointerActive();
    var dy = e.deltaY || (e.detail ? e.detail * 40 : 0) || (e.wheelDelta ? -e.wheelDelta : 0);
    if (!dy) return;
    e.preventDefault();
    wheelAcc += dy;
    if (Math.abs(wheelAcc) < 40) return;
    var dir = wheelAcc > 0 ? 1 : -1;
    wheelAcc = 0;
    move(0, dir);
    lastHover = null;
    lastTarget = null;
  }

  function installPointer() {
    document.addEventListener('mousemove', onMouseMove, false);
    document.addEventListener('click', onClick, false);
    document.addEventListener('wheel', onWheel, { passive: false });
  }

  function isPointerActive() { return pointerActive; }

  return {
    setContext: setContext, refresh: refresh, move: move, enter: enter, handleKey: handleKey,
    installPointer: installPointer, isPointerActive: isPointerActive, hitTest: zoneAndIndexOf,
    focusZone: focusZone, focusZoneByName: focusZoneByName, clearFocus: clearFocus,
    scrollX: scrollX, scrollY: scrollY, pinTop: pinTop,
    current: function () { return ctx ? { zone: ctx.zones[ctx.zone], index: ctx.zones[ctx.zone] && ctx.zones[ctx.zone].index, el: focusedEl } : null; },
    focusedEl: function () { return focusedEl; }
  };
})();
