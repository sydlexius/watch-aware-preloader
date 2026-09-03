# Configuration

Everything is configured from **Settings → Watch-Aware Preloader** in the Unraid
webGui. The settings there are written to `config.toml`, which is regenerated on
every save and every boot.

!!! warning "Do not edit `config.toml` directly"

    It is generated from your settings, so hand edits are silently overwritten
    on the next save or reboot. Change settings in the webGui.

## Credentials

Your media server API key is a secret and is deliberately kept out of
`config.toml`. Provide it in one of two ways:

- **A secrets file**, by default
  `/boot/config/plugins/watch-aware-preloader/secrets.toml`, under
  `[server].api_key`. The settings page writes this for you when you paste a key
  into the write-only API-key field. The repository ships
  [`secrets.example.toml`](https://github.com/sydlexius/watch-aware-preloader/blob/main/secrets.example.toml)
  showing the expected shape, for anyone configuring it by hand.
- **The `EMBY_API_KEY` environment variable**, which overrides the file.

`config.toml` must not contain `api_key`. The engine refuses to start if it does,
rather than running with a secret in a file that is not meant to hold one. You
can point somewhere else with the `secret_path` key in `config.toml`.

!!! note "Flash-drive permissions"

    The default location is on `/boot`, the Unraid USB flash drive, which is
    FAT32 and does not enforce Unix file permissions. The secrets file is
    therefore only as protected as flash and root access to the server - the
    same model every Unraid plugin uses for stored credentials.

    If you want `0600` file-mode enforcement, point `secret_path` at a Linux
    filesystem such as a cache pool.

## What gets preloaded

Preload candidates come from your media server's watch state, grouped into
signal tiers. By default the plugin works through them in this order:

| Tier | What it selects |
|---|---|
| **Resume** | Titles someone stopped partway through. Warmed at the resume byte offset, plus the file tail so the player can read the container's cue index to seek. |
| **Next up** | The next unwatched episode of a series someone is working through. |
| **Recently added** | New arrivals in the libraries you selected. |

Each tier has a dial in the settings page, so you can cap how many items it
contributes or turn it off entirely. An item that is already playing is excluded:
that disk is demonstrably awake already.

## Budget and sizing

Two of these are set in the settings page. The rest are written into
`config.toml` by the plugin with the defaults below and have no control in the
webGui yet.

| Setting | Default | Set in the webGui? | What it does |
|---|---|---|---|
| RAM percent | `50` | Yes | Share of system RAM the warm set may occupy. Page cache is reclaimable, so this never starves applications - the kernel evicts it under memory pressure. |
| Target seconds | `20` | Yes | How many seconds of playback each preload should cover. Sizing is duration-based, derived from the bitrate the server reports, so 4K and SD both cover the spin-up window. |
| Min head MB | `8` | No | Floor on the head read, so a very low-bitrate file still gets a useful warm region. |
| Max head MB | `250` | No | Ceiling on the head read. This is the real limit on very high-bitrate content. |
| Tail MB | `1` | No | How much of the file end to warm, so a mid-file seek does not stall reading the container's cue index off a cold platter. |

Target seconds is the setting most worth understanding. The measured cold
spin-up on the reference array was 8.5-9.9 seconds, so the default of 20 leaves
real headroom for seek and load on top of spin-up.

!!! note "Changing a setting with no webGui control"

    The three without a control are written to `config.toml` on every save and
    every boot, so editing that file does not persist. Until they gain controls,
    change them in the plugin's `.cfg` on the flash drive
    (`/boot/config/plugins/watch-aware-preloader/watch-aware-preloader.cfg`),
    using the `RAM_PERCENT`-style upper-case key names, then save any setting in
    the webGui to regenerate `config.toml`.

### A note on `tail_mb` and resume

For a mid-file resume the player reads the container's cue index, which lives at
the *end* of the file, before it can seek. If that region is cold the disk spins
up anyway and the resume stalls, which was measured at about 8.5 seconds.

MKV resume targets are handled precisely: the plugin parses the container and
warms the exact cue region rather than relying on `tail_mb`. The `tail_mb`
value covers the other cases - non-MKV containers, a parse failure, and the
non-resume tiers.

## Path mapping

Your media server usually sees a different path than the Unraid host does: a
container might see `/media/movies` where the host has
`/mnt/user/Movies`. The plugin has to translate, because it warms host paths.

The settings page auto-detects these mappings by inspecting your media server's
Docker container, and shows them in an editable table. Check them before your
first real sweep. If nothing is warming and the logs show unresolved paths, the
path map is the first thing to look at.

## Schedule

The plugin runs as a cron-invoked one-shot, like Fix Common Problems or the
Mover. Each run is a complete fresh sweep, so library and watch-state changes
are picked up every interval without any long-lived state.

The interval is set in the settings page. There is also an optional resident
`--daemon` mode for sub-minute reaction, which most installations do not need.

## Where things live

| Path | What it is |
|---|---|
| `/boot/config/plugins/watch-aware-preloader/` | Settings, `config.toml`, and `secrets.toml`. Preserved across uninstall. |
| `/usr/local/emhttp/plugins/watch-aware-preloader/` | The `preloadd` binary and the settings page. |
| `/var/local/preloadd/` | Runtime state: `status.json` and `estimate.json`. |
| syslog | All log output. There is no dedicated log file. |
