<?php

declare(strict_types=1);

// Settings sanitization helpers, applied by the root #include hook (presave.php)
// BEFORE /update.php writes the flash .cfg. Unraid serves direct plugin PHP
// endpoints as the unprivileged "nobody" user, which cannot write the 0600-root
// FAT flash dir; only /update.php (which runs as root) can. So the settings save
// goes through /update.php, and this code normalizes the posted values in place
// so /update.php writes clean, bounded KEY="value" pairs. No I/O here - pure
// transforms, unit-tested by test/settings_test.php.

/**
 * Sanitize a free-text .cfg scalar. /update.php writes values as KEY="$value"
 * with NO escaping, and rc.preloadd's cfg_get strips one surrounding quote pair
 * without unescaping, so the robust policy is to REMOVE characters that could
 * break the KEY="value" line rather than escape them. Control chars (newline/CR/
 * tab/etc.) are the real line-injection vector; double-quote and backslash never
 * appear legitimately in a server URL, comma-separated user list, or path-map,
 * so dropping them is lossless for real input and closes the injection surface.
 */
function wap_cfg_sanitize_str(string $v): string
{
    $v = preg_replace('/[\x00-\x1F\x7F"\\\\]/', '', $v);

    return trim((string) $v);
}

/**
 * Normalize a posted list-or-scalar field into a sanitized comma-separated cfg
 * value. Checkbox pickers post an array (USERS[]/LIBRARIES[]); a legacy free-text
 * field posts a scalar. Each element is run through wap_cfg_sanitize_str; empty
 * elements are dropped.
 *
 * @param mixed $v array<int,scalar>|scalar|null
 */
function wap_cfg_csv_from_list(mixed $v): string
{
    if (\is_array($v)) {
        $parts = [];
        foreach ($v as $item) {
            if (!\is_scalar($item)) {
                continue;
            }
            // Drop commas too: items are joined with "," and split back with
            // explode(",") on the page, so a comma in an id/name would split
            // into bogus selections. (Emby ids are GUIDs, but harden anyway.)
            $s = str_replace(',', '', wap_cfg_sanitize_str((string) $item));
            if ($s !== '') {
                $parts[] = $s;
            }
        }

        return implode(',', $parts);
    }

    return wap_cfg_sanitize_str((string) ($v ?? ''));
}

/**
 * A checkbox posts a value only when checked, so a present value other than the
 * literal "0" means enabled; absence (or a "0") means disabled. Returns "1" or "0".
 *
 * @param mixed $v
 */
function wap_cfg_checkbox(mixed $v): string
{
    return (\is_scalar($v) && (string) $v !== '' && (string) $v !== '0') ? '1' : '0';
}

/**
 * Coerce a posted numeric field to an int within [$min, $max], falling back to
 * $default when the value is not a plain decimal or is out of range. Only decimal
 * digits (optionally signed, optionally with a fractional part that is then
 * truncated) are accepted; is_numeric() would also pass scientific notation like
 * "1e2" - which a number input can submit - and (int) "1e2" is 1, silently
 * mis-clamping. Reject those to $default.
 */
function wap_cfg_clamp_int(mixed $v, int $min, int $max, int $default): int
{
    if (!\is_scalar($v) || preg_match('/^\s*-?\d+(?:\.\d+)?\s*$/', (string) $v) !== 1) {
        return $default;
    }
    $n = (int) $v;
    if ($n < $min) {
        return $min;
    }
    if ($n > $max) {
        return $max;
    }

    return $n;
}

/**
 * Normalize a tier-order field to a CSV of known tier names, in the posted order.
 * Unknown names and duplicates are dropped. Valid cfg spellings are exactly
 * resume, nextup and recent. An empty result is LEGAL and means "warm nothing":
 * it must not be back-filled with the default order.
 *
 * @param mixed $v a CSV string or a list of tier names
 */
function wap_cfg_tier_order(mixed $v): string
{
    $known = ['resume', 'nextup', 'recent'];
    $items = \is_array($v) ? $v : explode(',', (string) (\is_scalar($v) ? $v : ''));
    $out   = [];
    foreach ($items as $item) {
        if (!\is_scalar($item)) {
            continue;
        }
        $item = strtolower(trim((string) $item));
        if (\in_array($item, $known, true) && !\in_array($item, $out, true)) {
            $out[] = $item;
        }
    }

    return implode(',', $out);
}

