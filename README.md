# webos-torrserver — 电视磁力

A [TorrServer](https://github.com/YouROK/TorrServer) client for old LG webOS TVs (tested on an
OLED55B8, webOS 4.4 / Chrome 53), plus a tiny Go sidecar so you can drive it from your phone:

1. The TV shows a QR code.
2. Scan it with your phone, paste a magnet link (or infohash / `.torrent` URL).
3. Tap **在电视上观看** — the TV starts playing a few seconds later, while TorrServer caches the
   torrent on the fly. **缓存（不播放）** just adds it; the TV list shows cache progress, peers and speed.

[中文说明](README.zh-CN.md)

```
phone  --scan QR-->  http://<host>:8791/r/<id>    (phone page, served by the sidecar)
  |-- POST /torrents {add}  ----------------------> TorrServer :8090   (directly; CORS is open)
  |-- POST /remote/<id>/send {play,hash,index} --> sidecar :8791 --long-poll--> TV app
TV app (ES5, com.ting.torr)
  |-- POST /torrents {list,get}  ----------------> TorrServer
  |-- <video src="http://<host>:8090/stream/<name>?link=<hash>&index=<n>&play">
```

The sidecar never proxies media or API calls; it only holds the phone→TV channel in memory and
serves the static pages. TorrServer stays stock.

## Components

| dir | what |
| --- | --- |
| `sidecar/` | Go, stdlib + `skip2/go-qrcode` + `x/text`. `/remote/*` channel, `/r/<id>` phone page, `/tv/` app hosting, `/sim` browser simulator, `/config`, `/subs/*` external subtitles (`sidecar/subs/`). |
| `app/` | TV app. No build step, ES5 only (`tools/es5check.sh`). `nav.js` (focus + Magic Remote pointer) is copied from [moontv-webos](https://github.com/coomteng/moontv-webos). |
| `sim/` | 1920×1080 iframe + on-screen remote for desktop testing. |
| `test/walk.js` | Headless end-to-end walk against a live sidecar + TorrServer (puppeteer-core). |
| `test/audio.js` | Self-contained headless check of audio-track switching (ffmpeg fixture + fake TorrServer). |
| `test/subs.js` | Headless check of external subtitles: real sidecar binary + fake assrt.net + fake TorrServer. |
| `compose.yml` | TorrServer + sidecar as one stack. |

## Run the server side

```sh
TORR_PUBLIC=http://192.168.0.9 ASSRT_TOKEN=<your assrt.net token> docker compose up -d
```

Then open `http://<host>:8090` once and set TorrServer to cache on disk (Settings → *Use disk*,
path `/opt/ts/torrents`), or POST the settings as the stack on the reference box does:
`CacheSize` 256 MiB, `UseDisk` true, `RemoveCacheOnDrop` true, `PreloadCache` 10, `ReaderReadAHead` 95,
`ResponsiveMode` true. TorrServer runs without auth here — keep it on the LAN.

`http://<host>:8791/` is the simulator, `http://<host>:8791/tv/` the app itself in a browser.

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
| player | OK play/pause · ←→ ±10 s (hold to repeat) · ↑↓ file strip · green subtitles · yellow next audio track · ⏪⏩ ±60 s · Back exit |
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
- External subtitles: the green key opens a panel with the subtitles already stored for this file, an online
  search (assrt.net / 射手网伪, query derived from the release name) and a delay adjuster. The sidecar downloads
  the chosen file, decodes GBK/Big5/UTF-16, converts SRT/ASS (and zips of them) to WebVTT and keeps it under
  `TORR_SUBS_DIR/<hash>/<index>/`; the TV fetches the `.vtt` and draws cues itself (no `<track>`: unreliable on the
  webOS video plane, and we want delay + bilingual stacking). The phone page has a 字幕 button per file for the same
  search plus uploading a local `.srt/.ass/.zip`; the TV loads it immediately if it is playing that file. The choice
  (or "off") and the delay are remembered per file in `localStorage` (`torr.sub.<hash>`). Needs `ASSRT_TOKEN`
  (free account at assrt.net; the quota is ~5 requests/min, the sidecar spaces calls 15 s apart and caches searches).
- Resume positions live in the TV's `localStorage` (`torr.pos.<hash>`), since TorrServer's own
  `TrackTimecode` is off by default.

## License

MIT.
