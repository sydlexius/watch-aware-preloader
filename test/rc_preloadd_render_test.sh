#!/bin/bash
# Test rc.preloadd render: a fixture .cfg must produce a correct config.toml
# and cron fragment. Runs against a temp dir via the WAP_* env overrides.
set -euo pipefail

REPO_ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
RC="${REPO_ROOT}/src/usr/local/emhttp/plugins/watch-aware-preloader/scripts/rc.preloadd"

fail() { echo "FAIL: $1" >&2; exit 1; }
assert_contains() { grep -qF -- "$2" "$1" || fail "expected '$2' in $1:\n$(cat "$1")"; }
assert_not_contains() { if grep -qF -- "$2" "$1"; then fail "did not expect '$2' in $1"; fi; }

work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT
export WAP_FLASH="$work/flash"
export WAP_RUNTIME="$work/runtime"
export WAP_STATUS_PATH="$work/status.json"
# Sandbox the cron marker paths so the test never touches the real
# /var/log/plugins or the real .plg file on the host.
export WAP_PLUGIN_LOG_DIR="$work/plugins-log"
export WAP_PLG_FILE="$work/flash/watch-aware-preloader.plg"
mkdir -p "$WAP_FLASH" "$WAP_RUNTIME"

# default.cfg lives in the runtime tree; copy the real one in.
cp "${REPO_ROOT}/src/usr/local/emhttp/plugins/watch-aware-preloader/default.cfg" \
   "$WAP_RUNTIME/default.cfg"

# Fixture flash .cfg (what /update.php would have written).
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="alice, bob"
LIBRARIES="lib-1, lib-2"
RAM_PERCENT="40"
TARGET_SECONDS="25"
MIN_HEAD_MB="8"
MAX_HEAD_MB="250"
TAIL_MB="1"
PATH_MAPS="/share=>/mnt/user; /media=>/mnt/user/media"
CRON_INTERVAL="10"
CFG

bash "$RC" render

cfg="$WAP_FLASH/config.toml"
cron="$WAP_FLASH/watch-aware-preloader.cron"
[ -f "$cfg" ] || fail "config.toml not generated"
[ -f "$cron" ] || fail "cron fragment not generated"

assert_contains "$cfg" 'type = "emby"'
assert_contains "$cfg" 'url = "http://media.example:8096"'
assert_contains "$cfg" '[users]'
assert_contains "$cfg" 'enabled = ["alice", "bob"]'
assert_contains "$cfg" '[libraries]'
assert_contains "$cfg" 'enabled = ["lib-1", "lib-2"]'
assert_contains "$cfg" 'ram_percent = 40'
assert_contains "$cfg" 'target_seconds = 25'
assert_contains "$cfg" 'from = "/share"'
assert_contains "$cfg" 'to = "/mnt/user"'
assert_contains "$cfg" 'from = "/media"'
assert_contains "$cfg" 'to = "/mnt/user/media"'
assert_contains "$cfg" "status_path = \"$WAP_STATUS_PATH\""
assert_contains "$cfg" "secret_path = \"$WAP_FLASH/secrets.toml\""
assert_not_contains "$cfg" "api_key"
assert_contains "$cron" '*/10 * * * *'

# Absent flash .cfg -> falls back to default.cfg.
rm -f "$WAP_FLASH/watch-aware-preloader.cfg" "$cfg"
bash "$RC" render
assert_contains "$cfg" 'url = "http://localhost:8096"'
assert_contains "$cfg" 'enabled = []'

# Cron marker (PRs #24/#25 fix): a fresh `install` with PLG_FILE ABSENT must
# still create the /var/log/plugins/<name>.plg marker as a symlink ENTRY. It is
# a dangling symlink (target absent) - assert [ -L ], NOT [ -e ] - so
# update_cron can collate the cron fragment on install-by-URL.
rm -rf "$WAP_PLUGIN_LOG_DIR"
[ -e "$WAP_PLG_FILE" ] && fail "precondition: PLG_FILE should be absent for this check"
bash "$RC" install
marker="$WAP_PLUGIN_LOG_DIR/watch-aware-preloader.plg"
[ -L "$marker" ] || fail "cron marker symlink not created on fresh install"
[ -e "$marker" ] && fail "marker should be a DANGLING symlink (PLG_FILE absent)"

