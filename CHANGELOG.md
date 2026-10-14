# Changelog

Notable changes to Watch-Aware Preloader, newest first.

**GitHub Releases are the notes of record** where a release page carries full
notes: <https://github.com/sydlexius/watch-aware-preloader/releases>. Eleven
earlier pages (`2026.06.30.2` through `2026.08.05.2`) still show only the
automated one-line placeholder; their entries below come from the annotated tag
message, which is what shipped in the `.plg` changelog, plus `git log` for the
PR and issue numbers. This file is a short index, a few lines per release, so
the history is readable from a clone. Where a release page has full notes and
the two disagree, the release page wins.

## Versioning

Releases use calendar versions (CalVer) taken from the date the release is cut:

- `YYYY.MM.DD` for the first release of a day
- `YYYY.MM.DD.N` for a further release on the same day (`.1`, `.2`, ...)

Versions contain only digits and dots, because Slackware packages (which the
Unraid plugin installs) do not allow letters in the version. Pushing a version
tag publishes the release; see the Releasing section of the
[contributing guide](docs/site/src/contributing.md#releasing).

The `2026.08.05` series (`2026.08.05` through `2026.08.05.4`) was tagged on
2026-08-04 (US Pacific), a day early, before the rule above was adopted.

## 2026.09.02.1

- A directory under `/mnt` is no longer treated as a cache pool on its name alone; a pool must be a mount root (#120)
- The sweep pipeline is typed on the project's own core types, so a second media-server adapter needs no pipeline change (#137)

## 2026.09.02

- The `pluginURL` and support links in the `.plg` point at this repository, so installed copies no longer rely on a redirect to find updates (#124)
- README troubleshooting section covering logs, state files, and the diagnostic subcommands (#124)
- Go toolchain moved to 1.27.1 (#132)

## 2026.08.05.4

- The settings-page budget meter renders its projection again; its script was not being loaded (#121)
- A sweep that warmed nothing because everything was already cached now says so plainly (#121)

## 2026.08.05.3

- Files on a cache or NVMe pool are sized without the disk spin-up allowance (#113)
- The pre-push gate recovers from a stale lint cache instead of failing (#118)

## 2026.08.05.2

- A read-ahead truncated by `max_head_mb` now logs the coverage it got versus the coverage requested (#112)
- First pass at sizing pool-resident files without the spin-up allowance (#113)

## 2026.08.05.1

- The settings-page budget bar shows the last sweep when no fresh projection exists (#114)

## 2026.08.05

- Manual path-map overrides are edited as add/remove rows instead of one delimited text box (#27)
- The local pre-push gate runs the PHP, shell, and JavaScript tests CI runs (#86)

## 2026.08.04

- Per-user signal order and enrollment rank can be set from the settings page (#101)
- A user reference written with the config-file spelling binds to the real user (#100)
- `-verify` distinguishes an empty sweep and a probe failure from an unsupported platform (#94)

## 2026.07.19

- Per-user tier order with enrollment rank, warmed slot-major so one user cannot crowd out the rest (#90)
- `-estimate` warm-set projection and a settings-page cache-budget meter (#39)
- Mid-file resume warms the MKV cue index too, removing a measured spin-up stall (#71)

## 2026.07.03

- Settings page gains live user and library pickers, per-signal tier dials, and a read-only path-map table (#32)

## 2026.07.02

- Plugin name fix so the Unraid version-check spinner resolves (#38)
- Path maps are auto-derived so a sweep warms the cache (#31)

## 2026.07.01.1

- Settings and API-key persistence go through the Unraid `/update.php` root path (#30)

## 2026.07.01

- Deterministic settings and API-key persistence (#30)
- Valid plugin icon and a resolvable update-check URL (#33)

## 2026.06.30.5 (prerelease)

- Unraid settings page and status panel; no config-file editing needed (#28)
- Bounded timeout on the FUSE residency probe so a wedged mount cannot stall a sweep (#17)

## 2026.06.30.4 (prerelease)

- Cron collates on a fresh install-by-URL, not only after a reboot (#25)

## 2026.06.30.3 (prerelease)

- First fix for cron collation on a fresh install, and the `.plg` is rendered without pip in CI (#24)

## 2026.06.30.2 (prerelease)

- First packaged build: the Unraid `.plg` and cron install-by-URL (#21), published by the release workflow (#22)
