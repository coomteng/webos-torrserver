/* api.js — XMLHttpRequest wrappers for TorrServer and the torr-remote sidecar. ES5 only.
 *
 * On the TV the app is loaded from file:// and needs explicit addresses; tools/build_ipk.sh
 * substitutes the two placeholders. When served by the sidecar (http://<sidecar>:8791/tv/) the
 * sidecar is the page origin and TorrServer's address comes from GET /config. */
var API = (function () {
  'use strict';

  var fromHttp = (window.location.protocol === 'http:' || window.location.protocol === 'https:');
  var SIDECAR = fromHttp ? window.location.protocol + '//' + window.location.host : 'http://SIDECAR_HOST:8791';
  var TS = fromHttp ? '' : 'http://TORRSERVER_HOST:8090';   /* '' until /config answers */
  var DEFAULT_TIMEOUT = 20000;

  function request(method, url, body, timeoutMs) {
    return new Promise(function (resolve, reject) {
      var xhr = new XMLHttpRequest();
      var done = false;
      function fail(msg) {
        if (done) return;
        done = true;
        var err = new Error(msg);
        err.url = url;
        reject(err);
      }
      try { xhr.open(method, url, true); } catch (e) { return fail('open failed: ' + e.message); }
      xhr.timeout = timeoutMs || DEFAULT_TIMEOUT;
      xhr.onreadystatechange = function () {
        if (xhr.readyState !== 4 || done) return;
        if (xhr.status >= 200 && xhr.status < 300) {
          var data = null;
          if (xhr.responseText) {
            try { data = JSON.parse(xhr.responseText); } catch (e) { return fail('bad JSON from ' + url); }
          }
          done = true;
          resolve(data);
        } else if (xhr.status === 0) {
          fail('network error');
        } else {
          var msg = 'HTTP ' + xhr.status;
          try { var j = JSON.parse(xhr.responseText); if (j && j.error) msg += ': ' + j.error; } catch (e2) { /* ignore */ }
          fail(msg);
        }
      };
      xhr.ontimeout = function () { fail('timeout'); };
      xhr.onerror = function () { fail('network error'); };
      try {
        if (body !== undefined && body !== null) {
          xhr.setRequestHeader('Content-Type', 'application/json');
          xhr.send(JSON.stringify(body));
        } else {
          xhr.send();
        }
      } catch (e3) { fail('send failed: ' + e3.message); }
    });
  }

  /* ---- TorrServer (https://github.com/YouROK/TorrServer, server/web/api) ---- */
  function torrents(body, timeoutMs) { return request('POST', TS + '/torrents', body, timeoutMs); }

  function list() { return torrents({ action: 'list' }, 8000); }
  function get(hash) { return torrents({ action: 'get', hash: hash }, 8000); }
  function add(link, title) { return torrents({ action: 'add', link: link, title: title || '', save_to_db: true }, 30000); }
  function remove(hash) { return torrents({ action: 'rem', hash: hash }, 8000); }
  function drop(hash) { return torrents({ action: 'drop', hash: hash }, 8000); }
  /* A torrent idle for TorrentDisconnectTimeout (30 s) goes to stat 5 "in db": list/get then carry no
   * file_stats. "add" with the bare hash wakes it from the DB in ~0.1 s and returns the full status. */
  function wake(hash) { return torrents({ action: 'add', link: hash, save_to_db: true }, 30000); }
  function getFull(hash) {
    return get(hash).then(function (st) {
      if (st && st.file_stats && st.file_stats.length) return st;
      return wake(hash);
    });
  }

  /* Viewed marks (TorrServer keeps them per hash+file index; timecode only if TrackTimecode is on). */
  function viewedList(hash) { return request('POST', TS + '/viewed', { action: 'list', hash: hash }, 8000); }
  function viewedSet(hash, index, timecode) { return request('POST', TS + '/viewed', { action: 'set', hash: hash, file_index: index, timecode: timecode || 0 }, 8000); }

  /* Direct stream URL for <video src>. /stream?...&play keeps TorrServer's reader logic (read-ahead,
   * preload) and returns HTTP 206 ranges, which the webOS pipeline uses for seeking. */
  function streamUrl(hash, index, name) {
    var fname = encodeURIComponent((name || 'video').replace(/[\/\\]/g, '_'));
    return TS + '/stream/' + fname + '?link=' + encodeURIComponent(hash) + '&index=' + index + '&play';
  }

  function echo() { return request('GET', TS + '/echo', null, 5000); }

  /* ---- sidecar ---- */
  function config() {
    if (!fromHttp) return Promise.resolve({ torrserver: TS });
    return request('GET', SIDECAR + '/config', null, 8000).then(function (c) {
      if (c && c.torrserver) TS = String(c.torrserver).replace(/\/+$/, '');
      return c;
    });
  }
  function remoteNew(id) { return request('POST', SIDECAR + '/remote/new', { id: id || '' }, 8000); }
  function remotePoll(id, since) { return request('GET', SIDECAR + '/remote/' + id + '/poll?since=' + (since || 0), null, 32000); }
  function remoteState(id, st) { return request('POST', SIDECAR + '/remote/' + id + '/state', st, 8000); }
  function remoteQr(id) { return SIDECAR + '/remote/' + id + '/qr.svg'; }

  return {
    list: list, get: get, getFull: getFull, wake: wake, add: add, remove: remove, drop: drop, viewedList: viewedList, viewedSet: viewedSet,
    streamUrl: streamUrl, echo: echo,
    config: config, remoteNew: remoteNew, remotePoll: remotePoll, remoteState: remoteState, remoteQr: remoteQr,
    torrServer: function () { return TS; }, sidecar: function () { return SIDECAR; }
  };
})();
