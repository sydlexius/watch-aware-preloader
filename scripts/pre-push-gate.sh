#!/usr/bin/env bash
#
# pre-push-gate.sh -- full local gate before push.
#
# Steps (in order, fast-fail):
#   1. gofmt          -- whole module formatted
#   2. go vet         -- static analysis
#   3. golangci-lint  -- full linter suite (SKIP with warning if not installed)
#   4. go test -race  -- tests with race detector
#   5. go build       -- cross-compile linux/amd64 for the daemon
#   6. PHP lint       -- phpstan + php-cs-fixer (only when .php/.page files exist
#                        and vendor/bin/phpstan is present)
#   7. JS lint        -- eslint over the plugin's browser JS (SKIP with warning
#                        when node_modules is absent)
#      NOTE: every optional toolchain here SKIPS WITH A WARNING when it is not
#      installed and FAILS LOUDLY when it is. A missing tool must never look
#      like a pass, and must never block a fresh clone from running the gate.
#   8. plugin tests   -- the PHP/bash/JS suites CI enforces, so a broken one
#                        fails HERE rather than after the push (#86)
#   9. smoke-install  -- install-by-URL cron-collation smoke test (#26)
#
# Exit 0 = all checks passed; non-zero = first failure.

set -euo pipefail

# The PHP dev tooling (composer) installs a vendor/ dir at the repo root, which
# would put Go into vendor mode (there is no Go vendor/modules.txt). Ignore it so
# the gate works in a polyglot local checkout. No-op when no vendor/ exists.
export GOFLAGS="${GOFLAGS:--mod=readonly}"

echo "=== gofmt ==="
UNFORMATTED=$(gofmt -l . 2>/dev/null || true)
if [ -n "$UNFORMATTED" ]; then
    echo "FAIL: the following files need formatting:"
    while IFS= read -r f; do echo "  $f"; done <<< "$UNFORMATTED"
    echo ""
    echo "Run: gofmt -w ."
    exit 1
fi
echo "OK"

echo ""
echo "=== go vet ==="
go vet ./...
echo "OK"

echo ""
echo "=== golangci-lint ==="
if ! command -v golangci-lint >/dev/null 2>&1; then
    echo "SKIP: golangci-lint not installed (install the repo's documented golangci-lint v2 toolchain -- see REQUIREMENTS.md)"
else
    # A findings file naming a path that no longer exists is a STALE CACHE, not a
    # defect: golangci-lint keys results by absolute path, so removing a git
    # worktree leaves entries pointing into it and they resurface as phantom
    # findings on the next run. They cannot be fixed (the files are gone) and they
    # block the push, which is how a routine worktree cleanup turns into a
    # mysterious gate failure.
    #
    # Detect it rather than clearing unconditionally: a blanket cache clean would
    # make every run pay a full re-lint. Retry ONCE after clearing, so a genuine
    # finding still fails the gate on the second pass.
    # The test is "does the finding name a file that does not exist", which
    # catches a stale entry from ANY removed worktree rather than only the
    # conventional .claude/worktrees location. A genuine finding always names a
    # file that is present, so this cannot mask one.
    # Written WITHOUT an early-terminating pipeline on purpose. Under the
    # `set -o pipefail` above, a `break` inside a `while read` (or a `grep -q`)
    # closes the pipe while upstream commands are still writing, so they take
    # SIGPIPE and the pipeline reports failure even when the scan succeeded -
    # the function would return false and the retry would never fire, exactly
    # when it was needed. The loop reads every candidate and sets a flag.
    lint_stale() {
        local files f found=1
        files=$(printf '%s' "$1" | grep -oE '^[^ :]+\.go:[0-9]+:' | cut -d: -f1 | sort -u) || true
        [ -n "$files" ] || return 1
        while IFS= read -r f; do
            [ -n "$f" ] || continue
            if [ ! -e "$f" ]; then
                found=0
            fi
        done <<EOF_FILES
$files
EOF_FILES

        return "$found"
    }

    # Lint the HOST GOOS and then linux. A file behind //go:build linux is not
    # in the build on a darwin host, so the first pass never reads it: no
    # warning, no skip notice, just "0 issues". CI lints on linux, so a
    # host-only gate does not cover what CI checks -- three misspell findings in
    # internal/diskresolve/rotational_linux*.go cleared this gate and failed CI
    # on PR #140. The linux pass is what makes the local gate match CI.
    for lint_goos in "" linux; do
        lint_rc=0
        if [ -n "$lint_goos" ]; then
            echo "--- GOOS=$lint_goos ---"
            lint_out=$(GOOS="$lint_goos" golangci-lint run ./... 2>&1) || lint_rc=$?
        else
            lint_out=$(golangci-lint run ./... 2>&1) || lint_rc=$?
        fi
        if [ "$lint_rc" -ne 0 ] && lint_stale "$lint_out"; then
            echo "stale lint cache detected (findings name a removed worktree); clearing and retrying"
            golangci-lint cache clean
            lint_rc=0
            if [ -n "$lint_goos" ]; then
                lint_out=$(GOOS="$lint_goos" golangci-lint run ./... 2>&1) || lint_rc=$?
            else
                lint_out=$(golangci-lint run ./... 2>&1) || lint_rc=$?
            fi
        fi
        if [ "$lint_rc" -ne 0 ]; then
            printf '%s\n' "$lint_out"
            exit "$lint_rc"
        fi
    done
    echo "OK"
