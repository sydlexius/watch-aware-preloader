#!/bin/bash
# Offline fixture tests for test/ca_wrapper_test.sh (#145). The real check fetches
# the CA wrapper from sydlexius/unraid-templates; these drive it with local
# fixtures (CA_WRAPPER_SRC + CA_TEMPLATE) to prove each drift kind FAILS and the
# tolerated variances PASS, with no network. Runs in CI and the pre-push gate.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
CHECK="${REPO_ROOT}/test/ca_wrapper_test.sh"
fail() { echo "FAIL: $1" >&2; exit 1; }

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

URL="https://github.com/example/demo/releases/latest/download/demo.plg"

# A minimal template shaped like plugin/plugin.j2. The comment carries a DECOY
# icon= on ITS OWN LINE, where the anchored attribute regex (^\s*icon=") WOULD
# match it if comments were not stripped; the check would then see two
# conflicting icon= values and fail. So every passing case proves the
# comment-stripping works.
write_template() { # $1 = icon value, $2 = optional extra attribute line(s)
    cat >"${WORK}/plugin.j2" <<EOF
<?xml version='1.0' standalone='yes'?>
<!--
  icon="decoy" is prose here that a naive search would match.
-->
<PLUGIN name="demo"
  pluginURL="${URL}"
  support="https://github.com/example/demo/issues"
  min="7.0.0"
  icon="$1"
${2:-}
>
</PLUGIN>
EOF
}

# write_wrapper KEY=VALUE... ; a value of __OMIT__ drops the element.
write_wrapper() {
    python3 - "${WORK}/wrapper.xml" "$URL" "$@" <<'PY'
import sys
out, url, overrides = sys.argv[1], sys.argv[2], sys.argv[3:]
fields = {
    "Name": "Demo",
    "PluginURL": url,
    "PluginAuthor": "example",
    "Overview": "A demo plugin.",
    "Support": "https://github.com/example/demo/issues",
    "Project": "https://github.com/example/demo",
    "Category": "Tools:System",
    "IconFA": "hdd-o",
    "MinVer": "7.0.0",
}
for kv in overrides:
    k, v = kv.split("=", 1)
    fields[k] = v
body = "".join(
    f"  <{k}>{v}</{k}>\n" for k, v in fields.items() if v != "__OMIT__"
)
open(out, "w", encoding="utf-8").write(
    '<?xml version="1.0" encoding="utf-8"?>\n<Plugin>\n' + body + "</Plugin>\n"
)
PY
}

run_check() {
    CA_WRAPPER_SRC="${WORK}/wrapper.xml" CA_TEMPLATE="${WORK}/plugin.j2" \
        bash "$CHECK" >"${WORK}/out.log" 2>&1
}

expect_pass() { # $1 = description
    if ! run_check; then
        cat "${WORK}/out.log" >&2
        fail "expected PASS: $1"
    fi
    echo "ok  pass: $1"
}

expect_fail() { # $1 = description, $2 = substring the failure must name
    if run_check; then
        fail "expected FAIL but the check passed: $1"
    fi
    grep -qF -- "$2" "${WORK}/out.log" || {
        cat "${WORK}/out.log" >&2
        fail "failed for the wrong reason ($1); wanted message containing: $2"
    }
    echo "ok  fail: $1"
}

write_template "hdd-o"

write_wrapper
expect_pass "agreeing wrapper and template"

write_wrapper "PluginURL=https://github.com/example/demo/releases/download/demo.plg"
expect_fail "PluginURL mismatch" "<PluginURL> does not match"

write_wrapper "PluginURL= ${URL} "
expect_fail "PluginURL with surrounding whitespace" "<PluginURL> does not match"

write_wrapper "IconFA=bolt"
expect_fail "icon mismatch" "<IconFA> is bolt"

write_wrapper "IconFA=fa-hdd-o"
expect_pass "icon fa- prefix on the wrapper side"

write_template "fa-hdd-o"
write_wrapper
expect_pass "icon fa- prefix on the template side"
write_template "hdd-o"

