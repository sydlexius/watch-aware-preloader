<?php

declare(strict_types=1);

// Read-only helpers for the Watch-Aware Preloader status panel. No secrets, no
// TOML: this reads only the engine's status.json and the connection-test result
// (both JSON) and formats times.

const WAP_STATUS_SCHEMA_VERSION = 1;
const WAP_LAST_TEST_SCHEMA_VERSION = 1;
const WAP_ESTIMATE_SCHEMA_VERSION = 1;

/**
 * Decode and validate the engine's status.json.
 *
 * @return array<string, mixed>|null the decoded status, or null when the file is
 *         missing, unreadable, not valid JSON, or a different schema version.
 */
function wap_read_status(string $path): ?array
{
    if (!is_file($path)) {
        return null;
    }
    $raw = @file_get_contents($path);
    if ($raw === false) {
        return null;
    }
    $data = json_decode($raw, true);
    if (!\is_array($data)) {
        return null;
    }
    if (($data['schema_version'] ?? null) !== WAP_STATUS_SCHEMA_VERSION) {
        return null;
    }
    return $data;
}

/**
 * Decode the connection-test result written by `rc.preloadd test` (#36).
 *
 * @return array<string, mixed>|null the decoded result, or null when the file is
 *         missing, unreadable, not valid JSON, or a different schema version.
 */
function wap_read_last_test(string $path): ?array
{
    if (!is_file($path)) {
        return null;
    }
    $raw = @file_get_contents($path);
    if ($raw === false) {
        return null;
    }
    $data = json_decode($raw, true);
    if (!\is_array($data)) {
        return null;
    }
    if (($data['schema_version'] ?? null) !== WAP_LAST_TEST_SCHEMA_VERSION) {
        return null;
    }
    return $data;
}

/**
 * Decode and validate the engine's estimate.json (the -estimate projection).
 *
 * @return array<string, mixed>|null the decoded estimate, or null when the file
 *         is missing, unreadable, not valid JSON, or a different schema version.
 */
function wap_read_estimate(string $path): ?array
{
    if (!is_file($path)) {
        return null;
    }
    $raw = @file_get_contents($path);
    if ($raw === false) {
        return null;
    }
    $data = json_decode($raw, true);
    if (!\is_array($data)) {
        return null;
    }
    if (($data['schema_version'] ?? null) !== WAP_ESTIMATE_SCHEMA_VERSION) {
        return null;
    }
    return $data;
}

/**
 * Format a byte count as GiB with one decimal, matching the units the budget
 * meter's JavaScript uses (wapFmtGiB in js/meter.js) so the server-rendered bar
 * and the live projection cannot disagree on how the same number reads.
 */
function wap_gib(int $bytes): string
{
    return number_format($bytes / 1073741824, 1, '.', '') . ' GiB';
}

/**
 * Convert an RFC3339 UTC timestamp to a labeled US Pacific string, e.g.
 * "2026-06-30 14:05:00 PDT". Returns the input unchanged if it cannot be parsed.
 */
function wap_format_pacific(string $rfc3339utc): string
{
    try {
        $dt = new DateTimeImmutable($rfc3339utc);
    } catch (Exception $e) {
        return $rfc3339utc;
    }
    $dt = $dt->setTimezone(new DateTimeZone('America/Los_Angeles'));
    return $dt->format('Y-m-d H:i:s T');
}