# --- TOML-injection hardening (hostile-review findings 1-3): values containing
# " \ or control chars must be escaped so config.toml stays valid and round-trips
# the literal, and a non-numeric numeric field must fall back to its default
# rather than inject an unquoted token. ---
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://x:8096/p\q"r"
USERS="al"ice, bob"
RAM_PERCENT="not-a-number"
TARGET_SECONDS="25"
MIN_HEAD_MB="8"
MAX_HEAD_MB="250"
TAIL_MB="1"
PATH_MAPS="/sh\are=>/mnt"x"
CRON_INTERVAL="10"
CFG
rm -f "$cfg"
bash "$RC" render
[ -f "$cfg" ] || fail "config.toml not generated (injection fixture)"
assert_not_contains "$cfg" "api_key"
# Non-numeric RAM_PERCENT falls back to the default (50); never injected unquoted.
assert_contains "$cfg" 'ram_percent = 50'

if python3 -c 'import tomllib' 2>/dev/null; then
    # Strongest check: the whole file parses AND the injecting values round-trip.
    python3 - "$cfg" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as fh:
    d = tomllib.load(fh)
assert d["server"]["url"] == 'http://x:8096/p\\q"r', d["server"]["url"]
assert d["users"]["enabled"] == ['al"ice', 'bob'], d["users"]["enabled"]
pm = d["path_map"]  # [[path_map]] is a top-level array-of-tables
assert pm[0]["from"] == '/sh\\are', pm[0]
assert pm[0]["to"] == '/mnt"x', pm[0]
assert "api_key" not in d.get("server", {})
print("  TOML round-trip OK (tomllib)")
PY
else
    # Fallback when tomllib is unavailable: assert the escaped bytes are present.
    assert_contains "$cfg" 'url = "http://x:8096/p\\q\"r"'
    assert_contains "$cfg" 'enabled = ["al\"ice", "bob"]'
    assert_contains "$cfg" 'from = "/sh\\are"'
    assert_contains "$cfg" 'to = "/mnt\"x"'
fi

# --- Control-char stripping (CR review finding 2): a stray control char (0x01)
# in a string field must be STRIPPED so config.toml stays valid TOML. ---
ctrl=$'\x01'
{
    printf 'SERVER_TYPE="emby"\n'
    printf 'SERVER_URL="http://localhost:8096"\n'
    printf 'USERS="al%sice, bob"\n' "$ctrl"
    printf 'RAM_PERCENT="50"\n'
    printf 'TARGET_SECONDS="20"\n'
    printf 'MIN_HEAD_MB="8"\n'
    printf 'MAX_HEAD_MB="250"\n'
    printf 'TAIL_MB="1"\n'
    printf 'PATH_MAPS="/sh%sare=>/mnt/user"\n' "$ctrl"
    printf 'CRON_INTERVAL="15"\n'
} > "$WAP_FLASH/watch-aware-preloader.cfg"
rm -f "$cfg"
bash "$RC" render
[ -f "$cfg" ] || fail "config.toml not generated (control-char fixture)"
if python3 -c 'import tomllib' 2>/dev/null; then
    python3 - "$cfg" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as fh:
    d = tomllib.load(fh)
# The 0x01 byte must be gone; the rest of each value survives.
assert d["users"]["enabled"] == ["alice", "bob"], d["users"]["enabled"]
assert d["path_map"][0]["from"] == "/share", d["path_map"][0]
print("  control char stripped, TOML valid (tomllib)")
PY
else
    assert_contains "$cfg" 'enabled = ["alice", "bob"]'
    assert_contains "$cfg" 'from = "/share"'
fi
# The raw control byte must not appear anywhere in the rendered file.
if LC_ALL=C grep -q "$ctrl" "$cfg"; then fail "control char leaked into config.toml"; fi

# --- CRON_INTERVAL=0 (CR review finding 4): 0 is invalid (*/0 never fires) and
# must fall back to the default 15, and the rendered step is always >= 1. ---
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://localhost:8096"
USERS=""
RAM_PERCENT="50"
TARGET_SECONDS="20"
MIN_HEAD_MB="8"
MAX_HEAD_MB="250"
TAIL_MB="1"
PATH_MAPS=""
CRON_INTERVAL="0"
CFG
rm -f "$cfg" "$cron"
bash "$RC" render
assert_contains "$cron" '*/15 * * * *'
assert_not_contains "$cron" '*/0 '

