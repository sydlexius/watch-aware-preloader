# Contributing

Thanks for your interest. This project is in early development (pre-Phase-1).

## Getting set up

See [REQUIREMENTS.md](REQUIREMENTS.md), then:

```bash
make tools     # install golangci-lint + PHP dev deps
make build
make test
```

## Before opening a PR

Run the same checks CI runs:

```bash
make fmt        # gofmt + go mod tidy (commit the result)
make lint       # golangci-lint must pass clean
make test-race  # tests pass with the race detector
make php-lint   # only if you touched plugin/ PHP
```

## Conventions

- Go: follow the existing package boundaries (see [CLAUDE.md](CLAUDE.md)
  Architecture). Keep units decoupled and unit-testable.
- Every `//nolint` directive must carry a `// reason` (enforced by nolintlint).
- PHP (the Unraid settings page): PSR-12; run `make php-fix` before committing.
- Commits: small and focused. Don't commit secrets - `config.toml` and
  `*.local.toml` are gitignored for that reason.

## Conduct and releases

Participation is governed by the [Code of Conduct](CODE_OF_CONDUCT.md). Notable
changes per release are indexed in the [changelog](CHANGELOG.md).

## Design

Substantive changes should trace back to the project's design of record.
If you're proposing something new, open an issue describing the change first.
