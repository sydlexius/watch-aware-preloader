# Requirements

What you need installed to build, test, and lint this project. This repo is
polyglot (Go daemon + PHP Unraid settings page), so dependencies are declared in
each stack's native manifest; this file is the human-readable summary.

If you use [asdf](https://asdf-vm.com) or [mise](https://mise.jdx.dev), the
pinned language versions live in [`.tool-versions`](.tool-versions) - run
`asdf install` (or `mise install`) to get them.

## Build / runtime

| Tool | Version | Why | Declared in |
|------|---------|-----|-------------|
| Go | 1.27.3+ | builds the `preloadd` daemon | `go.mod`, `.tool-versions`, `.github/go-version` |
| make | any | task runner | - |

CI and release builds read `.github/go-version` (`1.27.x`) with `check-latest: true`,
so they pick up each new Go PATCH release without a PR, once it appears in the
actions/go-versions manifest (if that lookup fails, setup-go falls back to the
runner's cached 1.27, and govulncheck flags a stale one). Dependabot cannot do
this: it never bumps the `go`/`toolchain` line on its own
(dependabot/dependabot-core#13520). The `go` line in `go.mod` is only the
minimum. A MINOR bump (1.27 -> 1.28) stays a deliberate manual change to
`.github/go-version`, `go.mod`, and `.tool-versions` together;
`test/go_version_lockstep_test.sh` fails if they drift apart. (The file lives
under `.github/`, not at the root as `.go-version`, because goenv and mise read a
root `.go-version` and cannot use a `1.27.x` wildcard.)

The compiled daemon is a single static binary with **no runtime dependencies**
on the Unraid host. PHP and the tooling below are only needed for development of
the Phase 2 settings page.

## Linting / code quality

| Tool | Version | Scope | Declared in |
|------|---------|-------|-------------|
| golangci-lint | v2 | Go static analysis | `.golangci.yml` |
| PHP CLI | 8.1+ | runs the PHP linters | `composer.json`, `.tool-versions` |
| Composer | 2+ | installs PHP dev tools | - |
| PHPStan | ^2.0 | PHP AST/static analysis (`.php` + `.page`) | `composer.json`, `phpstan.neon.dist` |
| PHP-CS-Fixer | ^3.64 | PHP style/auto-format | `composer.json`, `.php-cs-fixer.dist.php` |

## One-shot setup

```bash
make tools        # installs golangci-lint (go install) + PHP dev deps (composer install)
```

Or install pieces individually:

```bash
# Go linter
go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest

# PHP dev tooling (requires php + composer on PATH)
composer install
```

## Everyday commands

```bash
make build        # build the daemon
make test         # Go tests
make test-race    # Go tests with the race detector
make lint         # Go lint (golangci-lint)
make fmt          # gofmt + go mod tidy
make php-lint     # PHPStan + PHP-CS-Fixer dry-run (skips cleanly until plugin/ has PHP)
make php-fix      # auto-fix PHP style
```