# --- tiers: defaults (no TIER_* keys) -> all enabled, max_items 0 ---
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
CFG
rm -f "$cfg"
bash "$RC" render
grep -q '^\[tiers.resume\]$'         "$cfg" || fail "missing [tiers.resume]"
grep -q '^\[tiers.next_up\]$'        "$cfg" || fail "missing [tiers.next_up]"
grep -q '^\[tiers.recently_added\]$' "$cfg" || fail "missing [tiers.recently_added]"
# Scope the default assertions to each tier block: an unscoped grep would pass if
# any single tier had the default even when another did not.
for t in resume next_up recently_added; do
    awk -v h="[tiers.$t]" '$0==h{f=1;next} f&&/^enabled = /{print;exit}'   "$cfg" | grep -q 'enabled = true' || fail "tier $t enabled default not true"
    awk -v h="[tiers.$t]" '$0==h{f=1;next} f&&/^max_items = /{print;exit}' "$cfg" | grep -q 'max_items = 0'   || fail "tier $t max_items default not 0"
done

# --- tiers: explicit values incl. a disabled tier and a cap ---
{ printf 'TIER_RESUME_ENABLED="1"\nTIER_RESUME_MAX="15"\n'; \
  printf 'TIER_NEXTUP_ENABLED="0"\nTIER_NEXTUP_MAX="0"\n'; \
  printf 'TIER_RECENT_ENABLED="1"\nTIER_RECENT_MAX="5"\n'; } >> "$WAP_FLASH/watch-aware-preloader.cfg"
bash "$RC" render
# resume enabled=true max=15; next_up enabled=false; recently_added max=5
awk '/^\[tiers.next_up\]$/{f=1;next} f&&/^enabled = /{print;exit}' "$cfg" | grep -q 'enabled = false' || fail "next_up not disabled"
awk '/^\[tiers.resume\]$/{f=1;next} f&&/^max_items = /{print;exit}' "$cfg" | grep -q 'max_items = 15' || fail "resume max not 15"

# --- tiers: [tiers] order and [tiers.override] (per-user tier priority) ---
# The override block is scoped out of the file before asserting: a bare id also
# appears in [users] enabled, so an unscoped grep would assert nothing.
override_block() {
    awk '/^\[tiers\.override\]$/{f=1;next} f&&/^\[/{f=0} f' "$cfg"
}

# TIER_ORDER absent: derive the order from the legacy *_ENABLED flags,
# preserving pre-order semantics exactly.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
TIER_RESUME_ENABLED="1"
TIER_NEXTUP_ENABLED="0"
TIER_RECENT_ENABLED="1"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" 'order = ["resume", "recent"]'

# [tiers] order must precede the [tiers.<tier>] sub-tables: TOML binds a bare
# key/value pair to the most recent table header, so an order emitted after
# [tiers.resume] would decode as tiers.resume.order and be silently ignored.
grep -n '^\[tiers' "$cfg" | head -n1 | grep -q '\[tiers\]$' || fail "[tiers] must be the first tiers table"

# TIER_ORDER present: honored verbatim, legacy flags ignored.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
TIER_ORDER="nextup,resume"
TIER_RESUME_ENABLED="0"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" 'order = ["nextup", "resume"]'

# A per-user override emits into [tiers.override]; users without one emit
# NOTHING (inheritance is by absence: emitting a copy of the global order would
# freeze that user against later global edits). Cfg keys cannot hold '-', so the
# page writes TIER_ORDER_<id with '-' as '_'>, but the emitted TOML key must be
# the ORIGINAL dashed id - that is what the engine matches MediaItem.UserID on.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a,id-b"
TIER_ORDER="resume,nextup"
TIER_ORDER_id_b="nextup"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" '[tiers.override]'
override_block | grep -qF '"id-b" = ["nextup"]' || fail "override for id-b not emitted:\n$(override_block)"
override_block | grep -qF 'id-a' && fail "id-a inherits by absence; must emit no override entry"

# An unknown tier name in the cfg must never reach config.toml: the cfg is
# untrusted input and an unknown tier fails the engine's config load.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
TIER_ORDER="resume,bogus,nextup"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" 'order = ["resume", "nextup"]'
assert_not_contains "$cfg" 'bogus'

