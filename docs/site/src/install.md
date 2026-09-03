# Install

Watch-Aware Preloader installs as a native Unraid plugin. It needs Unraid 7.0.0
or newer.

## Install by URL

In the Unraid webGui, go to **Plugins → Install Plugin** and paste this URL:

```text
https://github.com/sydlexius/watch-aware-preloader/releases/latest/download/watch-aware-preloader.plg
```

That address always tracks the newest stable release, so an installed copy keeps
checking it for updates.

??? info "Installing a specific version for testing"

    The `latest` URL only ever resolves to stable releases. To install a
    particular build, use that release's versioned asset URL:

    ```text
    https://github.com/sydlexius/watch-aware-preloader/releases/download/<version>/watch-aware-preloader.plg
    ```

    Releases are tagged with letter-free versions, for example `2026.09.02`,
    because Slackware's package tooling rejects letters in a version string. A
    second release on the same day gets a `.N` suffix.

## What installing does

The plugin:

- extracts the `preloadd` binary to
  `/usr/local/emhttp/plugins/watch-aware-preloader/`
- seeds `/boot/config/plugins/watch-aware-preloader/secrets.toml` for your API
  key, and generates `config.toml` from your settings
- installs a cron job that runs `preloadd -once` on your configured interval

## First run

Everything is configured from **Settings → Watch-Aware Preloader** in the webGui.
See [Configuration](configuration.md) for what each setting does. In short:

1. Enter your media server URL and paste your API key. The key field is
   write-only: it is stored in `secrets.toml` and never shown back to you.
2. Press **Test connection**. This confirms the plugin can reach the server and
   authenticate before you tune anything else.
3. Pick the users and libraries to preload for. Both pickers are populated by
   querying your server, so they show your real users and libraries.
4. Press **Run now** to warm immediately rather than waiting for the next cron
   interval.

The status panel reports the last run, with times in US Pacific.

!!! warning "Configure through the webGui, not `config.toml`"

    `config.toml` is regenerated from your settings on every save and on every
    boot. Editing it directly means your changes are silently overwritten. Edit
    settings in the webGui, or in the plugin `.cfg` file if you must.

## Uninstalling

Removing the plugin deletes the cron job and the binary, but **preserves your
settings and `secrets.toml`** on the flash drive. Reinstalling picks up where
you left off.

## Verifying it works

The plugin logs to **syslog**, not to a file of its own:

```bash
grep watch-aware-preloader /var/log/syslog | tail
```

A healthy sweep reports how many items it considered, warmed, and skipped.
Items already resident in the page cache are skipped, so in a steady state most
sweeps warm very little - that is the system working, not failing.

If a sweep is not doing what you expect, the first three things to check:

1. **Test connection** in the settings page. Most "nothing warmed" reports are a
   server URL or API key problem rather than a preloading one.
2. The **path map**. The plugin warms host paths, so if your media server's view
   of a path was not translated correctly, nothing resolves. The settings page
   auto-detects these from the container.
3. Whether the items were **already resident**. A sweep that reports everything
   skipped is a healthy steady state, not a failure.

A dedicated troubleshooting page is coming in the next documentation slice.
