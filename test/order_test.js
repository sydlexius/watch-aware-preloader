'use strict';
const assert = require('assert');
const { wapReorder, wapOrderCsv } = require('../src/usr/local/emhttp/plugins/watch-aware-preloader/js/order.js');

// Moving up swaps with the previous item and does not mutate the input.
const a = ['resume', 'nextup', 'recent'];
assert.deepStrictEqual(wapReorder(a, 1, -1), ['nextup', 'resume', 'recent']);
assert.deepStrictEqual(a, ['resume', 'nextup', 'recent'], 'must not mutate input');

// Moving down swaps with the next item.
assert.deepStrictEqual(wapReorder(a, 0, 1), ['nextup', 'resume', 'recent']);

// Out-of-range moves are no-ops, not errors: the first item cannot move up and
// the last cannot move down. No wrap-around, no throw.
assert.deepStrictEqual(wapReorder(a, 0, -1), ['resume', 'nextup', 'recent']);
assert.deepStrictEqual(wapReorder(a, 2, 1), ['resume', 'nextup', 'recent']);

// A single-item list is stable.
assert.deepStrictEqual(wapReorder(['resume'], 0, -1), ['resume']);

// An index outside the list is a no-op rather than a throw (scripted callers).
assert.deepStrictEqual(wapReorder(a, -1, 1), ['resume', 'nextup', 'recent']);
assert.deepStrictEqual(wapReorder(a, 9, -1), ['resume', 'nextup', 'recent']);

// wapOrderCsv: the wire format is the enabled tiers, in list order. Literals use
// the TIER_ORDER / engine vocabulary (resume/nextup/recent - see
// tier_csv_filter in scripts/rc.preloadd and ParseTierName in
// internal/config/config.go), not meter.js's estimate-row vocabulary.
assert.strictEqual(
  wapOrderCsv([
    { tier: 'resume', enabled: true },
    { tier: 'nextup', enabled: true },
    { tier: 'recent', enabled: true },
  ]),
  'resume,nextup,recent'
);

// Absence from the order IS disablement: an unticked tier is omitted entirely.
assert.strictEqual(
  wapOrderCsv([
    { tier: 'resume', enabled: true },
    { tier: 'nextup', enabled: false },
    { tier: 'recent', enabled: true },
  ]),
  'resume,recent'
);

// Every tier disabled -> empty CSV, not a placeholder.
assert.strictEqual(
  wapOrderCsv([
    { tier: 'resume', enabled: false },
    { tier: 'nextup', enabled: false },
  ]),
  ''
);

console.log('PASS: wapReorder + wapOrderCsv');