# An unknown tier inside a per-user override is dropped the same way.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a"
TIER_ORDER_id_a="bogus,recent"
CFG
rm -f "$cfg"
bash "$RC" render
override_block | grep -qF '"id-a" = ["recent"]' || fail "unknown tier not dropped from override:\n$(override_block)"
assert_not_contains "$cfg" 'bogus'

# An empty order is legal and means "warm nothing". Only a fully ABSENT
# TIER_ORDER key falls back to the legacy derivation.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
TIER_ORDER=""
TIER_RESUME_ENABLED="1"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" 'order = []'

# An empty per-user override is legal too and must not collapse to inheritance.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a"
TIER_ORDER_id_a=""
CFG
rm -f "$cfg"
bash "$RC" render
override_block | grep -qF '"id-a" = []' || fail "empty override must render [], not inherit:\n$(override_block)"

# An EMPTY users csv means "all users at equal rank" - the shipped default.cfg
# value - and must NOT suppress per-user overrides. Discovering overrides from the
# users csv dropped every one of them here: the settings page offers the override
# control on every user row regardless of enrolment, so it reported the override
# as saved while the engine never received it. The dashed id is unrecoverable in
# this state (the page's '-' -> '_' transform is not invertible), so the key is
# emitted in its .cfg spelling and the engine binds it back (refCfgKey).
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS=""
TIER_ORDER="resume,nextup"
TIER_ORDER_id_b="nextup"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" '[tiers.override]'
override_block | grep -qF '"id_b" = ["nextup"]' ||
    fail "an override must render with no users enrolled:\n$(override_block)"

# An override for a user who is NOT enrolled still renders: enrolment and the
# override cascade are separate dials, and the engine ignores an override that
# binds to nobody. Suppressing it here would silently discard the operator's
# saved choice the moment they narrowed the user list.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a"
TIER_ORDER="resume,nextup"
TIER_ORDER_id_b="nextup"
CFG
rm -f "$cfg"
bash "$RC" render
override_block | grep -qF '"id_b" = ["nextup"]' ||
    fail "an unenrolled user's override must still render:\n$(override_block)"

# A duplicated tier must be deduped, not passed through. The engine's Validate
# rejects a duplicate as hard as an unknown name, so passing them through renders
# a config.toml the engine REFUSES to load - and render exits 0, so it fails later
# in syslog on a cron pass, far from the edit that caused it. Covers both the
# global order and a per-user override.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a"
TIER_ORDER="resume,nextup,resume,recent,nextup"
TIER_ORDER_id_a="recent,recent,resume"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" 'order = ["resume", "nextup", "recent"]'
override_block | grep -qF '"id-a" = ["recent", "resume"]' ||
    fail "a duplicated tier in an override must be deduped:\n$(override_block)"
# ...and the result must actually LOAD in the engine's own validator.
if python3 -c 'import tomllib' 2>/dev/null; then
    python3 - "$cfg" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as fh:
    d = tomllib.load(fh)
for label, order in [("tiers.order", d["tiers"]["order"])] + \
                    [(f"override.{k}", v) for k, v in d["tiers"].get("override", {}).items()]:
    assert len(order) == len(set(order)), f"{label} still holds duplicates: {order}"
print("  duplicate-tier dedupe OK (tomllib)")
PY
fi

# A literal '_' in an id or display name must not let a '-' twin steal its
# override. Both spellings normalize to "a_b", so a normalized-only match would
# emit "a-b" for the key written against "a_b" and apply one user's override to
# the other. Exact match wins, mirroring the engine's own precedence.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="a-b,a_b"
TIER_ORDER="resume,nextup"
TIER_ORDER_a_b="recent"
CFG
rm -f "$cfg"
bash "$RC" render
override_block | grep -qF '"a_b" = ["recent"]' ||
    fail "the exact-match id must win over its dash twin:\n$(override_block)"
assert_not_contains "$cfg" '"a-b" ='

# No overrides at all -> no empty [tiers.override] table.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a,id-b"
TIER_ORDER="resume"
CFG
rm -f "$cfg"
bash "$RC" render
assert_not_contains "$cfg" '[tiers.override]'

