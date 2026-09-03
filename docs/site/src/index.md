# Watch-Aware Preloader

Your media server already knows what you will watch next. Your hard drives do
not, so they sit spun down, and every play starts with a multi-second stall
while a cold array disk spins up, seeks, and streams the first bytes off the
platter.

Watch-Aware Preloader closes that gap. It reads your server's own watch state
and quietly warms the files you are most likely to play next into RAM, so
playback starts the instant you press play.

[Install it :material-arrow-right:](install.md){ .md-button .md-button--primary }
[Configure it :material-arrow-right:](configuration.md){ .md-button }

## Why it matters, measured

When you press play on a title whose disk has spun down, three costs land **in
series, before the first frame**:

1. **Spin-up** - the platters reach speed from a dead stop. This is the big one.
2. **Cold seek** - the head unparks and travels to the byte offset you are
   starting from. A *resume* point deep in a file is a longer seek than the
   file's head.
3. **Cold load** - the bytes stream off the platter into the page cache before
   the player can use them.

Watch-Aware Preloader pre-pays all three ahead of time, off the playback path.
Measured on a live Unraid array of 5400 and 7200 RPM drives, 8-18 TB:

| First playback bytes | Cold array disk | Preloaded (warm) |
|---|---|---|
| Latency to first bytes | **~8.5-10 s** (spin-up + seek + load) | **~50 ms** (served from RAM) |
| Does the disk wake up? | Yes, and you wait for it | **No** - the read never touches the platter |

Both figures come from the same array. Cold spin-up, meaning the first read from
a genuinely spun-down disk, measured **~9.9 s on the 5400 RPM drives and ~8.5 s
on the 7200 RPM drives** - a 175-200x difference against the warm read.

The warm figure holds *through Unraid's FUSE layer*. Reading a preloaded range
via `/mnt/user`, which is exactly how the media server reads it, returned in
12-50 ms and left the drive **in standby**. Even the file `open` and `stat` did
not wake it. The disk only spins up when playback runs past the warmed window,
and by then it is spinning in the background rather than between you and the
first frame.

!!! note "The page cache is the product"

    This never serves or transcodes media. It only reads byte ranges so the
    Linux kernel caches them - the same shared page cache your media server
    reads from.

### Scope, measured and honest

The win applies to **direct-play and direct-stream** clients, which is how remux
libraries are actually watched: Apple TV, NVIDIA Shield, native apps. A client
that *transcodes* makes the server read the whole file off disk, which no
bounded preload can cover.

For a **mid-file resume**, the player also reads the container's cue index,
which lives at the end of the file, in order to seek. Resume and next-up targets
therefore need the file tail warmed as well as the head, which the plugin does.

## How it works

- **Watch state, not modification time.** Preload decisions come from the media
  server API - resume points, next-up episodes, recently-added, and per-user
  history - not filesystem timestamps.
- **Resume from the offset.** For an in-progress title it warms at the resume
  byte offset rather than the file head, so continuing a movie is as instant as
  starting one.
- **Duration-based sizing.** Each preload is sized by playback seconds, derived
  from the bitrate the server already reports, so the warmed window covers the
  spin-up gap whether the title is 4K or SD.
- **Budgeted.** You cap it at a share of RAM. Page cache is reclaimable, so it
  never starves applications: the kernel evicts it under memory pressure.

## What it is

- A native Unraid `.plg` plugin: one static Go binary (`preloadd`) plus a PHP
  settings page. No CGO, no runtime dependencies on the host.
- **Emby** is supported today. Jellyfin support is on the roadmap.
- Runs as a **cron-invoked one-shot** (`preloadd -once`), like Fix Common
  Problems or the Mover. Each run is a fresh sweep, so library changes are
  picked up every interval. An optional `--daemon` mode adds sub-minute
  reaction for those who want it.
- The settings page offers server-queried user and library pickers, per-signal
  tier dials, an auto-detected Docker path-map table, a connection test, and a
  last-run status panel.

## How it differs from mtime-based preloaders

The well-known [Video Preloader][vp] script by Marc Gutt guesses what to warm
from filesystem modification time. That finds recently-added files, but it
cannot know that you are three episodes into a series, or that you stopped 40
minutes into a film last night.

This plugin asks the media server instead, so what gets warmed is what someone
in the household is actually likely to play next.

[vp]: https://forums.unraid.net/topic/97982-video-preloader-avoids-hdd-spinup-latency-when-starting-a-movie-or-episode-through-plex-jellyfin-or-emby/
