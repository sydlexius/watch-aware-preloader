'use strict';
const assert = require('assert');
const { wapAggregate } = require('../src/usr/local/emhttp/plugins/watch-aware-preloader/js/meter.js');

const T = () => ({
  resume: { enabled: true, max: 0 },
  'next-up': { enabled: true, max: 0 },
  'recently-added': { enabled: true, max: 0 },
});
// rows: r is global rank (0 highest priority)
const rows = [
  { u: '3', t: 'resume', l: 'L1', b: 100, r: 0 },
  { u: '3', t: 'resume', l: 'L1', b: 100, r: 1 },
  { u: '7', t: 'next-up', l: 'L2', b: 100, r: 2 },
  { u: '7', t: 'recently-added', l: 'L2', b: 100, r: 3 },
];

// All selected, budget covers everything -> projected 400, not over, no drops.
let a = wapAggregate(rows, 1000, { users: new Set(), libraries: new Set(), tiers: T() });
assert.strictEqual(a.projected, 400);
assert.strictEqual(a.over, false);
assert.strictEqual(a.dropCount, 0);

// Budget 250 -> warms r0,r1 (cum 200), r2 pushes cum to 300 > 250 -> cutline at r2.
// projected still 400 (total selected); dropped = r2,r3 (2), by tier next-up 1, recently-added 1.
a = wapAggregate(rows, 250, { users: new Set(), libraries: new Set(), tiers: T() });
assert.strictEqual(a.projected, 400);
assert.strictEqual(a.over, true);
assert.strictEqual(a.dropCount, 2);
assert.deepStrictEqual(a.dropByTier, { 'next-up': 1, 'recently-added': 1 });

// Filter to user 7 only -> two rows, projected 200.
a = wapAggregate(rows, 1000, { users: new Set(['7']), libraries: new Set(), tiers: T() });
assert.strictEqual(a.projected, 200);
assert.strictEqual(a.count, 2);

// Filter to library L1 only -> two resume rows.
a = wapAggregate(rows, 1000, { users: new Set(), libraries: new Set(['L1']), tiers: T() });
assert.strictEqual(a.count, 2);
assert.strictEqual(a.projected, 200);

// Disable resume tier -> only next-up + recently-added remain.
const t = T(); t.resume.enabled = false;
a = wapAggregate(rows, 1000, { users: new Set(), libraries: new Set(), tiers: t });
assert.strictEqual(a.count, 2);

// Per-(user,tier) max cap: user 3 resume max 1 -> keep only the top-ranked (r0).
const t2 = T(); t2.resume.max = 1;
a = wapAggregate(rows, 1000, { users: new Set(), libraries: new Set(), tiers: t2 });
assert.strictEqual(a.count, 3); // 1 resume (capped) + next-up + recently-added
assert.strictEqual(a.projected, 300);

// Binge look-ahead rows count only while that tier is ticked (it is off by
// default, but the estimate projects it so ticking it moves the meter).
const bingeRows = rows.concat([{ u: '7', t: 'binge-ahead', l: 'L2', b: 50, r: 4 }]);
const t3 = T(); t3['binge-ahead'] = { enabled: false, max: 0 };
a = wapAggregate(bingeRows, 1000, { users: new Set(), libraries: new Set(), tiers: t3 });
assert.strictEqual(a.projected, 400);
t3['binge-ahead'].enabled = true;
a = wapAggregate(bingeRows, 1000, { users: new Set(), libraries: new Set(), tiers: t3 });
assert.strictEqual(a.projected, 450);

console.log('PASS: wapAggregate');
