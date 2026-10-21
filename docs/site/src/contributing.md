# Contributing

The project is MIT licensed and development happens in the open at
[github.com/sydlexius/watch-aware-preloader][repo].

[repo]: https://github.com/sydlexius/watch-aware-preloader

## What you need

The repo is polyglot: a Go binary plus a PHP settings page for Unraid.
Dependencies are declared in each stack's native manifest.

The binary is named `preloadd`, but the primary run model is a cron-invoked
one-shot rather than a resident service - `--daemon` is an opt-in mode. Worth
knowing before you go looking for a supervision loop that is not the main path.

| Tool | Version | Why |
|---|---|---|
| Go | 1.27.3+ | builds the `preloadd` binary |
| make | any | task runner |
| golangci-lint | v2 | Go static analysis |
| PHP CLI | 8.1+ (8.3.0 pinned) | runs the PHP linters |
| Composer | 2+ | installs the PHP dev tools |
| PHPStan | ^2.0 | PHP static analysis (`.php` and `.page`) |
| PHP-CS-Fixer | ^3.64 | PHP style and auto-format |

Only Go and make are needed to build the binary. The PHP tooling is for working
on the settings page.

If you use [asdf](https://asdf-vm.com) or [mise](https://mise.jdx.dev), the
pinned language versions are in `.tool-versions`: run `asdf install` or
`mise install`.

## Setup

```bash
make tools    # golangci-lint via go install, plus PHP dev deps via composer
```

## Everyday commands

```bash
make build       # build the binary
make test        # Go tests
make test-race   # Go tests with the race detector
make lint        # Go lint, for the host GOOS and for linux
make fmt         # gofmt + go mod tidy
make php-lint    # PHPStan + PHP-CS-Fixer, dry run
make php-fix     # auto-fix PHP style
```

!!! warning "Always go through `make`, never a bare `go` command"

    Once `composer install` has run (via `make tools`), there is a `vendor/`
    directory at the repo root, and it belongs to **Composer**, not Go. It is
    gitignored, so a fresh clone does not have one - the failure appears only
    after you set up the PHP tooling, which is what makes it confusing.

    From then on a bare `go build` or `go test ./...` assumes any root
    `vendor/` is its own and fails with "inconsistent vendoring". Nothing is
    broken; the Makefile sets `GOFLAGS=-mod=readonly`, which is the guard.

!!! note "`make lint` runs twice, on purpose"

    A file behind `//go:build linux` is not in the build on a macOS host, so
    linting only the host platform silently skips it - no warning, just "0
    issues". CI lints on Linux, so the local gate runs both.

## The single binary

The compiled binary is one static executable with **no runtime dependencies** on the
Unraid host: no CGO, nothing to install alongside it. That is deliberate, since
an Unraid plugin cannot assume a package manager or persistent system state.

## Architecture

The load-bearing units inside `preloadd`. This is a map of the main seams, not a
full inventory - run `ls internal/` for everything.

| Package | Responsibility |
|---|---|
| `cmd/preloadd` | Entry point and run modes: `-once` (default), `--daemon`, `-verify` |
| `internal/app` | Sweep orchestration, and the `Provider` interface the pipeline is typed on |
| `internal/core` | The shared domain types `Provider` is expressed in, so the pipeline names no vendor type |
| `internal/mediaserver` | Provider implementations: the Emby adapter, with auth and the resume / next-up / latest / sessions fetches |
| `internal/scorer` | Pure: per-user signals to a ranked, deduplicated target list |
| `internal/preloader` | Duration-based head, tail and resume-offset reads; residency probing; byte-budget accounting |
| `internal/container` | Parses MKV to locate the cue index, so a resume target warms the real cue region |
| `internal/diskresolve` | Resolves a union-share path to the array member holding it, and whether that member is a pool |
| `internal/pathmap` | Media-server path to host path, auto-detected from the container |
| `internal/config` | TOML configuration |
| `internal/status` | `status.json` for the settings page |
| `plugin/` | The Unraid `.plg` tree: PHP settings page, rc.d scripts, events |

`ls internal/` is the authoritative list; the table above names the seams, not
every package.

Two design decisions are worth knowing before changing anything:

- **There is no warm-set ledger, and never was.** Whether a range is already
  warm is decided per sweep by probing residency, not by remembering what a
  previous run did. That absence is what makes an external `drop_caches`
  re-warm correctly rather than being masked by stale bookkeeping.
- **Uncertainty resolves toward the array.** Anywhere the code cannot determine
  something - whether a member is a pool, whether a path maps, whether a device
  spins - it answers conservatively. A wrong conservative answer spends a little
  cache budget; a wrong optimistic one reintroduces the stall the project exists
  to remove.

## Testing conventions

Tests are expected to be able to fail. The question to ask of any new test is
**"when can this NOT fail?"** - a guard that passes against a broken
implementation is worse than no guard, because it advertises coverage that does
not exist.

In practice that means verifying by mutation: break the behavior the test
claims to protect, confirm that specific test reddens, then restore. Several
defects in this repo were found exactly that way, including a wired-up predicate
that was permanently inert while every test passed.

## Pull requests

CI runs the Go build, tests, lint, `govulncheck`, CodeQL, the PHP lint, and the
plugin packaging smoke tests. Most of those also run locally via
`scripts/pre-push-gate.sh`, so a PR should not be the first place a failure
shows up. `govulncheck` is not part of the pre-push gate: run `make vulncheck`
yourself (CI runs it too). CodeQL is CI-only.

Bot review runs on every PR. Findings get a fix, a tracked deferral, or a
written rebuttal - never a silent drop.

## Releasing

Releases are cut by the maintainer by pushing a version tag. There is no
version file to bump: the tag name is the version.

### Version numbers

Versions are calendar dates, using the real date the release is cut:

- `YYYY.MM.DD` for the first release of the day, for example `2026.09.02`
- `YYYY.MM.DD.N` for each further release that same day, for example
  `2026.09.02.1` - never the next day's date

Versions are digits and dots only. Slackware, whose package format the plugin
installs with, does not allow letters in a version, so a suffix such as `-rc1`
is not available.

### Cutting a release

Tag the commit on `main` with an annotated tag and push the tag:

```bash
git tag -a 2026.09.02 -m "Short summary of what this release changes"
git push origin 2026.09.02
```

Tag from an up-to-date `main`; nothing in the workflow checks that the tagged
commit is the current tip of `main`.

The tag message matters: it becomes the changelog shown in the Unraid plugin
manager, so write it for the people installing the update.

### What the release workflow does

Pushing a tag that starts with a digit runs `.github/workflows/release.yml`,
which:

1. Builds the static `linux/amd64` `preloadd` binary into the plugin tree
   (`plugin/build.sh`), stamped with the tag as its version, using the latest
   patch of the Go line named in `.github/go-version` (for example `1.27.x`).
   The workflow sets `check-latest`, so the exact patch is not pinned.
2. Packages `src/` as `watch-aware-preloader-<version>-noarch-1.txz` and
   computes its SHA-256 checksum.
3. Renders `watch-aware-preloader.plg` from `plugin/plugin.j2`, filling in the
   version, the checksum, and the tag message as the `<CHANGES>` block (or
   `Release <version>` if the message is empty). The render fails if the version
   or checksum is missing from the result, or if the `.plg` is not well-formed
   XML.
4. Refuses to publish unless the tag is strictly numeric with three or four
   dot-separated parts.
5. Creates a draft GitHub Release titled with the version, attaches the `.txz`,
   its `.sha256`, and the `.plg`, then publishes it. Drafting first is required
   because releases in this repository are immutable once published, so assets
   cannot be added afterwards.

A published release is immutable. If the workflow fails after the tag push,
check the release state before deleting anything
(`gh release view <tag> --json isDraft`), because the publish step can succeed
on GitHub even when the workflow reports an error. Only if the release is still
a draft, delete it and the tag (locally and on the remote) and tag again. If it
is already published, keep the tag and do not retry; fix the problem in a new
release instead.

Every release is published as the latest release, never as a prerelease (the
four `2026.06.30.x` builds were prereleases under an earlier version of the
workflow). Installed copies check
`releases/latest/download/watch-aware-preloader.plg` for updates, and GitHub
leaves prereleases out of `releases/latest`.

The workflow writes a one-line placeholder as the release notes. Replace it by
hand with the full notes on the release page, which is then the record of what
the release contains; nothing enforces this step, and several older releases
still show only the placeholder. `CHANGELOG.md` at the repository root is a
short index that points back to those pages.
