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
| Go | 1.27+ | builds the `preloadd` daemon | `go.mod`, `.tool-versions` |
| make | any | task runner | - |

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