write_wrapper "MinVer=6.12.0"
expect_fail "min mismatch" "<MinVer> is 6.12.0"

write_wrapper "MaxVer=7.9.9"
expect_pass "wrapper MaxVer with no max= in the template is skipped"

write_wrapper "Support=https://github.com/example/demo/issues.evil.example/x"
expect_fail "support outside the path boundary" "<Support>"

write_wrapper "Support=https://github.com/example/demo/issues/new"
expect_pass "support path beneath the template's support="

# Every field in each tier, omitted one at a time, must fail naming its tier.
for f in Name Overview PluginURL PluginAuthor; do
    write_wrapper "${f}=__OMIT__"
    expect_fail "missing required ${f}" "<${f}> (required by CA)"
done
for f in Support Project Category; do
    write_wrapper "${f}=__OMIT__"
    expect_fail "missing repo-convention ${f}" "<${f}> (repo convention)"
done

write_wrapper "Overview=   "
expect_fail "whitespace-only required Overview" "<Overview> (required by CA)"

# Wrong root element.
write_wrapper
sed -e 's#<Plugin>#<Plugins>#' -e 's#</Plugin>#</Plugins>#' "${WORK}/wrapper.xml" >"${WORK}/w2.xml"
mv "${WORK}/w2.xml" "${WORK}/wrapper.xml"
expect_fail "wrapper root is not <Plugin>" "wrapper root is <Plugins>"

# Two DIFFERENT real icon= attribute lines in the template.
write_template "hdd-o" '  icon="bolt"'
write_wrapper
expect_fail "template with conflicting icon= values" "conflicting icon= values"

# max= present in the template: the wrapper's MaxVer must agree.
write_template "hdd-o" '  max="7.9.9"'
write_wrapper "MaxVer=7.9.8"
expect_fail "max mismatch" "<MaxVer> is 7.9.8"
write_wrapper "MaxVer=7.9.9"
expect_pass "max agreeing with the template's max="
write_template "hdd-o"

printf '<Plugin><Name>broken' >"${WORK}/wrapper.xml"
expect_fail "malformed wrapper XML" "not well-formed XML"

# Fetch failures must fail closed, never skip. All offline: a missing local
# source, a non-https URL (refused before any request), and a refused local port.
if CA_WRAPPER_SRC="${WORK}/does-not-exist.xml" CA_TEMPLATE="${WORK}/plugin.j2" \
    bash "$CHECK" >"${WORK}/out.log" 2>&1; then
    fail "expected FAIL for a missing CA_WRAPPER_SRC"
fi
grep -qF "CA_WRAPPER_SRC does not exist" "${WORK}/out.log" || fail "missing-source failure had the wrong message"
echo "ok  fail: missing CA_WRAPPER_SRC"

if env -u CA_WRAPPER_SRC CA_WRAPPER_URL="http://127.0.0.1:1/w.xml" CA_TEMPLATE="${WORK}/plugin.j2" \
    bash "$CHECK" >"${WORK}/out.log" 2>&1; then
    fail "expected FAIL for a non-https wrapper URL"
fi
grep -qF "refusing non-https" "${WORK}/out.log" || fail "non-https failure had the wrong message"
echo "ok  fail: non-https wrapper URL"

# CA_WRAPPER_RETRIES=0: with --retry-all-errors a refused connection would
# otherwise back off and retry, making this case take several seconds.
if env -u CA_WRAPPER_SRC CA_WRAPPER_RETRIES=0 CA_WRAPPER_URL="https://127.0.0.1:1/w.xml" CA_TEMPLATE="${WORK}/plugin.j2" \
    bash "$CHECK" >"${WORK}/out.log" 2>&1; then
    fail "expected FAIL for an unreachable wrapper URL"
fi
grep -qF "could not fetch the CA wrapper" "${WORK}/out.log" || fail "unreachable-URL failure had the wrong message"
echo "ok  fail: unreachable wrapper URL"

echo "PASS: CA wrapper fixture tests"
