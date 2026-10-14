#!/bin/bash
# Guards the Go toolchain wiring. CI and release builds read .github/go-version
# (a MAJOR.MINOR.x wildcard) with check-latest, so every new Go patch release is
# picked up without a PR. Dependabot cannot bump the Go toolchain itself
# (dependabot/dependabot-core#13520); before this wiring, an exact go.mod pin
# held CI on a vulnerable patch and failed govulncheck on every Dependabot PR.
#
# The silent failure this catches is a setup-go step that stops floating - one
# pointed back at go.mod, or given an inline go-version (which setup-go prefers
# over go-version-file) - so CI pins a patch again with no red check. Minor-line
# drift between the three version files is caught too: setup-go exports
# GOTOOLCHAIN=local, so that case would fail CI loudly, but only after a push.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
export REPO_ROOT
fail() { echo "FAIL: $1" >&2; exit 1; }

spec="$(tr -d '[:space:]' < "${REPO_ROOT}/.github/go-version")"
[[ "$spec" =~ ^1\.([0-9]+)\.x$ ]] || fail ".github/go-version must be a 1.N.x wildcard, got '${spec}'"
minor="1.${BASH_REMATCH[1]}"

# awk on fields tolerates indentation and a trailing // comment, both of which
# the go command accepts in go.mod.
gomod="$(awk '$1 == "go" { print $2; exit }' "${REPO_ROOT}/go.mod")"
[[ "$gomod" == "${minor}."* ]] || fail "go.mod 'go ${gomod}' is not on the ${minor} line from .github/go-version"
if awk '$1 == "toolchain" { found = 1 } END { exit !found }' "${REPO_ROOT}/go.mod"; then
    fail "go.mod has a toolchain directive; it would pin an exact patch and defeat .github/go-version"
fi

tv="$(awk '$1 == "golang" { print $2; exit }' "${REPO_ROOT}/.tool-versions")"
[[ "$tv" == "${minor}."* ]] || fail ".tool-versions 'golang ${tv}' is not on the ${minor} line"

# Every setup-go reference anywhere under .github (workflows in .yml or .yaml,
# composite actions) must read .github/go-version, float with check-latest, and
# carry no inline go-version. Each step's block runs from its setup-go line to
# the next list item at the same or a shallower indent, with comments stripped
# so a commented-out key cannot satisfy the check.
steps="$(python3 - <<'PY'
import os, re, sys
root = os.environ["REPO_ROOT"]
steps = errors = 0
for dirpath, _, files in os.walk(os.path.join(root, ".github")):
    for name in sorted(files):
        if not name.endswith((".yml", ".yaml")):
            continue
        path = os.path.join(dirpath, name)
        rel = os.path.relpath(path, root)
        lines = open(path, encoding="utf-8").read().splitlines()
        for i, line in enumerate(lines):
            if "actions/setup-go@" not in line or line.lstrip().startswith("#"):
                continue
            steps += 1
            # Indent of the list item that owns this uses: line.
            j = i
            while j > 0 and not re.match(r"^\s*-\s", lines[j]):
                j -= 1
            dash = len(lines[j]) - len(lines[j].lstrip())
            block = []
            for k in range(i + 1, len(lines)):
                m = re.match(r"^(\s*)-\s", lines[k])
                if m and len(m.group(1)) <= dash:
                    break
                block.append(re.sub(r"(^|\s)#.*$", "", lines[k]).rstrip())
            where = f"{rel}:{i + 1}"
            keys = [b.strip() for b in block]
            if not any(re.fullmatch(r"""go-version-file:\s*["']?\.github/go-version["']?""", b) for b in keys):
                print(f"FAIL: {where} setup-go does not read .github/go-version", file=sys.stderr); errors += 1
            if not any(re.fullmatch(r"""check-latest:\s*["']?true["']?""", b) for b in keys):
                print(f"FAIL: {where} setup-go lacks check-latest: true", file=sys.stderr); errors += 1
            if any(re.match(r"go-version:", b) for b in keys):
                print(f"FAIL: {where} setup-go has an inline go-version, which overrides the file", file=sys.stderr); errors += 1
if steps == 0:
    print("FAIL: no actions/setup-go steps found; the guard is checking nothing", file=sys.stderr); sys.exit(1)
if errors:
    sys.exit(1)
print(steps)
PY
)"
echo "PASS: Go ${minor}.x floats in ${steps} setup-go steps; go.mod floor ${gomod}"
