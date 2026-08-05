# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| latest (main) | Yes |

This project is pre-release. Only the latest commit on main receives security updates.

## Reporting a Vulnerability

**Please do not report security vulnerabilities through public GitHub issues.**

Use [GitHub Security Advisories](https://github.com/sydlexius/watch-aware-preloader/security/advisories/new)
to report vulnerabilities privately. This ensures the issue can be triaged and
a fix prepared before public disclosure.

When reporting, please include:

- A description of the vulnerability and its potential impact
- Steps to reproduce or a proof-of-concept
- The affected version(s) or commit(s)
- A suggested fix, if you have one

You should receive an initial acknowledgment within 72 hours. Critical issues
will be addressed as quickly as practical.

## Automated security scanning

The project runs several automated scans in CI (the workflows use SHA-pinned
GitHub Actions and least-privilege permissions):

| Tool | Scope | Where |
| ---- | ----- | ----- |
| **CodeQL** | Static analysis (SAST): dataflow/taint on Go | `.github/workflows/codeql.yml` (PR, push to main, weekly) |
| **govulncheck** | Known vulnerabilities in Go dependencies and reachable stdlib | `make vulncheck` + the `Go Vulncheck` CI job |
| **Native fuzzing** | Adversarial-input properties at trust boundaries (path map, config load, base-URL validation) | `.github/workflows/fuzz.yml` (weekly + on demand) |
| **dependency-review** | Blocks PRs that introduce dependencies with high/critical severity advisories | `.github/workflows/dependency-review.yml` |
| **OpenSSF Scorecard** | Supply-chain / repository security posture | `.github/workflows/scorecard.yml` |
| **Dependabot** | Dependency update PRs | `.github/dependabot.yml` |

### SAST engine choice

**CodeQL is the chosen SAST engine.** With the repository public, CodeQL code
scanning runs for free and provides the deepest dataflow/taint analysis of the
options considered. Semgrep and Psalm were evaluated and are intentionally **not**
adopted at this time; they remain optional future additions if broader
multi-language or PHP-specific coverage is ever needed. Go code quality/style is
additionally covered by `golangci-lint`.

PHP support is deferred to the Phase 2 settings page: PHP quality/style will be
covered by PHPStan and PHP-CS-Fixer, and PHP SAST will extend CodeQL's language
matrix with its PHP extractor (which keys on `.php`; Unraid `.page` files will
need extension/path configuration at that time).

## Out of Scope

- Vulnerabilities in upstream dependencies (report those to the upstream project)
- Issues requiring physical access to the host machine
