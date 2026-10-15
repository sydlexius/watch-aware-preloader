#!/bin/bash
# Cross-repo guard for the Community Applications (CA) plugin wrapper.
#
# The CA wrapper (plugins/watch-aware-preloader.xml) and the CA profile no longer
# live in this repo: they are aggregated with this account's other CA listings in
# sydlexius/unraid-templates (#145). This test fetches that wrapper from
# unraid-templates `main` and checks it against plugin/plugin.j2 here.
#
# Two halves, one invariant:
#   - PREVENTIVE (this test): runs in CI and the pre-push gate, so a change to
#     plugin/plugin.j2 that would break the wrapper pairing fails on the PR,
#     BEFORE a release publishes a .plg the listing no longer matches.
#   - DETECTIVE (unraid-templates' daily check): compares the wrapper against the
#     PUBLISHED .plg and catches drift that lands from either side.
#
# The load-bearing assertion is PluginURL: CA matches the wrapper's <PluginURL>
# against the .plg's own pluginURL attribute BYTE-FOR-BYTE, and that pairing is
# the plugin's identity key. Surrounding whitespace in the wrapper is therefore a
# mismatch, not something to strip.
#
# Fields the .plg overrides (support, min, max, icon) are checked for AGREEMENT
# rather than presence: a disagreement is not a hard failure in CA, but it means
# one surface describes a plugin that does not exist.
#
# Inputs (overrides exist so test/ca_wrapper_fixture_test.sh can run offline):
#   CA_WRAPPER_SRC  local wrapper file to check instead of fetching
#   CA_WRAPPER_URL  wrapper URL to fetch (https only); default is unraid-templates main
#   CA_TEMPLATE     plugin template to compare against; default plugin/plugin.j2
#   CA_WRAPPER_RETRIES  curl retry count (non-negative integer); default 3. The
#                   fixture suite sets 0 so its unreachable-URL case stays fast.
# A fetch failure FAILS: an unreachable wrapper is never a silent pass.
#
# ORDERING for a change that must touch BOTH plugin/plugin.j2 and the wrapper in
# sydlexius/unraid-templates (e.g. a min= bump):
#   1. Merge the wrapper change in sydlexius/unraid-templates FIRST. Its daily
#      drift check stays red until the next release here; that check is not a
#      required check there, so it does not block the merge.
#   2. This repo's PR then goes green (this test reads unraid-templates main);
#      merge it.
#   3. Release.
# Conversely, ANY wrapper edit in unraid-templates turns open PRs here red until
# plugin.j2 agrees with it. That is the intended preventive effect, not a flake.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
DEFAULT_URL="https://raw.githubusercontent.com/sydlexius/unraid-templates/main/plugins/watch-aware-preloader.xml"
URL="${CA_WRAPPER_URL:-$DEFAULT_URL}"
RETRIES="${CA_WRAPPER_RETRIES:-3}"
TMPL="${CA_TEMPLATE:-${REPO_ROOT}/plugin/plugin.j2}"

fail() { echo "FAIL: $1" >&2; exit 1; }

[ -f "$TMPL" ] || fail "missing plugin template: $TMPL"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT
WRAPPER="${WORK}/wrapper.xml"

if [ -n "${CA_WRAPPER_SRC:-}" ]; then
    [ -f "$CA_WRAPPER_SRC" ] || fail "CA_WRAPPER_SRC does not exist: $CA_WRAPPER_SRC"
    cp "$CA_WRAPPER_SRC" "$WRAPPER"
    SRC_DESC="$CA_WRAPPER_SRC"
else
    case "$URL" in
        https://*) ;;
        *) fail "refusing non-https wrapper URL: $URL" ;;
    esac
    case "$RETRIES" in
        ''|*[!0-9]*) fail "CA_WRAPPER_RETRIES must be a non-negative integer: $RETRIES" ;;
    esac
    # --retry-all-errors: DNS failures and refused connections retry too, not
    # only the HTTP/timeout errors curl treats as transient by default.
    curl -fsSL --proto '=https' --retry "$RETRIES" --retry-all-errors --max-time 30 \
        -o "$WRAPPER" "$URL" \
        || fail "could not fetch the CA wrapper from $URL (fail closed: the cross-repo check did not run)"
    SRC_DESC="$URL"
fi
[ -s "$WRAPPER" ] || fail "CA wrapper is empty: $SRC_DESC"

python3 - "$WRAPPER" "$TMPL" "$SRC_DESC" <<'PY' || exit 1
import re, sys, xml.etree.ElementTree as ET

