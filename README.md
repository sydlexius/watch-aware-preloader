# Watch-Aware Preloader

**Your media server already knows what you'll watch next. Your hard drives don't - so they sit
spun down, and every play starts with a multi-second stall while a cold array disk spins up,
seeks, and streams the first bytes off the platter.** Watch-Aware Preloader closes that gap: it
reads your server's own watch state and quietly warms the files you're most likely to play next
into RAM, so playback starts the instant you hit play - not seconds later.

Unlike the popular [Video Preloader][vp] script by Marc Gutt (which guesses from filesystem
modification time), this derives intent from your media server's watch state - resume points,
next-up episodes, recently-added, and what each household user has been watching - and sizes each
preload by playback *duration*, not a fixed byte count, so a 4K title and an SD one both cover the
spin-up window.

[vp]: https://forums.unraid.net/topic/97982-video-preloader-avoids-hdd-spinup-latency-when-starting-a-movie-or-episode-through-plex-jellyfin-or-emby/

**Full documentation: <https://sydlexius.github.io/watch-aware-preloader/>**

## Why it matters (measured)

When you press play on a title whose disk has spun down, three costs land **in series, before the
first frame**:

1. **Spin-up** - the platters have to reach speed from a dead stop (the big one).
2. **Cold seek** - the head unparks and travels to the byte offset you're starting from; a *resume*
   point deep in a file is a longer seek than the file's head.
3. **Cold load** - the bytes stream off the platter into the page cache before the player can use them.

Watch-Aware Preloader pre-pays all three *ahead of time*, off the playback path. Measured on a live
Unraid array (a mix of 5400 and 7200 RPM WD drives, 8-18 TB):

| First playback bytes | Cold array disk | Preloaded (warm) |
|---|---|---|
| Latency to first bytes | **~8.5-10 s** (spin-up + seek + load) | **~50 ms** (served from RAM) |
| Does the disk wake up? | Yes - and you wait for it | **No** - the read never touches the platter |

Both figures are measured on the same array. Cold spin-up (first read from a genuinely spun-down
disk): **~9.9 s on the 5400 RPM drives, ~8.5 s on the 7200 RPM drives** - a ~175-200x difference
versus the warm read. The warm figure holds *through Unraid's FUSE layer* - reading a preloaded
range via `/mnt/user` (exactly how the media server reads it) returned in ~12-50 ms and left the
drive **in standby**. Even the file `open`/`stat` didn't wake it. The disk only spins up when
playback runs *past* the warmed window - and by then it's spinning in the background, no longer
between you and the first frame.

**Scope (measured, honest):** the win applies to **direct-play / direct-stream** clients - how
remux libraries are actually watched (Apple TV, Shield, native apps). A client that *transcodes*
makes the server read the whole file off disk, which no bounded preload can cover. And for a
*mid-file resume*, the player also reads the container's cue index (at the end of the file) to
seek - so resume/next-up targets need the file tail warmed too, not just the head.

> Note: this never serves or transcodes media. It only reads byte ranges to make the Linux kernel
> cache them - the same shared page cache your media server reads from. The page cache is the product.

## How it works

- **Watch state, not modification time.** Preload decisions come from the media server API:
  resume points, next-up episodes, recently-added, and per-user history.
- **Resume from the offset.** An in-progress title is warmed at the resume byte offset, plus the
  file tail, so the player can usually read the container cue index without waking the disk. (An
  oversized cue index is currently only partly covered - see
  [#143](https://github.com/sydlexius/watch-aware-preloader/issues/143).)
- **Duration-based sizing.** Each preload is sized by playback seconds derived from bitrate, so
  the warmed window covers the spin-up gap at any resolution.
- **Budgeted.** You cap it at a share of RAM; page cache is reclaimable, so it never starves apps.

Full detail: **[How it works](https://sydlexius.github.io/watch-aware-preloader/how-it-works/)**.

## What it is

- Native Unraid `.plg` plugin: a single static Go binary (`preloadd`) + a PHP settings page.
- Supports **Emby** (Jellyfin support is on the roadmap).
- Runs as a **cron-invoked one-shot** (`preloadd -once`) like Fix Common Problems and the Mover -
  each run is a fresh sweep, so library changes are picked up every interval. An optional `--daemon`
  mode adds sub-minute reaction for those who want it.
- Settings page with server-queried **user and library pickers**, per-signal **tier dials**, an
  auto-detected Docker path-map table, a **Test connection** check, and a last-run status panel.

## Installation

Install by URL (Plugins -> Install Plugin):

```text
https://github.com/sydlexius/watch-aware-preloader/releases/latest/download/watch-aware-preloader.plg
```

That address always tracks the newest stable release. After installing, configure everything at
**Settings -> Watch-Aware Preloader**: server URL, API key, users, libraries, tiers, and schedule.

Full walkthrough: **[Install](https://sydlexius.github.io/watch-aware-preloader/install/)**.

## Configuration

Settings live in the webGui. `config.toml` is generated from them on every save and boot, so edit
settings there rather than the file. The API key is a secret and is kept out of `config.toml`,
in `secrets.toml` on the flash drive (or the `EMBY_API_KEY` environment variable).

Settings reference, defaults, and the flash-drive permission caveat:
**[Configuration](https://sydlexius.github.io/watch-aware-preloader/configuration/)**.

## Troubleshooting

The engine logs to **syslog**, not its own file:

```bash
grep watch-aware-preloader /var/log/syslog | tail -20
```

"Nothing was warmed" is usually correct - already-resident items are skipped. If `missing` is high,
the path map is the first thing to check.

Full guide, including the read-only diagnostic subcommands:
**[Troubleshooting](https://sydlexius.github.io/watch-aware-preloader/troubleshooting/)**.

## Contributing

Requirements, architecture, and the testing conventions:
**[Contributing](https://sydlexius.github.io/watch-aware-preloader/contributing/)**.
See also [REQUIREMENTS.md](REQUIREMENTS.md) and [CONTRIBUTING.md](CONTRIBUTING.md).

## License

[MIT](LICENSE).
