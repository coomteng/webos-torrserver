# webos-torrserver — 电视磁力

给老 LG webOS 电视（OLED55B8，webOS 4.4 / Chrome 53）做的 [TorrServer](https://github.com/YouROK/TorrServer)
客户端，外加一个很小的 Go sidecar，让你用手机操作：

1. 电视上显示二维码。
2. 手机扫码，粘贴磁力链接（或 40 位 infohash / `.torrent` 链接）。
3. 点「在电视上观看」，几秒后电视开始播放，TorrServer 边下边缓存。「缓存（不播放）」只添加，
   电视列表里能看到缓存进度、peers 和速度。

sidecar 不代理任何媒体或 API 请求，只在内存里保存手机→电视的通道并托管静态页；TorrServer 用官方镜像、不改源码。

## 目录

| 目录 | 内容 |
| --- | --- |
| `sidecar/` | Go（stdlib + `skip2/go-qrcode`）：`/remote/*` 通道、`/r/<id>` 手机页、`/tv/` 托管电视 APP、`/sim` 模拟器、`/config` |
| `app/` | 电视 APP，无构建、纯 ES5（`tools/es5check.sh` 门禁）。`nav.js` 来自 [moontv-webos](https://github.com/coomteng/moontv-webos) |
| `sim/` | 浏览器模拟器（1920×1080 iframe + 虚拟遥控器） |
| `test/walk.js` | puppeteer 端到端测试，对着真实 sidecar + TorrServer 跑 |
| `compose.yml` | TorrServer + sidecar 一个 stack |

## 服务端

```sh
TORR_PUBLIC=http://192.168.0.9 ASSRT_TOKEN=<assrt.net 的 token> docker compose up -d
```

然后在 TorrServer 设置里打开磁盘缓存（路径 `/opt/ts/torrents`），参考值：CacheSize 256 MiB、
UseDisk、RemoveCacheOnDrop、PreloadCache 10、ReaderReadAHead 95、ResponsiveMode。没有开 Basic Auth，只在局域网用。

## 装到电视

```sh
cp local.env.example local.env   # 填电视看到的 TORRSERVER_BASE / SIDECAR_BASE
tools/build_ipk.sh
ares-install -d lgtv dist/com.ting.torr_*.ipk
```

## 遥控器

| 页面 | 按键 |
| --- | --- |
| 首页 | 确认 打开 · ↑↓ 移动 · 红键两次 删除 · 绿键 刷新 · 蓝键 放大二维码 |
| 文件 | 确认 播放（有记录则续播）· 绿键 刷新 |
| 播放 | 确认 播放/暂停 · ←→ ±10 秒（长按连续）· ↑↓ 选文件 · 绿键 字幕 · 黄键 切换音轨 · ⏪⏩ ±60 秒 · 返回 退出 |
| 字幕面板 | ↑↓ 选择 · 确认 应用 · ←→ 字幕提前/延后 0.5 秒 · 绿键/返回 关闭 |

Magic Remote：悬停聚焦、点击确认、滚轮滚动/快进快退、点进度条跳转。

## TorrServer 行为备注

- 种子空闲 30 秒（`TorrentDisconnectTimeout`）后变成状态 5「in db」，`list`/`get` 不再带 `file_stats`；
  用 `{"action":"add","link":"<hash>"}` 可在 0.1 秒内唤醒并拿到完整状态，APP 的 `API.getFull()` 和手机页都这么做。
- `/stream/<name>?link=<hash>&index=<n>&play` 返回 HTTP 206 分片，webOS 原生播放器靠它 seek；`index` 是 1 起的 `file_stats[].id`。
- 多音轨 MKV/MP4 在 OSD 右上显示「♪ 音轨 n/N · 语言」，黄键（或点击徽标）循环切换 `video.audioTracks[i].enabled`；
  选过的语言记在 `torr.lang`，下一文件自动选同语言。依赖 `HTMLMediaElement.audioTracks`（webOS ≥ 4 有；桌面 Chrome 需 `--enable-blink-features=AudioVideoTracks`）。
- 外挂字幕：绿键打开面板——本文件已有的字幕、在线搜索（射手网伪 assrt.net，搜索词由发布名自动清洗）、延时调节。
  sidecar 负责下载、识别 GBK/Big5/UTF-16、把 SRT/ASS（含 zip 包）转成 WebVTT 存在 `TORR_SUBS_DIR/<hash>/<index>/`；
  电视取 `.vtt` 自己画（不用 `<track>`：webOS 视频硬件面上不可靠，而且要做延时和双语叠行）。手机页每个文件有「字幕」按钮，
  可搜索或上传本地 `.srt/.ass/.zip`，电视正在播该文件时立即加载。选择（含「关闭」）和延时按文件记在 `torr.sub.<hash>`。
  需要 `ASSRT_TOKEN`（assrt.net 免费注册；配额约 5 次/分，sidecar 间隔 15 秒排队并缓存搜索）。
- 续播位置存在电视 `localStorage`（`torr.pos.<hash>`），因为 TorrServer 的 `TrackTimecode` 默认关。

MIT。
