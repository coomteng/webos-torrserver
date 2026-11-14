# webos-torrserver — 电视磁力

A [TorrServer](https://github.com/YouROK/TorrServer) client for old LG webOS TVs (tested on an
OLED55B8, webOS 4.4 / Chrome 53), plus a tiny Go sidecar so you can drive it from your phone:

1. The TV shows a QR code.
2. Scan it with your phone, paste a magnet link (or infohash / `.torrent` URL).
3. Tap **在电视上观看** — the TV starts playing a few seconds later, while TorrServer caches the
   torrent on the fly. **缓存（不播放）** just adds it; the TV list shows cache progress, peers and speed.

[中文说明](README.zh-CN.md)

```
phone  --scan QR-->  http://<host>:8090/tr/r/<id> (phone page, served by torr-remote inside TorrServer; :8791/r/<id> standalone)
  |-- POST /torrents {add}  ----------------------> TorrServer :8090   (directly; CORS is open)
  |-- POST /tr/remote/<id>/send {play,hash,index} --> torr-remote --long-poll--> TV app
TV app (ES5, com.ting.torr)
  |-- POST /torrents {list,get}  ----------------> TorrServer
  |-- <video src="http://<host>:8090/stream/<name>?link=<hash>&index=<n>&play">
```

The sidecar never proxies media or API calls; it only holds the phone→TV channel in memory and
serves the static pages. TorrServer stays stock.

## Components