# A user id holding a regex metacharacter must never STEAL another user's
# override. The cfg lookup matches its key as a regex, so "J.Smith" would match
# the TIER_ORDER_JXSmith line: J.Smith has no key of its own and must inherit by
# absence. Hex GUIDs cannot trigger this, but a display name can ('.' '(' '*').
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="J.Smith,JXSmith"
TIER_ORDER_JXSmith="nextup"
CFG
rm -f "$cfg"
bash "$RC" render
override_block | grep -qF '"JXSmith" = ["nextup"]' || fail "override for JXSmith not emitted:\n$(override_block)"
override_block | grep -qF 'J.Smith' && fail "J.Smith has no override key; must inherit by absence, not steal JXSmith's:\n$(override_block)"

# An id whose transform cannot form a .cfg key is SKIPPED, never fatal: an
# unbalanced '[' is an invalid regex, and render must stay fail-safe (no abort,
# no override) rather than error out mid-file.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="a[b,id-c"
TIER_ORDER_id_c="recent"
CFG
rm -f "$cfg"
bash "$RC" render || fail "render must not abort on an id that cannot form a cfg key"
override_block | grep -qF '"id-c" = ["recent"]' || fail "override for id-c not emitted:\n$(override_block)"
override_block | grep -qF 'a[b' && fail "an id that cannot form a cfg key must emit no override"

# TWO OR MORE overrides at once: each must land on its OWN line. Building the
# block through a command substitution strips the trailing newline and collapses
# them onto one line as silently-invalid TOML.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a,id-b,id-c"
TIER_ORDER="resume,nextup"
TIER_ORDER_id_a="resume"
TIER_ORDER_id_c="nextup,recent"
CFG
rm -f "$cfg"
bash "$RC" render
override_block | grep -qxF '"id-a" = ["resume"]' || fail "id-a override not on its own line:\n$(override_block)"
override_block | grep -qxF '"id-c" = ["nextup", "recent"]' || fail "id-c override not on its own line:\n$(override_block)"
[ "$(override_block | grep -c '^"')" -eq 2 ] || fail "expected exactly 2 override lines:\n$(override_block)"
if python3 -c 'import tomllib' 2>/dev/null; then
    python3 - "$cfg" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as fh:
    d = tomllib.load(fh)
o = d["tiers"]["override"]
assert o == {"id-a": ["resume"], "id-c": ["nextup", "recent"]}, o
print("  multi-override round-trip OK (tomllib)")
PY
fi

# The absent-key marker is internal: a cfg VALUE that happens to equal it is
# data, not an absence. TIER_ORDER="__unset__" filters to an empty order, never
# a fallback to the legacy derivation.
cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a"
TIER_ORDER="__unset__"
TIER_RESUME_ENABLED="1"
TIER_ORDER_id_a="__unset__"
CFG
rm -f "$cfg"
bash "$RC" render
assert_contains "$cfg" 'order = []'
override_block | grep -qF '"id-a" = []' || fail "an override value of __unset__ is data, not inheritance:\n$(override_block)"

# The whole rendered file must stay valid TOML with the new tables present.
if python3 -c 'import tomllib' 2>/dev/null; then
    cat > "$WAP_FLASH/watch-aware-preloader.cfg" <<'CFG'
SERVER_TYPE="emby"
SERVER_URL="http://media.example:8096"
USERS="id-a,id-b"
TIER_ORDER="recent,resume"
TIER_ORDER_id_b="nextup,resume"
CFG
    rm -f "$cfg"
    bash "$RC" render
    python3 - "$cfg" <<'PY'
import sys, tomllib
with open(sys.argv[1], "rb") as fh:
    d = tomllib.load(fh)
t = d["tiers"]
assert t["order"] == ["recent", "resume"], t["order"]
assert t["override"] == {"id-b": ["nextup", "resume"]}, t["override"]
# The legacy dials must survive: PR A still reads them as migration input.
assert t["resume"]["max_items"] == 0, t["resume"]
print("  tiers order/override round-trip OK (tomllib)")
PY
fi

# --- write-pickers: assembles pickers.json from the three read-only
# subcommands, atomically and world-readable. ---
STUB_BIN="$work/preloadd"
cat > "$STUB_BIN" <<'STUB'
#!/bin/bash
case "$1" in
  list-users)
    python3 -m json.tool <<'JSON'
