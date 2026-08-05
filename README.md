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

- **Watch-state, not mtime.** Preload decisions come from the media server API: resume points,
  next-up episodes, recently-added, and per-user history - not filesystem timestamps.
- **Resume from the offset.** For an in-progress title it warms at the *resume byte offset*, not
  the file head, so continuing a movie is as instant as starting one.
- **Duration-based sizing.** Each preload is sized by playback seconds (derived from bitrate), so
  the warmed window always covers the spin-up gap regardless of resolution.
- **Budgeted.** You cap it at a share of RAM; page cache is reclaimable, so it never starves apps -
  the kernel evicts it under memory pressure.

## What it is

- Native Unraid `.plg` plugin: a single static Go binary (`preloadd`) + a PHP settings page.
- Supports **Emby** (Jellyfin support is on the roadmap).
- Runs as a **cron-invoked one-shot** (`preloadd -once`) like Fix Common Problems and the Mover -
  each run is a fresh sweep, so library changes are picked up every interval. An optional `--daemon`
  mode adds sub-minute reaction for those who want it.
- Settings page with server-queried **user and library pickers**, per-signal **tier dials**, an
  auto-detected Docker path-map table, a **Test connection** check, and a last-run status panel.

## Installation (Unraid plugin)

Install by URL (Plugins -> Install Plugin) from the stable "latest release" asset (it always tracks
the newest stable release's package):

```text
https://github.com/sydlexius/watch-aware-preloader/releases/latest/download/watch-aware-preloader.plg
```

To install a specific pre-release build for testing, use that release's versioned asset URL instead
(the `latest` URL only resolves to stable releases):

```text
https://github.com/sydlexius/watch-aware-preloader/releases/download/<version>/watch-aware-preloader.plg
```

On install the plugin:
- extracts the `preloadd` binary to `/usr/local/emhttp/plugins/watch-aware-preloader/`
- seeds `/boot/config/plugins/watch-aware-preloader/secrets.toml` (see the Credentials note below
  about flash file permissions) and generates `config.toml` from your settings
- installs a cron job running `preloadd -once` on the configured interval

After install, configure everything from the webGui at **Settings -> Watch-Aware Preloader**: set
the server URL, pick users and libraries, tune the signal tiers, RAM budget, target seconds, and
schedule interval, then paste your API key into the write-only API-key field (stored in
`secrets.toml`, never shown back). Use **Test connection** to verify, and **Run now** to warm
immediately. The status panel shows the last run (time in US Pacific).

`config.toml` is generated from these settings on every save and every boot - edit settings in the
webGui (or the plugin `.cfg`), not `config.toml` directly. Uninstalling removes the cron job and
binary but preserves your settings and `secrets.toml` on the flash drive.

Releases are tagged with letter-free versions (Slackware requirement), e.g. `2026.07.03`.

## Troubleshooting

**"Nothing was warmed" is usually correct.** When every item the preloader wants is already in the
page cache, the right outcome is to warm nothing, and the settings page says so: *everything already
cached, nothing needed warming*. A bar that barely moves on a healthy system means there was little
left to do, not that the plugin is broken. What it reports is what each sweep warmed, not what is
resident right now - items already cached are skipped and add nothing to the total.

**Where the logs are.** The engine logs to syslog, not to its own file:

```bash
grep watch-aware-preloader /var/log/syslog | tail -20
```

Each sweep logs a `sweep complete` line with target, preloaded, skipped, and missing counts. `missing`
is the number worth watching - see path mapping below.

**Nothing is preloaded, and `missing` is high.** The media server reports its own paths, which are
rarely the host's. If libraries were added over SMB, Emby reports UNC paths (`\\tower\Movies\...`);
if the server runs in Docker, it reports container paths (`/media/...`). The plugin maps these to
host paths automatically by inspecting the running container, and falls back to a UNC rule. When a
path cannot be mapped, the item counts as missing. To see what the mapping produced:

```bash
/usr/local/emhttp/plugins/watch-aware-preloader/preloadd detect-pathmaps \
  -config /boot/config/plugins/watch-aware-preloader/config.toml
```

It prints the rules it derived, as JSON. Add explicit rules in the settings page if auto-detection
does not cover your layout. `list-users` and `list-libraries` are the two other read-only
diagnostics, useful for confirming the server is reachable and returning what you expect. All three
are subcommands, so the name comes before the flags.

**No users or libraries to pick.** The pickers are populated by **Test connection**, so run that
first. If it fails, the server URL or API key is wrong - the key is write-only in the UI and is never
shown back, so re-paste it rather than assuming it is still set.

**Verifying it actually helped.** `-verify` runs one sweep and then reports how much of what it
warmed is still resident:

```bash
/usr/local/emhttp/plugins/watch-aware-preloader/preloadd \
  -config /boot/config/plugins/watch-aware-preloader/config.toml -verify
```

The honest end-to-end test is still a real one: let a disk spin down, then start something the
plugin warmed and see whether playback starts immediately.

## Configuration

### Credentials

The Emby API key is a secret and is kept out of `config.toml`. Provide it either:

- in a secrets file (default `/boot/config/plugins/watch-aware-preloader/secrets.toml`) under
  `[server].api_key` - see `secrets.example.toml`; or
- via the `EMBY_API_KEY` environment variable (which overrides the file).

`config.toml` must not contain `api_key`; the engine refuses to start if it does. The secrets-file
location can be overridden with the `secret_path` key in `config.toml`.

Note on the default location: `/boot` is the Unraid USB flash drive (FAT32), which does not enforce
Unix file permissions - so the secrets file is only as protected as flash/root access to the server
(the same model every Unraid plugin uses for stored credentials). Point `secret_path` at a Linux
filesystem if you want `0600` file-mode enforcement.

## License

[MIT](LICENSE).
