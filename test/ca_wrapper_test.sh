#!/bin/bash
# Guards the Community Applications submission artifacts (ca_profile.xml and
# plugins/watch-aware-preloader.xml) against the failure modes that are only
# observable AFTER a submission is rejected, which is a slow and manual loop:
# CA plugin submissions are reviewed by a human, so a defect here costs a
# round trip with a moderator rather than a red check.
#
# The load-bearing assertion is PluginURL: CA matches the wrapper's <PluginURL>
# against the .plg's own pluginURL attribute EXACTLY, and that pairing is the
# plugin's identity key. A drift between plugin/plugin.j2 and the wrapper is
# invisible to every other gate in this repo, so it is asserted here rather than
# left to review.
#
# Fields the .plg overrides (support, min, max, icon) are checked for AGREEMENT
# rather than presence: the wrapper declaring something different from the .plg
# is not a hard failure in CA, but it is a documentation lie, and this repo has
# been bitten before by comments and docs claiming more than the code delivers.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
PROFILE="${REPO_ROOT}/ca_profile.xml"
WRAPPER="${REPO_ROOT}/plugins/watch-aware-preloader.xml"
TMPL="${REPO_ROOT}/plugin/plugin.j2"

fail() { echo "FAIL: $1" >&2; exit 1; }

for f in "$PROFILE" "$WRAPPER" "$TMPL"; do
    [ -f "$f" ] || fail "missing required file: $f"
done

# 1. Both CA files must be well-formed XML. python3 is already a hard dependency
#    of this repo's gates; xmllint is not present on every runner.
for f in "$PROFILE" "$WRAPPER"; do
    python3 -c "import sys,xml.etree.ElementTree as ET; ET.parse(sys.argv[1])" "$f" \
        || fail "not well-formed XML: $f"
done

# 2. The profile's root element and a NON-EMPTY <Profile>. An empty Profile
#    blocks CA submission from finalizing, and the root element is
#    <CommunityApplications> per the official starter - NOT the <RepositoryInfo>
#    spelling used by this author's older Docker templates repo.
python3 - "$PROFILE" <<'PY' || exit 1
import sys, xml.etree.ElementTree as ET
root = ET.parse(sys.argv[1]).getroot()
if root.tag != "CommunityApplications":
    sys.exit(f"FAIL: ca_profile.xml root is <{root.tag}>, want <CommunityApplications>")
prof = root.findtext("Profile") or ""
if not prof.strip():
    sys.exit("FAIL: ca_profile.xml <Profile> is empty; CA blocks submission on this")
PY

# 3. Required wrapper fields, and agreement with the .plg on every field the
#    .plg overrides.
python3 - "$WRAPPER" "$TMPL" <<'PY' || exit 1
import re, sys, xml.etree.ElementTree as ET

wrapper, tmpl = sys.argv[1], sys.argv[2]
root = ET.parse(wrapper).getroot()
if root.tag != "Plugin":
    sys.exit(f"FAIL: wrapper root is <{root.tag}>, want <Plugin>")

for field in ("Name", "PluginURL", "Overview", "Support", "Project", "Category"):
    if not (root.findtext(field) or "").strip():
        sys.exit(f"FAIL: wrapper is missing a non-empty <{field}>")

raw = open(tmpl, encoding="utf-8").read()

# STRIP XML COMMENTS FIRST. plugin.j2 documents its own attributes in comments -
# the icon= rationale block quotes icon="hdd-o" twelve lines above the real
# attribute - so a naive search matches the PROSE and compares the wrapper
# against a sentence. Found by review: mutating the real icon= to "bolt" left
# this test passing, and the other three attributes agreed only by luck of
# ordering.
src = re.sub(r"<!--.*?-->", "", raw, flags=re.S)

def attr(name):
    # Anchor on the PLUGIN element's attribute lines (leading whitespace then
    # name=), not a bare word boundary, so a substring like pluginURL= cannot
    # satisfy a search for a shorter name.
    matches = re.findall(rf'^\s*{name}="([^"]*)"', src, flags=re.M)
    if not matches:
        sys.exit(f"FAIL: plugin.j2 has no {name}= attribute to compare against")
    if len(set(matches)) > 1:
        sys.exit(f"FAIL: plugin.j2 has conflicting {name}= values: {sorted(set(matches))}")
    return matches[0]

# THE hard one: CA keys the plugin's identity on this pair matching exactly.
plg_url, wrap_url = attr("pluginURL"), root.findtext("PluginURL").strip()
if plg_url != wrap_url:
    sys.exit(
        "FAIL: <PluginURL> does not match plugin.j2's pluginURL exactly.\n"
        f"  plugin.j2: {plg_url}\n"
        f"  wrapper:   {wrap_url}\n"
        "  CA matches these byte-for-byte; a mismatch fails the submission."
    )

# The .plg overrides these, so a difference is a silent documentation lie.
for plg_attr, wrap_field in (
    ("support", "Support"),
    ("min", "MinVer"),
    ("max", "MaxVer"),
    ("icon", "IconFA"),
):
    wrap_val = (root.findtext(wrap_field) or "").strip()
    if not wrap_val:
        continue
    # An attribute the .plg does not carry cannot conflict with the wrapper, and
    # for min/max the wrapper value is then the one CA uses. max= is genuinely
    # absent here, which is why this is a skip rather than a failure.
    m = re.findall(rf'^\s*{plg_attr}="([^"]*)"', src, flags=re.M)
    if not m:
        continue
    plg_val = attr(plg_attr)
    # support= is allowed to be the repo root while the wrapper names /issues:
    # the .plg wins either way, and /issues is the convention CA listings use.
    # Compare on PATH BOUNDARIES, not a raw prefix - "…/issues.evil.example/x"
    # startswith "…/issues" and would otherwise pass.
    if plg_attr == "support":
        base = plg_val.rstrip("/")
        if wrap_val != base and not wrap_val.startswith(base + "/"):
            sys.exit(
                f"FAIL: <Support> {wrap_val} is not the .plg's support= {plg_val} "
                "nor a path beneath it"
            )
        continue
    if plg_val != wrap_val:
        sys.exit(
            f"FAIL: <{wrap_field}> is {wrap_val} but plugin.j2 {plg_attr}= is {plg_val}.\n"
            "  Which surface wins varies by field (the .plg overrides support/min/max;\n"
            "  the wrapper's IconFA is what CA publishes), so the only safe state is\n"
            "  agreement: a disagreement means one of them describes a plugin that\n"
            "  does not exist."
        )
PY

echo "PASS: CA wrapper and profile"
