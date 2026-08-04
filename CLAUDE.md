# Watch-Aware Preloader - Claude Code Project Instructions

## >> ON SESSION START / RESUME: read SESSION-STATE.md FIRST <<

`SESSION-STATE.md` (repo root; local-only, git-excluded) is the running checkpoint -
current status, the immediate next action, and gotchas. Read it before doing anything
when asked to "resume" or "pick up where we left off".

Plugin display name: **Watch-Aware Preloader**. Binary: `preloadd`. Repo/Go-module
slug stays kebab-case `watch-aware-preloader`.

## Project Overview

An Unraid plugin that warms the Linux page cache with the media each household
user is most likely to play next, so playback starts instantly instead of waiting
8-10 seconds for an array disk to spin up. Unlike the popular [Video Preloader](https://forums.unraid.net/topic/97982-video-preloader-avoids-hdd-spinup-latency-when-starting-a-movie-or-episode-through-plex-jellyfin-or-emby/)
script by Marc Gutt (which guesses from filesystem mtime), this derives intent from the media
server's own watch state (Emby/Jellyfin): resume points, next-up episodes,
recently-added, and what each user has been watching.

The approved design and working docs are maintained privately (the gitignored
`docs/private/` tree: specs, plans, verification) and are not published to the
repo. The maintainer reads the design spec there before implementing; contributors
without that tree should open an issue to discuss the design of record. Phase 1
(Go engine MVP, Emby, file config) is the first deliverable.

**Run model: cron-invoked one-shot is primary** (like Fix Common Problems / Mover),
not a resident service. `preloadd -once` (also the default) does one full sweep and
exits; cron re-invokes it each interval to pick up library changes. `--daemon` is an
optional resident loop (periodic sweep + `/Sessions` poll) for sub-interval reaction;
`-verify` runs one sweep then reports page-cache residency. `cmd/preloadd` chose this
over a supervised service to avoid init-script/restart/update complexity.

## Style and Conventions

- **Keep every GitHub-facing surface G-rated and professional** - commit messages, PR
  titles/bodies, issue titles/bodies, bot-review replies, branch names, release
  notes, labels. No profanity, crude language, or informal slang (e.g. "kludge",
  "hack", "sucks"); prefer "workaround", "defect", "does not work". When quoting
  maintainer feedback into an issue or PR, paraphrase the slang out. This is a
  public repo; keep the record clean.
- **Go 1.26+**, `net/http` stdlib (no third-party router), `log/slog` for logging.
- Single static binary `preloadd` - no CGO, no runtime deps on the Unraid host.
- Internally decoupled units, each independently testable (see Architecture).
- **PHP** (Phase 2 settings page only): PSR-12, kept under `plugin/`. Lint with
  PHPStan (AST/static) + PHP-CS-Fixer (style); both cover `.php` and Unraid `.page`.
- Pin GitHub Actions to commit SHAs (with `# vX`), job-level least-privilege
  `permissions:`, `persist-credentials: false` on checkout.

## Architecture (target)

The load-bearing units inside the `preloadd` binary. This is a map of the main
seams, not an inventory - `internal/` also holds `app` (sweep orchestration),
`core`, `pagecache`, `estimate`, `libscope`, `container`, `secrets`, and
`sysinfo`. Run `ls internal/` for the full list rather than trusting this block
to be exhaustive.

There is deliberately NO warm-set ledger: whether a range is already warm is
decided per sweep by probing residency (mincore, or a read-timing probe on
FUSE), not by remembering what a previous run warmed. An earlier version of this
section listed a ledger under `internal/config`; it was never built, and the
absence is what makes an external `drop_caches` re-warm correctly instead of
being masked by stale bookkeeping.

```
cmd/preloadd/        - entry point + run modes (-once default, --daemon, -verify)
internal/mediaserver/ - WatchProvider interface + Emby/Jellyfin adapters
                        (auth, fetch Resume/NextUp/Latest/Sessions, subscribe)
internal/scorer/      - pure: per-user signals -> ranked, deduped []PreloadTarget
internal/preloader/   - duration-based head/tail/resume-offset reads into page
                        cache; mincore warm-detection; byte-budget accounting
internal/pathmap/     - server path -> host path (docker inspect auto-detect)
internal/config/      - TOML config
internal/status/      - status.json for the settings page (aggregate run counts)
plugin/               - Phase 2: Unraid .plg tree (PHP settings page, rc.d, events)
```

The media-server client adapts patterns from the **stillwater** repo
(`~/Developer/stillwater/internal/connection/{emby,jellyfin}`,
`internal/webhook`): auth headers, SSRF-hardened URL handling
(`BuildRequestURL`/`ValidateBaseURL`), and webhook parsing. Copy/adapt - do not
add a cross-repo dependency.

## Common Commands

```bash
make build        # build bin/preloadd
make run          # build and run
make test         # Go tests
make test-race    # Go tests with race detector
make cover        # coverage report
make lint         # golangci-lint
make fmt          # gofmt + go mod tidy
make php-lint     # PHPStan + PHP-CS-Fixer dry-run (no-op until plugin/ has PHP)
make php-fix      # auto-fix PHP style
make tools        # install golangci-lint + PHP dev deps
```

See [REQUIREMENTS.md](REQUIREMENTS.md) for what to install.

## Key Rules

- **Page cache is the product.** This never serves or transcodes media; it only
  reads byte ranges to make the kernel cache them. Reads on bind-mounted paths
  warm the same host cache (shared kernel).
- **Watch-state, not mtime.** Preload decisions come from the media server API.
  Exclude items in an active playback session (already spun-up/resident).
- **Duration-based sizing.** Size each preload by playback seconds (from API
  `Bitrate`), not a fixed byte count, so 4K and SD both cover the spin-up window.
- **Resume from the offset.** For in-progress items, preload at the resume byte
  offset, not the file head.
- **Security:** API keys are secrets - keep them out of logs and out of git
  (`config.toml`/`*.local.toml` are gitignored). Validate the server base URL at
  the trust boundary (reuse stillwater's `ValidateBaseURL` rationale).

## PR Workflow

Follows the global workflow (`/prep-pr`, `/handle-review`, `/merge-pr`). CI
(`.github/workflows/ci.yml`) runs Go build/test/lint and PHP lint. Never push or
open a PR without explicit maintainer go-ahead.

## License

**MIT** (see [LICENSE](LICENSE)).

Licensing caveat: **stillwater is GPL-3.0.** Reimplement its Emby/Jellyfin client
*patterns* (auth header format, endpoint set, URL-validation strategy) in fresh
code - approaches and API shapes are not copyrightable - but do **not** paste
stillwater source verbatim into this MIT repo. If GPL code ever genuinely needs to
be incorporated, the project license must be revisited first.