/**
 * Reconcile a tier-order CSV against the per-tier TIER_<K>_ENABLED tickboxes.
 *
 * The order CSV carries ORDERING and is maintained by order.js; the tickboxes
 * carry ENABLEMENT and post on their own. With JS the two agree by construction.
 * Without it they do not, and rc.preloadd prefers the CSV, so the engine acted on
 * a stale order while the page re-rendered the boxes from it - the operator's
 * ticks silently reverted. Reconciling makes the tickboxes authoritative for
 * membership while the CSV keeps its positions:
 *   - a tier whose box is off is REMOVED from the order (absence is disablement)
 *   - a tier whose box is on but is missing from the order is APPENDED (last,
 *     because the operator expressed no position for it)
 *
 * GUARDED on the tier section actually being present in this POST. An unchecked
 * checkbox posts NOTHING, so "no tickbox fields at all" is indistinguishable from
 * "every tier off" by the checkboxes alone - and reading it as all-off would let a
 * POST that never rendered the tier section silently wipe the saved order. The
 * TIER_<K>_MAX number inputs are the reliable presence signal: a number input
 * always posts, checked or not. Detect on the RAW post, before the caller's
 * clamping loop materializes those keys.
 *
 * @param array<string, mixed> $post the request map, read-only here
 * @param string $csv an already-normalized order CSV (see wap_cfg_tier_order)
 */
function wap_cfg_reconcile_order(string $csv, array $post): string
{
    // token => the cfg-prefix naming its legacy tickbox.
    $boxes = ['resume' => 'RESUME', 'nextup' => 'NEXTUP', 'recent' => 'RECENT'];

    $sectionPosted = false;
    foreach ($boxes as $pfx) {
        if (\array_key_exists("TIER_{$pfx}_MAX", $post)) {
            $sectionPosted = true;
            break;
        }
    }
    if (!$sectionPosted) {
        return $csv; // tier section absent from this POST: nothing to reconcile
    }

    $order = ($csv === '') ? [] : explode(',', $csv);

    $out = [];
    foreach ($order as $tok) {
        if (wap_cfg_checkbox($post["TIER_{$boxes[$tok]}_ENABLED"] ?? null) === '1') {
            $out[] = $tok;
        }
    }
    foreach ($boxes as $tok => $pfx) {
        if (wap_cfg_checkbox($post["TIER_{$pfx}_ENABLED"] ?? null) === '1' && !\in_array($tok, $out, true)) {
            $out[] = $tok;
        }
    }

    return implode(',', $out);
}

/**
 * Remove the per-user override keys the operator switched OFF.
 *
 * REQUIRED because /update.php MERGES rather than rewrites: it seeds its key map
 * from the EXISTING .cfg and only overlays the POSTed fields, so a key nobody
 * posts SURVIVES with its old value. "Not posted" therefore means KEEP, not
 * DELETE, and the disabled-input trick can never express removal - unticking an
 * override was a permanent no-op and the box came back ticked on re-render.
 *
 * The page posts one always-enabled '#wap_ov_<suffix>' flag per override control.
 * A '#'-prefixed field reaches $_POST but is never written to the .cfg, the same
 * idiom as '#clear_api_key'. Only an EXPLICIT '0' removes: a MISSING flag means
 * "leave this key alone", so a save from a branch that renders no user rows (the
 * connect gate) cannot wipe saved overrides.
 *
 * $keys is /update.php's own pre-seeded key map, which is in scope for an
 * '#include' hook. Removing the key from $post alone is NOT enough - that only
 * stops the overlay, leaving the stale file value in place.
 *
 * @param array<string, mixed> $post the request map, mutated in place
 * @param array<string, mixed> $keys /update.php's key map, mutated in place
 */