| dir | what |
| --- | --- |
| `sidecar/` | Go, stdlib + `skip2/go-qrcode` + `x/text`. `/remote/*` channel, `/r/<id>` phone page, `/title` Chinese→English titles (Wikidata), `/tv/` app hosting, `/sim` browser simulator, `/config`, `/subs/*` external subtitles (`sidecar/subs/`). |
| `app/` | TV app. No build step, ES5 only (`tools/es5check.sh`). `nav.js` (focus + Magic Remote pointer) is copied from [moontv-webos](https://github.com/coomteng/moontv-webos). |
| `sim/` | 1920×1080 iframe + on-screen remote for desktop testing. |
| `test/search.js` | Phone-page torrent search end-to-end (real sidecar + fake TorrServer). |
| `test/walk.js` | Headless end-to-end walk against a live sidecar + TorrServer (puppeteer-core). |
| `test/audio.js` | Self-contained headless check of audio-track switching (ffmpeg fixture + fake TorrServer). |
| `test/subs.js` | Headless check of external subtitles: real sidecar binary + fake assrt.net + fake TorrServer. |
| `compose.yml` | TorrServer + sidecar as one stack. |

## Run the server side

```sh
ASSRT_TOKEN=<your assrt.net token> docker compose up -d
```

Then open `http://<host>:8090` once and set TorrServer to cache on disk (Settings → *Use disk*,
path `/opt/ts/torrents`), or POST the settings as the stack on the reference box does:
`CacheSize` 256 MiB, `UseDisk` true, `RemoveCacheOnDrop` true, `PreloadCache` 10, `ReaderReadAHead` 95,
`ResponsiveMode` true. TorrServer runs without auth here — keep it on the LAN.

With the fork image in `compose.yml`, `http://<host>:8090/tr/sim` is the simulator and
`http://<host>:8090/tr/tv/` the app itself in a browser (standalone sidecar: `:8791/` and `:8791/tv/`).

### How the two fit together

`sidecar/` is also a Go library (`package torrremote`, import path
`github.com/coomteng/webos-torrserver/sidecar`): `torrremote.New(cfg)` + `app.Handler()` is an
`http.Handler` that can be mounted under a prefix (`cfg.BasePath`). The TorrServer fork
[coomteng/TorrServer](https://github.com/coomteng/TorrServer) does exactly that under `/tr`, so one
container on `:8090` serves TorrServer, the TV app (`/tr/tv/`), the phone page (`/tr/r/<id>`),
subtitles and pre-caching; no second port, no CORS, no address to configure. That is the deployed
setup since 2026-11-08 (`compose.yml`, TV app built with `SIDECAR_BASE=http://<host>:8090/tr`).
The standalone binary (`sidecar/cmd/torr-remote`) and the `ghcr.io/coomteng/webos-torrserver`
image keep working unchanged for anyone running stock TorrServer on `:8791`; the fork's Docker
build also takes the TV app files from that image.

## Install on the TV

```sh
cp local.env.example local.env   # TORRSERVER_BASE / SIDECAR_BASE as the TV sees them
tools/build_ipk.sh               # ES5 gate, substitutes the placeholders, ares-package
ares-install -d lgtv dist/com.ting.torr_*.ipk
```

Developer Mode / `ares-setup-device` notes are the same as for moontv-webos.

## Remote keys

| view | keys |
| --- | --- |
| home | OK open · ↑↓ move · red ×2 delete from TorrServer · green refresh · blue big QR |
| files | OK play (resumes if watched before) · green refresh |
| player | OK play/pause · ←→ ±10 s (hold to repeat) · ↑↓ file strip · green subtitles · yellow next audio track · ⏪⏩ ±60 s (→ / OK / ⏩ skip the dead block when stalled) · Back exit |
| subtitle panel | ↑↓ choose · OK apply · ←→ delay ∓0.5 s · green/Back close |

Magic Remote: hover focuses, click activates, wheel scrolls / seeks, click on the seek bar seeks.

## Notes on TorrServer behaviour

- A torrent idle for `TorrentDisconnectTimeout` (30 s) drops to state 5 "in db"; `list`/`get` then
  return no `file_stats`. `{"action":"add","link":"<hash>"}` wakes it in ~0.1 s and returns the full
  status — `API.getFull()` and the phone page do exactly that.
- `/stream/<name>?link=<hash>&index=<n>&play` answers HTTP 206 ranges; seeking in the webOS pipeline
  works through that. File `index` is TorrServer's 1-based `file_stats[].id`.
- Audio tracks: multi-audio MKV/MP4 show a `♪ 音轨 n/N · 语言` badge in the OSD; the yellow key (or clicking the badge)
  cycles `video.audioTracks[i].enabled`. The chosen language is remembered (`torr.lang`) and auto-selected for the next
  file. Needs `HTMLMediaElement.audioTracks` — webOS ≥ 4 has it; desktop Chrome only with `--enable-blink-features=AudioVideoTracks`.
- External subtitles: the green key opens a panel with the subtitles already stored for this file (pick up to **two**:
  the first goes to the bottom, the second to the top of the screen — e.g. 简中 + 英文 for language learning; 交换上下 swaps
  them), an online search (assrt.net / 射手网伪, query derived from the release name) and a delay adjuster. The sidecar downloads
  the chosen file, decodes GBK/Big5/UTF-16, converts SRT/ASS (and zips of them) to WebVTT and keeps it under
  `TORR_SUBS_DIR/<hash>/<index>/`; the TV fetches the `.vtt` and draws cues itself (no `<track>`: unreliable on the
  webOS video plane, and we want delay + bilingual stacking). The phone page has a 字幕 button per file for the same
  search plus uploading a local `.srt/.ass/.zip`; the TV loads it immediately if it is playing that file. The choice
  (or "off") and the delay are remembered per file in `localStorage` (`torr.sub.<hash>`). Needs `ASSRT_TOKEN`
  (free account at assrt.net; the quota is ~5 requests/min, the sidecar spaces calls 15 s apart and caches searches).
- Swarm health: every torrent row (TV home and phone page) carries a tag derived from `connected_seeders` /
  `active_peers` / `total_peers` of `list`: green **健康 · N 做种** (at least one complete peer, so it can finish),
  yellow **无做种者 · 可能下不完** (peers but nobody has everything: all of them wait for the same missing pieces),
  red **没有 peer**. Finished and idle torrents show nothing.
- Torrent search (phone page): a collapsible 搜索种子 block under the paste box queries TorrServer's Torznab proxy
  directly (`GET /torznab/search/?query=`; enable Torznab and add indexers in TorrServer settings). Results are sorted by
  seeders (max 50) and carry the same health tag as the torrent list (健康 / 无做种者 / 没有 peer, from the indexer's
  Seed/Peer snapshot, which may lag). 观看 = add → wait for metadata → play the biggest video, 缓存 = add →
  background pre-cache of the biggest video — the same flow as a pasted magnet (`Magnet`, else `Link`).
- Background pre-caching: TorrServer only downloads what a reader asks for and drops an idle torrent after 30 s, so
  a torrent nobody watches does not download. The phone page's 后台缓存（不播放） / per-card 缓存 buttons
  register `{hash,index}` with the sidecar (`POST /precache`), the per-file 缓存 button sends `{hash,indexes:[id]}`
  and so appends that file to the torrent's queue; for each file in turn the sidecar issues `GET /stream?link=&index=&preload&stat` —
  TorrServer's own sequential preload (state 2 "Torrent preload", exempt from the idle timeout; with the usual
  `CacheSize`/`PreloadCache` it covers the whole file). The request returns when the preload ends. 停止缓存 and the
  header switch **后台缓存：开/关** send the fork's `{"action":"preload_stop"}` ([coomteng/TorrServer](https://github.com/coomteng/TorrServer)),
  which ends the preload without dropping the torrent (a TV streaming it is unaffected). The switch keeps the list
  and resumes from the pieces on disk when turned on again. The list lives in `TORR_DATA_DIR/precache.json`
  (set it: TorrServer forgets running preloads on restart; the sidecar re-sends unfinished items on boot and
  retries every 30 s while TorrServer is unreachable). Torrents run in parallel; the files of one torrent run one at
  a time in queue order (TorrServer preloads one file per torrent). Each item of `GET /precache` lists them as
  `files:[{index,done,error?}]`; its own `index`/`done`/`error` describe the file being cached (`done` = all files).
  `POST /precache {hash,index}` replaces the queue with that file, `{hash,indexes:[n,...]}` appends (a finished file
  is queued again at the end; duplicates count once; `title` only names an item that has none). `indexes` wins over
  `index` when both are sent (`indexes:null` counts as not sent) and must hold 1..5000 file ids ≥ 1, else 400. A
  queued id the torrent does not have is dropped once TorrServer lists the files. `POST /precache/stop {hash}` drops
  the torrent, with `index` or `indexes` only those files (the next one starts; dropping the last file removes the
  item). `pending:true` on stop spares finished files: only not-done files among `index`/`indexes` go — or, without
  them, every not-done file of the torrent — so a client acting on a stale list cannot erase a `done` mark; the item
  is removed only when no file is left. The phone card shows `· done/total` for an item with several files.
  No rate limit or disk cap — see TODO.
  Finished torrents stop seeding (`StopSeedingWhenCached`) and close 30 s later; the pieces stay on disk.
- Chinese titles: the indexers only have English release names, and TPB answers an unmatched query with its top-100.
  A query containing Han characters first goes to the sidecar's `GET /title?q=` (Wikidata entity search in Chinese →
  English label + year of film/series items; cached 24 h, 1 s spacing, 30 s back-off on 429). The newest released title
  is searched, the others become chips; unknown titles ask for the English name. A result must contain every word of
  the search (stopwords and words with digits are optional), which also drops TPB's top-100 fallback. Known miss: YTS
  lists some Asian films under romanized original titles (e.g. Liú Làng Dì Qiú) and those are filtered out.
- Dead blocks: when the player has been `waiting` for 12 s with no seeder, the app polls `POST /cache {action:get}`
  for the first piece ahead of the stream reader that is not `Completed` (`Readers[].Reader` vs `Pieces`, which also
  lists partial pieces). If that piece has not grown for 12 s it is dead — overall download speed is ignored, since
  TorrServer keeps reading ahead into other pieces at full speed. The run is converted into seconds via the file's
  average bitrate and shown as 「卡在没人有的数据块 #30（共 101 块，无做种者）· 按 → 或 OK 跳过约 1 分钟」. →, OK or ⏩ then seek
  past it (the Magic Remote has no ⏩; otherwise they are +10 s / pause / +60 s). webOS eventually gives up on a stalled stream with a decode error; the prompt then
  appears immediately (「播放中断：…」) and the skip reopens the stream past the dead block (any ←→ seek on an
  errored element reopens the stream too). One press per dead run.
- Cold start: if webOS reports error 4 before the first frame (TorrServer still fetching metadata), the player
  retries once before showing the error.
- Resume positions live in the TV's `localStorage` (`torr.pos.<hash>`), since TorrServer's own
  `TrackTimecode` is off by default.

## License

MIT.