fi

echo ""
echo "=== go test -race ==="
CGO_ENABLED=1 go test -race -count=1 ./...
echo "OK"

echo ""
echo "=== go build (linux/amd64) ==="
GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o /dev/null ./cmd/preloadd
echo "OK"

echo ""
echo "=== PHP lint ==="
if find plugin/ src/ -type f \( -name '*.php' -o -name '*.page' \) 2>/dev/null | grep -q . && [ -x vendor/bin/phpstan ]; then
    vendor/bin/phpstan analyse --no-progress
    vendor/bin/php-cs-fixer fix --dry-run --diff
    echo "OK"
else
    echo "SKIP (warning): no PHP files under plugin/ or src/, or vendor/bin/phpstan not present - run 'make php-install' to lint locally"
fi

echo ""
echo "=== JS lint (eslint) ==="
if [ -x node_modules/.bin/eslint ]; then
    # The verified binary is invoked directly rather than through npx: npx adds
    # its own resolution layer (and prints npm notice lines into the gate
    # output), which is exactly the nondeterminism a gate should not have.
    ./node_modules/.bin/eslint .
    echo "OK"
elif command -v node >/dev/null 2>&1; then
    echo "SKIP (warning): node_modules absent - run 'npm ci' to lint the plugin JS"
else
    echo "SKIP (warning): node not installed - the plugin JS is unlinted locally"
fi

echo ""
echo "=== plugin tests (PHP / bash / JS) ==="
# CI enforces these in its PHP-tests job; running them here keeps the local gate
# from being a strict subset of CI (#86). Optional toolchains warn on absence
# rather than passing silently, so a missing interpreter is visible.
if command -v php >/dev/null 2>&1; then
    for t in test/*_test.php; do
        [ -e "$t" ] || continue
        echo "== $t =="
        php "$t"
    done
else
    echo "SKIP (warning): php not installed - the PHP unit tests did not run"
fi

for t in test/rc_preloadd_render_test.sh test/rc_preloadd_estimate_test.sh test/plg_render_test.sh; do
    [ -e "$t" ] || continue
    echo "== $t =="
    bash "$t"
done

if command -v node >/dev/null 2>&1; then
    for t in test/*_test.js; do
        [ -e "$t" ] || continue
        case "$t" in *_dom_test.js) continue ;; esac
        echo "== $t =="
        node "$t"
    done
    # The DOM suites need jsdom from node_modules, not just an interpreter.
    # Guarding these on `node` alone made a fresh clone fail the whole gate with
    # a bare "Cannot find module 'jsdom'" that never named the fix (#108). An
    # uninstalled optional toolchain warns; it does not fail.
    if [ -d node_modules/jsdom ]; then
        for t in test/*_dom_test.js; do
            [ -e "$t" ] || continue
            echo "== $t =="
            node --test "$t"
        done
    elif ls test/*_dom_test.js >/dev/null 2>&1; then
        echo "SKIP (warning): jsdom absent - run 'npm ci' to run the DOM tests"
    fi
else
    echo "SKIP (warning): node not installed - the JS unit and DOM tests did not run"
fi
echo "OK"

echo ""
echo "=== smoke-install ==="
bash scripts/smoke-install-by-url.sh
echo "OK"

echo ""
echo "All hard checks passed."
