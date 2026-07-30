<?php

declare(strict_types=1);

$wapImpl = __DIR__ . '/../src/usr/local/emhttp/plugins/watch-aware-preloader/include/settings.php';
if (!is_file($wapImpl)) {
    fwrite(STDERR, "FAIL: implementation not found: {$wapImpl}\n");
    exit(1);
}
require_once $wapImpl;

$failures = 0;
function check(bool $cond, string $msg): void
{
    global $failures;
    if (!$cond) {
        fwrite(STDERR, "FAIL: {$msg}\n");
        $failures++;
    }
}

// --- wap_cfg_sanitize_str ---
check(wap_cfg_sanitize_str('  trim me  ') === 'trim me', 'sanitize trims');
check(wap_cfg_sanitize_str("a\"b") === 'ab', 'double-quote stripped');
check(wap_cfg_sanitize_str('a\\b') === 'ab', 'backslash stripped');
check(wap_cfg_sanitize_str("a\nb\tc") === 'abc', 'control chars stripped');
check(wap_cfg_sanitize_str('/share=>/mnt/user; /m=>/mnt/m') === '/share=>/mnt/user; /m=>/mnt/m', 'path map preserved');

// --- wap_cfg_clamp_int ---
check(wap_cfg_clamp_int('50', 1, 100, 10) === 50, 'clamp passes in-range');
check(wap_cfg_clamp_int('0', 1, 100, 10) === 1, 'clamp below min -> min');
check(wap_cfg_clamp_int('999', 1, 100, 10) === 100, 'clamp above max -> max');
check(wap_cfg_clamp_int('', 1, 100, 10) === 10, 'clamp empty -> default');
check(wap_cfg_clamp_int('abc', 1, 100, 10) === 10, 'clamp non-numeric -> default');
check(wap_cfg_clamp_int('7.9', 1, 100, 10) === 7, 'clamp truncates float');
check(wap_cfg_clamp_int('1e2', 1, 100, 10) === 10, 'clamp rejects scientific notation');
check(wap_cfg_clamp_int('0x10', 1, 100, 10) === 10, 'clamp rejects hex');
check(wap_cfg_clamp_int('  25  ', 1, 100, 10) === 25, 'clamp tolerates surrounding whitespace');

// --- wap_sanitize_settings_post: normalizes $_POST in place for /update.php ---
$post = [
    'SERVER_TYPE'    => 'jellyfin',                     // spoofed -> pinned to emby
    'SERVER_URL'     => "http://tower:8096\n",          // trailing newline stripped
    'USERS'          => 'alice, bob',
    'RAM_PERCENT'    => '999',                          // clamped to 100
    'TARGET_SECONDS' => 'abc',                          // -> default 20
    'PATH_MAPS'      => '/library=>/mnt/user/media',
    'CRON_INTERVAL'  => '0',                            // clamped to 1
];
wap_sanitize_settings_post($post);
check($post['SERVER_TYPE'] === 'emby', 'server type pinned to emby');
check($post['SERVER_URL'] === 'http://tower:8096', 'server url sanitized');
check($post['USERS'] === 'alice, bob', 'users preserved');
check($post['RAM_PERCENT'] === '100', 'ram clamped to max (string)');
check($post['TARGET_SECONDS'] === '20', 'target non-numeric -> default (string)');
check($post['PATH_MAPS'] === '/library=>/mnt/user/media', 'path maps preserved');
check($post['CRON_INTERVAL'] === '1', 'cron clamped to min (string)');

// Empty/missing fields fall back to documented defaults.
$empty = [];
wap_sanitize_settings_post($empty);
check($empty['SERVER_URL'] === 'http://localhost:8096', 'default server url');
check($empty['USERS'] === '', 'default users empty');
check($empty['RAM_PERCENT'] === '50', 'default ram 50');
check($empty['CRON_INTERVAL'] === '15', 'default cron 15');

// Injection: a newline in a value cannot survive to break the KEY="value" line.
$evil = ['SERVER_URL' => "http://x\nINJECTED=pwned"];
wap_sanitize_settings_post($evil);
check(!str_contains($evil['SERVER_URL'], "\n"), 'newline stripped from value');

// --- wap_cfg_csv_from_list ---
check(wap_cfg_csv_from_list(['id-a', 'id-b']) === 'id-a,id-b', 'array joined to csv');
check(wap_cfg_csv_from_list(['id-a', '', ' id-b ']) === 'id-a,id-b', 'array trims and drops empties');
check(wap_cfg_csv_from_list('legacy,names') === 'legacy,names', 'scalar passes through sanitized');
check(wap_cfg_csv_from_list(["a\"b", 'c']) === 'ab,c', 'array elements sanitized');
check(wap_cfg_csv_from_list(['a', ['nested'], 'b']) === 'a,b', 'non-scalar array items skipped');
check(wap_cfg_csv_from_list(['Movies, TV', 'x']) === 'Movies TV,x', 'commas stripped from items (delimiter safety)');
check(wap_cfg_csv_from_list(null) === '', 'null yields empty string');

