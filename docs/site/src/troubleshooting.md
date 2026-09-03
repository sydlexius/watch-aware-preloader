# Troubleshooting

## "Nothing was warmed" is usually correct

When every item the preloader wants is already in the page cache, the right
outcome is to warm nothing. The settings page says so directly: *everything
already cached, nothing needed warming*.

A budget bar that barely moves on a healthy system means there was little left
to do, not that the plugin is broken. What it reports is **what each sweep
warmed**, not what is resident right now: items already cached are skipped and
contribute nothing to the total, so in a steady state that number trends toward
zero.

## Where the logs are

The engine logs to **syslog**, not to a file of its own:

```bash
grep watch-aware-preloader /var/log/syslog | tail -20
```

Each sweep logs a `sweep complete` line with `targets`, `preloaded`,
`skipped`, `missing`, `bytes_warmed` and `by_tier`. **`missing` is the number worth watching** - see path mapping
below.

## Nothing is preloaded, and `missing` is high

This is almost always path mapping. The media server reports its own paths,
which are rarely the host's:

- Libraries added over SMB make Emby report UNC paths (`\\tower\Movies\...`)
- A server running in Docker reports container paths (`/media/...`)

The plugin maps these to host paths automatically by inspecting the running
container, and falls back to a UNC rule. When a path cannot be mapped, the item
counts as missing.

To see what the mapping actually produced:

```bash
/usr/local/emhttp/plugins/watch-aware-preloader/preloadd detect-pathmaps \
  -config /boot/config/plugins/watch-aware-preloader/config.toml
```

It prints the rules it derived, as JSON. Add explicit rules in the settings page
if auto-detection does not cover your layout.

!!! tip "The diagnostics are subcommands"

    `detect-pathmaps`, `list-users` and `list-libraries` are subcommands, so the
    name comes **before** the flags. `preloadd -detect-pathmaps` is not a flag
    and will not work.

## No users or libraries to pick

The pickers are populated by **Test connection**, so run that first.

If it fails, the server URL or the API key is wrong. The key is write-only in
the UI and is never shown back, so re-paste it rather than assuming it is still
set.

## Verifying it actually helped

`-verify` runs one sweep and then reports how much of what it warmed is still
resident:

```bash
/usr/local/emhttp/plugins/watch-aware-preloader/preloadd \
  -config /boot/config/plugins/watch-aware-preloader/config.toml -verify
```

The honest end-to-end test is still a real one: let a disk spin down, then start
something the plugin warmed and see whether playback begins immediately.

## Other read-only diagnostics

| Command | What it answers |
|---|---|
| `list-users` | Is the server reachable, and which users does it report? |
| `list-libraries` | Which libraries does the server expose? |
| `detect-pathmaps` | What path rules did auto-detection derive? |

All three are read-only and safe to run at any time. They are the fastest way to
confirm the plugin sees what you expect before looking at sweep behavior.

## Where things live

| Path | What it is |
|---|---|
| `/boot/config/plugins/watch-aware-preloader/` | Settings, `config.toml`, `secrets.toml`. Survives uninstall. |
| `/usr/local/emhttp/plugins/watch-aware-preloader/` | The `preloadd` binary and the settings page. |
| `/var/local/preloadd/` | Runtime state: `status.json` and `estimate.json`. |
| syslog | All log output. There is no dedicated log file. |

!!! warning "`/usr/local/emhttp` does not survive a reboot"

    That path is on tmpfs. The plugin is re-extracted there from the flash drive
    on every boot, so editing a file in place is lost at the next restart. Your
    settings live on `/boot` and do persist.
