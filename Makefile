# watch-aware-preloader - developer Makefile
# Run `make help` for a summary of targets.

BINARY      := preloadd
PKG         := ./cmd/preloadd
BIN_DIR     := bin
VERSION     := $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS     := -X main.version=$(VERSION)
GOLANGCI    := golangci-lint
COMPOSER    := composer

# Ignore the composer PHP vendor/ dir for Go (see scripts/pre-push-gate.sh). No-op without vendor/.
export GOFLAGS ?= -mod=readonly

.DEFAULT_GOAL := help

## ----- Go -----

.PHONY: build
build: ## Build the daemon into bin/preloadd
	@mkdir -p $(BIN_DIR)
	go build -ldflags "$(LDFLAGS)" -o $(BIN_DIR)/$(BINARY) $(PKG)

.PHONY: run
run: build ## Build and run locally
	./$(BIN_DIR)/$(BINARY)

.PHONY: test
test: ## Run tests
	go test ./...

.PHONY: test-race
test-race: ## Run tests with the race detector
	CGO_ENABLED=1 go test -race -count=1 ./...

.PHONY: cover
cover: ## Run tests with a coverage report
	go test -coverprofile=coverage.out ./...
	go tool cover -func=coverage.out | tail -1

.PHONY: lint
lint: ## Run golangci-lint (host GOOS, then linux for build-tagged files)
	$(GOLANGCI) run
	@# A file behind //go:build linux is NOT in the build on darwin, so the run
	@# above never reads it - it is skipped silently, with no warning and a
	@# "0 issues" result. internal/diskresolve/rotational_linux.go and its test
	@# are exactly that shape, and three misspell findings in them passed this
	@# gate and failed CI (PR #140). CI lints on linux, so linting only the host
	@# GOOS means the gate does not cover what CI checks.
	@echo "=== golangci-lint (GOOS=linux) ==="
	GOOS=linux $(GOLANGCI) run

.PHONY: fmt
fmt: ## Format Go code and tidy modules
	gofmt -w .
	go mod tidy

.PHONY: vet
vet: ## Run go vet
	go vet ./...

# Fuzz each target by explicit <pkg>:<FuzzName> so this stays correct if a
# package ever gains a second Fuzz func (go test -fuzz='.' errors on >1 target
# per package). Keep in sync with the matrix in .github/workflows/fuzz.yml.
FUZZ_TARGETS := \
	./internal/pathmap:FuzzToHost \
	./internal/config:FuzzConfigLoad \
	./internal/mediaserver/emby:FuzzValidateBaseURL

.PHONY: fuzz
fuzz: ## Smoke-fuzz each target 20s with mutation; the seed corpus alone runs (no mutation) under `make test`
	@for t in $(FUZZ_TARGETS); do \
		pkg=$${t%%:*}; name=$${t##*:}; \
		echo "== fuzz $$name ($$pkg) =="; \
		go test -run='^$$' -fuzz="^$$name$$" -fuzztime=20s $$pkg || exit 1; \
	done

.PHONY: vulncheck
vulncheck: ## Scan for known vulnerabilities (govulncheck, pinned)
	go run golang.org/x/vuln/cmd/govulncheck@v1.6.0 ./...

## ----- PHP (Phase 2 settings page) -----

.PHONY: php-install
php-install: ## Install PHP dev tooling (PHPStan, PHP-CS-Fixer) via Composer
	$(COMPOSER) install

.PHONY: php-lint
php-lint: ## Static analysis (PHPStan) + style check (PHP-CS-Fixer, dry-run)
	@if find plugin src -type f \( -name '*.php' -o -name '*.page' \) 2>/dev/null | grep -q .; then \
		vendor/bin/phpstan analyse --no-progress ; \
		vendor/bin/php-cs-fixer fix --dry-run --diff ; \
	else \
		echo "no PHP files under plugin/ or src/ yet - skipping PHP lint" ; \
	fi

.PHONY: php-fix
php-fix: ## Auto-fix PHP style (PHP-CS-Fixer)
	vendor/bin/php-cs-fixer fix

.PHONY: php-test
php-test: ## Run plain-PHP unit tests (test/*_test.php) + the render contract test
	@for t in test/*_test.php; do [ -e "$$t" ] || continue; echo "== $$t =="; php "$$t" || exit 1; done
	bash test/rc_preloadd_render_test.sh
	bash test/rc_preloadd_estimate_test.sh
	bash test/plg_render_test.sh
	@# Standalone scripts (assert-and-exit) vs node:test suites (*_dom_test.js).
	@# The DOM suites need the test runner, so they are excluded from the first loop.
	@for t in test/*_test.js; do \
		[ -e "$$t" ] || continue; \
		case "$$t" in *_dom_test.js) continue ;; esac; \
		echo "== $$t =="; node "$$t" || exit 1; \
	done
	@for t in test/*_dom_test.js; do [ -e "$$t" ] || continue; echo "== $$t =="; node --test "$$t" || exit 1; done

.PHONY: js-lint
js-lint: ## Lint the plugin's browser JS + the headless tests (ESLint)
	@if [ -x node_modules/.bin/eslint ]; then \
		./node_modules/.bin/eslint . ; \
	else \
		echo "eslint not installed - run 'make js-install' first" >&2 ; exit 1 ; \
	fi

.PHONY: js-install
js-install: ## Install the dev-only JS tooling (ESLint, jsdom)
	npm ci

.PHONY: smoke-test
smoke-test: ## Smoke-test the install-by-URL cron-collation flow (#26)
	bash scripts/smoke-install-by-url.sh

.PHONY: shellcheck
shellcheck: ## Lint shipped bash (rc.preloadd + test/ + scripts/ harnesses)
	@files=$$(find src -type f -name 'rc.*'; find test -type f -name '*.sh' 2>/dev/null; find scripts -type f -name '*.sh' 2>/dev/null); \
	if [ -n "$$files" ]; then \
		shellcheck $$files ; \
	else \
		echo "no shell scripts to check yet" ; \
	fi

## ----- Hooks -----

.PHONY: hooks
hooks: ## Install git hooks (sets core.hooksPath + chmod +x)
	git config core.hooksPath .githooks
	chmod +x .githooks/* scripts/*.sh
	@echo "Hooks installed. Run 'make doctor' to verify."

.PHONY: doctor
doctor: ## Verify git hook wiring is correct
	bash scripts/check-hooks.sh

## ----- Meta -----

.PHONY: tools
tools: ## Install local dev tooling (golangci-lint + PHP dev deps)
	go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@latest
	$(COMPOSER) install

.PHONY: clean
clean: ## Remove build artifacts
	rm -rf $(BIN_DIR) coverage.out coverage.html

.PHONY: help
help: ## Show this help
	@grep -hE '^[a-zA-Z_-]+:.*?## ' $(MAKEFILE_LIST) | \
		awk 'BEGIN {FS = ":.*?## "}; {printf "  \033[36m%-14s\033[0m %s\n", $$1, $$2}'