// --- wap_sanitize_settings_post: USERS[]/LIBRARIES[] arrays ---
$post = ['USERS' => ['id-a', 'id-b'], 'LIBRARIES' => ['lib-1']];
wap_sanitize_settings_post($post);
check($post['USERS'] === 'id-a,id-b', 'USERS array normalized to csv');
check($post['LIBRARIES'] === 'lib-1', 'LIBRARIES array normalized to csv');

$post2 = [];
wap_sanitize_settings_post($post2);
check($post2['LIBRARIES'] === '', 'LIBRARIES defaults empty');

// --- tier dials ---
$post = [
    'TIER_RESUME_ENABLED' => '1', 'TIER_RESUME_MAX' => '15',
    'TIER_NEXTUP_MAX' => '0',                 // NEXTUP_ENABLED absent (unchecked)
    'TIER_RECENT_ENABLED' => 'on', 'TIER_RECENT_MAX' => '5',
];
wap_sanitize_settings_post($post);
check($post['TIER_RESUME_ENABLED'] === '1', 'resume enabled normalized to 1');
check($post['TIER_RESUME_MAX'] === '15', 'resume max preserved');
check($post['TIER_NEXTUP_ENABLED'] === '0', 'absent tier checkbox => 0');
check($post['TIER_RECENT_ENABLED'] === '1', 'any present checkbox value => 1');
check($post['TIER_RECENT_MAX'] === '5', 'recent max preserved');

$empty = [];
wap_sanitize_settings_post($empty);
check($empty['TIER_RESUME_ENABLED'] === '0', 'all-absent => disabled flag 0');
check($empty['TIER_RESUME_MAX'] === '0', 'max default 0');

// --- wap_cfg_tier_order ---
check(wap_cfg_tier_order('resume,bogus,nextup,resume') === 'resume,nextup', 'tier order drops unknown and duplicates');
check(wap_cfg_tier_order(['nextup', 'resume']) === 'nextup,resume', 'tier order accepts an array');
check(wap_cfg_tier_order('nextup, resume') === 'nextup,resume', 'tier order accepts csv with spaces');
check(wap_cfg_tier_order('RESUME,NextUp') === 'resume,nextup', 'tier order lowercases');
check(wap_cfg_tier_order('') === '', 'tier order empty stays empty');
check(wap_cfg_tier_order('bogus') === '', 'tier order all-unknown yields empty');

// --- wap_sanitize_settings_post: tier order ---
$post = ['TIER_ORDER' => 'resume,bogus,nextup,resume'];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === 'resume,nextup', 'posted tier order normalized');

$post = ['TIER_ORDER' => ['nextup', 'resume']];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === 'nextup,resume', 'posted tier order array normalized');

$post = ['TIER_ORDER' => 'nextup, resume'];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === 'nextup,resume', 'posted tier order csv normalized');

// An empty order is legal and means "warm nothing". It must NOT silently become
// the default order.
$post = ['TIER_ORDER' => ''];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === '', 'empty tier order preserved, not back-filled');

// Per-user overrides: only posted keys are normalized. Inheritance is by absence,
// so a user without an override key must stay absent.
$post = [
    'USERS'           => ['id-a', 'id-b'],
    'TIER_ORDER'      => 'resume,nextup',
    'TIER_ORDER_id_b' => 'bogus,nextup',
];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER_id_b'] === 'nextup', 'per-user override normalized');
check(!isset($post['TIER_ORDER_id_a']), 'a user without an override must stay absent');

// An absent global TIER_ORDER normalizes to empty rather than a materialized default.
$empty = [];
wap_sanitize_settings_post($empty);
check($empty['TIER_ORDER'] === '', 'absent tier order defaults to empty');

// A per-user override posted empty stays empty (that user warms nothing) and is
// NOT dropped: absence and an explicit empty override are different states.
$post = ['TIER_ORDER' => 'resume', 'TIER_ORDER_id_a' => ''];
wap_sanitize_settings_post($post);
check(isset($post['TIER_ORDER_id_a']) && $post['TIER_ORDER_id_a'] === '', 'explicit empty override kept distinct from absence');

// --- wap_cfg_reconcile_order: the tickboxes are authoritative for MEMBERSHIP ---
// Without JS, TIER_ORDER is the server-rendered value while the tickboxes carry
// the operator's actual clicks. rc.preloadd prefers TIER_ORDER, so an
// unreconciled save made the engine act on the inverse of what was ticked.
// TIER_*_MAX is present in all of these: a number input always posts, and it is
// what marks the tier section as having been rendered in this POST.
$post = [
    'TIER_ORDER'        => 'resume,nextup',
    'TIER_RESUME_MAX'   => '0',
    'TIER_NEXTUP_ENABLED' => '1',
    'TIER_RECENT_ENABLED' => '1',
];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === 'nextup,recent', 'unticked tier dropped, newly-ticked appended');