[{"id":"id-a","name":"Alice"}]
JSON
    ;;
  list-libraries)
    python3 -m json.tool <<'JSON'
[{"id":"lib-1","name":"Movies","type":"movies"}]
JSON
    ;;
  detect-pathmaps)
    python3 -m json.tool <<'JSON'
{"rules":[{"from":"/share/Movies","to":"/mnt/user/Movies","source":"docker"}],"unraid_unc_fallback":true}
JSON
    ;;
  *) exit 2 ;;
esac
STUB
chmod +x "$STUB_BIN"
printf 'SERVER_URL="http://tower:8096"\n' >> "$WAP_FLASH/watch-aware-preloader.cfg"
WAP_PICKERS_PATH="$work/pickers.json" WAP_BIN="$STUB_BIN" "$RC" write-pickers
grep -q '"server_url": *"http://tower:8096"' "$work/pickers.json" || fail "server_url not in cache"
grep -q '"id": "id-a"' "$work/pickers.json" || fail "users not merged"
grep -q '"id": "lib-1"' "$work/pickers.json" || fail "libraries not merged"
grep -q '"source": "docker"' "$work/pickers.json" || fail "pathmaps not merged"
# world-readable
perms="$(stat -c '%a' "$work/pickers.json" 2>/dev/null || stat -f '%Lp' "$work/pickers.json")"
[ "$perms" = "644" ] || fail "pickers.json not 0644 (got $perms)"

# --- write-pickers JSON-escaping: a SERVER_URL containing a double-quote
# (hand-edited .cfg, not run through the presave sanitizer) must be escaped in
# pickers.json rather than corrupting the JSON structure. ---
printf 'SERVER_URL="http://tow\"er:8096"\n' >> "$WAP_FLASH/watch-aware-preloader.cfg"
WAP_PICKERS_PATH="$work/pickers.json" WAP_BIN="$STUB_BIN" "$RC" write-pickers
grep -q 'tow\\"er' "$work/pickers.json" || fail "server_url quote not escaped in pickers.json"
if python3 -c 'import json' 2>/dev/null; then
    python3 - "$work/pickers.json" <<'PY'
import sys, json
with open(sys.argv[1]) as fh:
    d = json.load(fh)
assert d["server_url"] == 'http://tow"er:8096', d["server_url"]
print("  pickers.json valid JSON, server_url round-trips (json)")
PY
fi

# --- test connection persists a last-test.json result (#36) so the settings page
# can show a readable pass/fail after the transient /update.php progress popup
# closes. The connection itself needs no live server here: the no-URL and
# unreachable branches both persist ok=false deterministically offline. ---
lasttest="$work/last-test.json"

# No server URL -> ok=false, "No server URL configured.", still valid JSON.
printf 'SERVER_URL=""\n' > "$WAP_FLASH/watch-aware-preloader.cfg"
WAP_LASTTEST_PATH="$lasttest" bash "$RC" test || true
[ -f "$lasttest" ] || fail "last-test.json not written on no-URL test"
assert_contains "$lasttest" '"schema_version":1'
assert_contains "$lasttest" '"ok":false'
assert_contains "$lasttest" 'No server URL configured.'
perms="$(stat -c '%a' "$lasttest" 2>/dev/null || stat -f '%Lp' "$lasttest")"
[ "$perms" = "644" ] || fail "last-test.json not 0644 (got $perms)"
if python3 -c 'import json' 2>/dev/null; then
    python3 - "$lasttest" <<'PY'
import sys, json
with open(sys.argv[1]) as fh:
    d = json.load(fh)
assert d["ok"] is False, d
assert d["schema_version"] == 1, d
assert isinstance(d["tested_at"], str) and d["tested_at"], d
print("  last-test.json valid JSON (no-URL branch)")
PY
fi

# Unreachable server (refused port) -> ok=false, "not reachable".
printf 'SERVER_URL="http://127.0.0.1:1"\n' > "$WAP_FLASH/watch-aware-preloader.cfg"
rm -f "$lasttest"
WAP_LASTTEST_PATH="$lasttest" bash "$RC" test || true
[ -f "$lasttest" ] || fail "last-test.json not written on unreachable test"
assert_contains "$lasttest" '"ok":false'
assert_contains "$lasttest" 'not reachable'

echo "PASS: rc.preloadd render"