wrapper, tmpl, desc = sys.argv[1], sys.argv[2], sys.argv[3]
try:
    root = ET.parse(wrapper).getroot()
except ET.ParseError as e:
    sys.exit(f"FAIL: wrapper is not well-formed XML ({desc}): {e}")
if root.tag != "Plugin":
    sys.exit(f"FAIL: wrapper root is <{root.tag}>, want <Plugin>")

# Two tiers, kept distinct because they fail differently. CA REQUIRES the first
# set (the starter's "Required fields" block), so a listing missing one is
# rejected.
for field in ("Name", "Overview", "PluginURL", "PluginAuthor"):
    if not (root.findtext(field) or "").strip():
        sys.exit(f"FAIL: wrapper is missing a non-empty <{field}> (required by CA)")

# CA treats these as optional, but the listing always ships them; asserted
# separately so the message does not claim CA rejects their absence.
for field in ("Support", "Project", "Category"):
    if not (root.findtext(field) or "").strip():
        sys.exit(f"FAIL: wrapper is missing a non-empty <{field}> (repo convention)")

raw = open(tmpl, encoding="utf-8").read()

# STRIP XML COMMENTS FIRST. plugin.j2 documents its own attributes in comments
# (the icon= rationale quotes icon="hdd-o" above the real attribute), so a naive
# search would compare the wrapper against prose.
src = re.sub(r"<!--.*?-->", "", raw, flags=re.S)

def attr(name):
    # Anchor on attribute lines (leading whitespace then name=), so a substring
    # like pluginURL= cannot satisfy a search for a shorter name.
    matches = re.findall(rf'^\s*{name}="([^"]*)"', src, flags=re.M)
    if not matches:
        sys.exit(f"FAIL: plugin template has no {name}= attribute to compare against")
    if len(set(matches)) > 1:
        sys.exit(f"FAIL: plugin template has conflicting {name}= values: {sorted(set(matches))}")
    return matches[0]

# THE hard one. Deliberately NOT stripped: CA compares byte-for-byte.
plg_url, wrap_url = attr("pluginURL"), root.findtext("PluginURL")
if plg_url != wrap_url:
    sys.exit(
        "FAIL: <PluginURL> does not match plugin.j2's pluginURL exactly.\n"
        f"  plugin.j2: {plg_url!r}\n"
        f"  wrapper:   {wrap_url!r}\n"
        "  CA matches these byte-for-byte (surrounding whitespace included);\n"
        "  fix plugin.j2 or the wrapper in sydlexius/unraid-templates."
    )

def strip_fa(v):
    return v[3:] if v.startswith("fa-") else v

for plg_attr, wrap_field in (
    ("support", "Support"),
    ("min", "MinVer"),
    ("max", "MaxVer"),
    ("icon", "IconFA"),
):
    wrap_val = (root.findtext(wrap_field) or "").strip()
    if not wrap_val:
        continue
    # An attribute the template does not carry cannot conflict (max= is
    # genuinely absent today), so either side lacking it is a skip.
    if not re.findall(rf'^\s*{plg_attr}="([^"]*)"', src, flags=re.M):
        continue
    plg_val = attr(plg_attr)
    # support= may be the repo root while the wrapper names /issues. Compare on
    # PATH BOUNDARIES: ".../issues.evil.example/x" startswith ".../issues".
    if plg_attr == "support":
        base = plg_val.rstrip("/")
        if wrap_val != base and not wrap_val.startswith(base + "/"):
            sys.exit(
                f"FAIL: <Support> {wrap_val} is not the .plg's support= {plg_val} "
                "nor a path beneath it"
            )
        continue
    # The wrapper's IconFA is conventionally spelled without the fa- prefix
    # (CA publishes it as-is); the .plg may carry either form.
    if plg_attr == "icon":
        if strip_fa(plg_val) != strip_fa(wrap_val):
            sys.exit(
                f"FAIL: <IconFA> is {wrap_val} but plugin.j2 icon= is {plg_val} "
                "(compared modulo a leading fa-)"
            )
        continue
    if plg_val != wrap_val:
        sys.exit(
            f"FAIL: <{wrap_field}> is {wrap_val} but plugin.j2 {plg_attr}= is {plg_val}.\n"
            "  The only safe state is agreement: a disagreement means one of them\n"
            "  describes a plugin that does not exist."
        )
PY

echo "PASS: CA wrapper ($SRC_DESC) agrees with $(basename "$TMPL")"