// Positions in the csv survive reconciliation; only membership changes.
$post = [
    'TIER_ORDER'          => 'recent,nextup,resume',
    'TIER_RESUME_MAX'     => '0',
    'TIER_RECENT_ENABLED' => '1',
    'TIER_RESUME_ENABLED' => '1',
];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === 'recent,resume', 'reconciliation preserves the posted order');

// A POST that never rendered the tier section must NOT be read as "all tiers off"
// - an unchecked box posts nothing, so that would wipe the saved order.
$post = ['TIER_ORDER' => 'resume,nextup'];
wap_sanitize_settings_post($post);
check($post['TIER_ORDER'] === 'resume,nextup', 'absent tier section leaves the order alone');

// --- wap_apply_override_removals: the ONLY way to delete an override key ---
// /update.php MERGES: it seeds its key map from the existing .cfg and overlays
// POST, so an unposted key SURVIVES. This emulates that write faithfully (see
// emhttp/update.php: $keys = parse_ini_file(...); overlay; write all of $keys).
$mergeWrite = static function (array $existing, array $post): array {
    $keys = $existing;                       // seeded from the existing file
    wap_sanitize_settings_post($post);       // the #include hook, step 1
    wap_apply_override_removals($post, $keys); // the #include hook, step 2
    foreach ($post as $k => $v) {            // update.php's overlay
        if ($k[0] !== '#') {
            $keys[$k] = $v;
        }
    }
    return $keys;
};

// Unticking the override must REMOVE the key. This is the Critical: it was a
// permanent no-op, and the box came back ticked on the next render.
$out = $mergeWrite(
    ['TIER_ORDER_id_a' => 'recent', 'TIER_ORDER' => 'resume'],
    ['TIER_ORDER' => 'resume', '#wap_ov_id_a' => '0']
);
check(!isset($out['TIER_ORDER_id_a']), 'unticking an override removes the key');

// Ticking it keeps the posted value.
$out = $mergeWrite(
    ['TIER_ORDER_id_a' => 'recent'],
    ['TIER_ORDER' => 'resume', '#wap_ov_id_a' => '1', 'TIER_ORDER_id_a' => 'nextup']
);
check(($out['TIER_ORDER_id_a'] ?? null) === 'nextup', 'an on override keeps its posted value');

// An explicit EMPTY override is a real choice ("warm nothing") and must survive as
// a present-but-empty key, distinct from removal.
$out = $mergeWrite(
    ['TIER_ORDER_id_a' => 'recent'],
    ['#wap_ov_id_a' => '1', 'TIER_ORDER_id_a' => '']
);
check(array_key_exists('TIER_ORDER_id_a', $out) && $out['TIER_ORDER_id_a'] === '', 'empty override survives as present-but-empty');

// A MISSING flag means "leave alone": a save from a branch that renders no user
// rows (the connect gate) must not wipe saved overrides.
$out = $mergeWrite(
    ['TIER_ORDER_id_a' => 'recent'],
    ['TIER_ORDER' => 'resume']
);
check(($out['TIER_ORDER_id_a'] ?? null) === 'recent', 'a missing flag leaves the override untouched');

// The flag suffix is validated, so a crafted flag cannot unset an arbitrary key.
$keys = ['TIER_ORDER_id_a' => 'recent', 'RAM_PERCENT' => '50'];
$post = ['#wap_ov_id_a"; RAM_PERCENT' => '0'];
wap_apply_override_removals($post, $keys);
check(($keys['RAM_PERCENT'] ?? null) === '50', 'a malformed removal flag is ignored');
check(($keys['TIER_ORDER_id_a'] ?? null) === 'recent', 'a malformed removal flag removes nothing');

// --- key-shape validation: /update.php writes keys VERBATIM as KEY="value" ---
// A quote-and-newline in a key would emit a second .cfg line read as a
// first-class setting, defeating the clamps this file exists to apply.
$hostileKey = 'TIER_ORDER_a"' . "\n" . 'RAM_PERCENT=999999';
$post = [
    'TIER_ORDER'  => 'resume',
    $hostileKey   => 'recent',
    'TIER_ORDER_' => 'recent',
];
wap_sanitize_settings_post($post);
check(!isset($post[$hostileKey]), 'a key with a quote/newline is dropped');
check(!isset($post['TIER_ORDER_']), 'a bare TIER_ORDER_ with no suffix is dropped');
foreach (array_keys($post) as $k) {
    if (str_starts_with((string) $k, 'TIER_ORDER_')) {
        check(preg_match('/^TIER_ORDER_[A-Za-z0-9_]+$/D', (string) $k) === 1, "surviving override key is well-formed: {$k}");
    }
}

if ($failures > 0) {
    fwrite(STDERR, "{$failures} failure(s)\n");
    exit(1);
}
echo "PASS: settings sanitizer\n";
