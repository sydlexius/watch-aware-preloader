#!/bin/bash
# Guards the .plg render (plugin/plugin.j2) against producing malformed XML - the
# CDATA-injection gap in #23. Faithfully mirrors the release.yml renderer: the same
# stdlib regex substitution, the same explicit error on a missing env var, and the
# same "]]>" -> "]]]]><![CDATA[>" CDATA escaping (applied here so the test verifies
# the actual transform, not a hand-escaped string). Then asserts the rendered .plg
# is well-formed XML. Runs in CI and the pre-push gate so template or escaping drift
# fails at PR time, not at user install.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
TMPL="${REPO_ROOT}/plugin/plugin.j2"
fail() { echo "FAIL: $1" >&2; exit 1; }

# render_check CHANGELOG ESCAPE(0|1) -> prints "OK" (well-formed) or "MALFORMED".
# ESCAPE=1 applies the same CDATA escaping release.yml does before rendering.
render_check() {
    PLUGIN_VERSION="2026.07.99" PLUGIN_CHECKSUM="deadbeef" \
    PLUGIN_CHANGELOG="$1" WAP_ESCAPE="$2" \
        python3 - "$TMPL" <<'PY'
import os, re, sys, xml.etree.ElementTree as ET

# Mirror release.yml: split "]]>" so it cannot terminate the CDATA block early.
if os.environ.get("WAP_ESCAPE") == "1":
    os.environ["PLUGIN_CHANGELOG"] = os.environ["PLUGIN_CHANGELOG"].replace(
        "]]>", "]]]]><![CDATA[>")

tmpl = open(sys.argv[1], encoding="utf-8").read()
token = re.compile(r"\{\{\s*env\[['\"]([^'\"]+)['\"]\]\s*\}\}")


def replace(match):
    key = match.group(1)
    value = os.environ.get(key)
    if value is None:  # same explicit, actionable failure as release.yml
        sys.exit(f"render: missing environment variable {key}")
    return value


rendered = token.sub(replace, tmpl)
try:
    # Validate on bytes (like release.yml's ET.parse of the file) so a future
    # encoding= declaration in the .plg cannot trip str-parsing.
    ET.fromstring(rendered.encode("utf-8"))
    print("OK")
except ET.ParseError:
    print("MALFORMED")
PY
}

RAW='boom]]>oops <b>and</b> more'

# A normal changelog renders to well-formed XML.
[ "$(render_check 'Release 2026.07.99: fixed a thing' 0)" = "OK" ] \
    || fail "normal changelog did not render well-formed .plg"

# A raw "]]>" (unescaped) breaks the CDATA block - the gap #23 guards against.
[ "$(render_check "$RAW" 0)" = "MALFORMED" ] \
    || fail "expected an unescaped ]]> to yield malformed XML"

# The SAME raw input, run through the release escaping, renders well-formed -
# verifying the escape transform itself, not a pre-escaped string.
[ "$(render_check "$RAW" 1)" = "OK" ] \
    || fail "escaped ]]> should render well-formed .plg"

# --- #171: a signed tag's signature must never reach the CHANGES block. -------
# Uses the real plugin/tag-changelog.sh against a real SSH-signed annotated tag in
# a throwaway repo (isolated from the user's git config). Fails loudly if
# ssh-keygen is unavailable rather than skipping, so the guard cannot go silent.
command -v ssh-keygen >/dev/null || fail "ssh-keygen required for the signed-tag test"
SCRATCH="$(mktemp -d)"
trap 'rm -rf "${SCRATCH}"' EXIT
export GIT_CONFIG_GLOBAL=/dev/null GIT_CONFIG_SYSTEM=/dev/null
ssh-keygen -q -t ed25519 -N '' -f "${SCRATCH}/key" >/dev/null
git init -q "${SCRATCH}/repo"
(
    cd "${SCRATCH}/repo"
    git config user.name t
    git config user.email t@example.com
    git config gpg.format ssh
    git config gpg.ssh.program ssh-keygen
    git config user.signingkey "${SCRATCH}/key.pub"
    git commit -q --allow-empty -m init
    git tag -s -m $'Summary line\n\nBody line one.\nBody line two.' 2026.99.01
    git tag -s -m 'Only a subject' 2026.99.02
    git tag -a -m 'Unsigned subject' 2026.99.03
)
signed_raw="$(cd "${SCRATCH}/repo" && git tag -l --format='%(contents)' 2026.99.01)"
case "${signed_raw}" in
    *"BEGIN SSH SIGNATURE"*) ;;
    *) fail "test setup: the signed tag carries no signature, so the guard below proves nothing" ;;
esac
for t in 2026.99.01 2026.99.02 2026.99.03; do
    out="$(cd "${SCRATCH}/repo" && "${REPO_ROOT}/plugin/tag-changelog.sh" "${t}")"
    case "${out}" in
        *"BEGIN SSH SIGNATURE"* | *"BEGIN PGP SIGNATURE"*) fail "signature leaked into changelog for tag ${t}" ;;
    esac
done
out="$(cd "${SCRATCH}/repo" && "${REPO_ROOT}/plugin/tag-changelog.sh" 2026.99.01)"
[ "${out}" = $'Summary line\n\nBody line one.\nBody line two.' ] \
    || fail "signed tag subject+body not preserved: ${out}"
out="$(cd "${SCRATCH}/repo" && "${REPO_ROOT}/plugin/tag-changelog.sh" 2026.99.02)"
[ "${out}" = "Only a subject" ] || fail "subject-only tag mangled: ${out}"

echo "PASS: plg render CDATA safety and signature-free changelog"
