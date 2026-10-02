<?php

declare(strict_types=1);

$wapImpl = __DIR__ . '/../src/usr/local/emhttp/plugins/watch-aware-preloader/include/pickers.php';
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

$tmp = tempnam(sys_get_temp_dir(), 'wappick');
file_put_contents($tmp, json_encode([
    'generated_at' => '2026-07-03T00:00:00Z',
    'server_url'   => 'http://tower:8096',
    'users'        => [['id' => 'id-a', 'name' => 'Alice']],
    'libraries'    => [['id' => 'lib-1', 'name' => 'Movies']],
    'pathmaps'     => ['rules' => [['from' => '/share/Movies', 'to' => '/mnt/user/Movies', 'source' => 'docker']], 'unraid_unc_fallback' => true],
]));

$p = wap_read_pickers($tmp);
check($p !== null, 'valid cache decodes');
check(wap_pickers_fresh($p, 'http://tower:8096'), 'fresh when url matches');
check(wap_pickers_fresh($p, 'http://tower:8096/'), 'fresh ignores trailing slash');
check(!wap_pickers_fresh($p, 'http://other:8096'), 'stale when url differs');
// A cache with no server_type predates Jellyfin support, so it came from Emby.
check(wap_pickers_fresh($p, 'http://tower:8096', 'emby'), 'legacy cache (no type) is fresh for emby');
check(!wap_pickers_fresh($p, 'http://tower:8096', 'jellyfin'), 'legacy cache (no type) is stale for jellyfin');
$typed = $p;
$typed['server_type'] = 'jellyfin';
check(wap_pickers_fresh($typed, 'http://tower:8096', 'jellyfin'), 'fresh when url and type match');
check(!wap_pickers_fresh($typed, 'http://tower:8096', 'emby'), 'stale when type differs at the same url');
check(count(wap_picker_users($p)) === 1, 'users accessor');
check(wap_picker_users($p)[0]['id'] === 'id-a', 'user id readable');
check(count(wap_picker_libraries($p)) === 1, 'libraries accessor');
check(wap_picker_pathmaps($p)['unraid_unc_fallback'] === true, 'pathmaps accessor');

check(wap_read_pickers($tmp . '.missing') === null, 'missing file -> null');
file_put_contents($tmp, 'not json');
check(wap_read_pickers($tmp) === null, 'invalid json -> null');
check(!wap_pickers_fresh(null, 'http://tower:8096'), 'null cache never fresh');
check(wap_picker_users(null) === [], 'null users -> empty');

unlink($tmp);
if ($failures > 0) {
    fwrite(STDERR, "{$failures} failure(s)\n");
    exit(1);
}
echo "pickers_test: OK\n";