function wap_apply_override_removals(array &$post, array &$keys): void
{
    foreach (array_keys($post) as $flag) {
        if (!\is_string($flag) || !str_starts_with($flag, '#wap_ov_')) {
            continue;
        }
        $suffix = substr($flag, \strlen('#wap_ov_'));
        if (preg_match('/^[A-Za-z0-9_]+$/D', $suffix) !== 1) {
            continue;
        }
        if ((string) (\is_scalar($post[$flag]) ? $post[$flag] : '') !== '0') {
            continue; // on, or unrecognized -> leave the key alone
        }
        unset($post["TIER_ORDER_{$suffix}"], $keys["TIER_ORDER_{$suffix}"]);
    }
}

/**
 * Normalize the settings fields in $post IN PLACE so /update.php writes a clean,
 * bounded .cfg. Only the fields the form posts are touched; every other engine
 * default is left to rc.preloadd's cfg_get fallbacks. Numeric fields are clamped
 * to their valid ranges; string fields are sanitized; SERVER_TYPE is constrained
 * to the only adapter shipping in Phase 2 so a spoofed value cannot select an
 * unsupported server.
 *
 * @param array<string, mixed> $post the request map (typically $_POST), mutated in place
 */
function wap_sanitize_settings_post(array &$post): void
{
    // Only the Emby adapter ships in Phase 2, so pin it regardless of input.
    $post['SERVER_TYPE'] = 'emby';

    $url = wap_cfg_sanitize_str((string) ($post['SERVER_URL'] ?? ''));
    $post['SERVER_URL'] = ($url === '') ? 'http://localhost:8096' : $url;

    $post['USERS']     = wap_cfg_csv_from_list($post['USERS'] ?? '');
    $post['LIBRARIES'] = wap_cfg_csv_from_list($post['LIBRARIES'] ?? '');
    $post['PATH_MAPS'] = wap_cfg_sanitize_str((string) ($post['PATH_MAPS'] ?? ''));

    $post['TIER_ORDER'] = wap_cfg_tier_order($post['TIER_ORDER'] ?? '');
    // Reconcile the order csv against the per-tier tickboxes. TIER_ORDER is
    // maintained by order.js; TIER_<K>_ENABLED posts on its own. Without JS (or
    // before it loads) the two disagree and rc.preloadd prefers TIER_ORDER, so the
    // engine warmed the INVERSE of what the operator ticked and the page then
    // snapped the boxes back. The presave always runs, so reconciling here is the
    // only place the two cannot drift.
    $post['TIER_ORDER'] = wap_cfg_reconcile_order($post['TIER_ORDER'], $post);

    // Per-user overrides: normalize only the keys actually posted, and only keys
    // that MATCH THE EXPECTED SHAPE. /update.php writes each key verbatim as
    // KEY="value", so a crafted key containing a quote and a newline would emit a
    // second .cfg line that reads as a first-class setting - defeating the clamps
    // this function exists to apply. The suffix charset matches rc.preloadd's
    // cfg_key_safe. A bare 'TIER_ORDER_' with no suffix is junk and is dropped
    // rather than written as permanent dead weight.
    foreach (array_keys($post) as $key) {
        if (!\is_string($key) || !str_starts_with($key, 'TIER_ORDER_')) {
            continue;
        }
        if (preg_match('/^TIER_ORDER_[A-Za-z0-9_]+$/D', $key) !== 1) {
            unset($post[$key]);
            continue;
        }
        $post[$key] = wap_cfg_tier_order($post[$key]);
    }

    $post['RAM_PERCENT']    = (string) wap_cfg_clamp_int($post['RAM_PERCENT'] ?? null, 1, 100, 50);
    $post['TARGET_SECONDS'] = (string) wap_cfg_clamp_int($post['TARGET_SECONDS'] ?? null, 1, 86400, 20);
    $post['CRON_INTERVAL']  = (string) wap_cfg_clamp_int($post['CRON_INTERVAL'] ?? null, 1, 59, 15);

    foreach (['RESUME', 'NEXTUP', 'RECENT'] as $t) {
        $post["TIER_{$t}_ENABLED"] = wap_cfg_checkbox($post["TIER_{$t}_ENABLED"] ?? null);
        $post["TIER_{$t}_MAX"]     = (string) wap_cfg_clamp_int($post["TIER_{$t}_MAX"] ?? null, 0, 10000, 0);
    }
}
