'use strict';
// Every npm "overrides" entry in package.json must still be needed.
//
// An override forces a transitive dev dependency past what its parent declares
// (minimatch ^11.0.3 under ESLint's ^10, for GHSA-g3hv-75wg-m249). Once every
// parent's declared range is already a SUBSET of the override's range, npm would
// resolve an acceptable version without it, so the override is redundant and
// silently masks future resolution. Dependabot and npm audit stay quiet in that
// state, so this test is what flags it: when it fails, delete the override from
// package.json and run `npm install` to refresh the lockfile.
//
// The comparison is semver-correct (semver.subset), and it errs toward KEEPING
// the override: a parent range that could still resolve anything outside the
// override's range (10.x, >=10 <11, an exact 11.0.0 below the fix, a disjoint
// ^12) means the override stays. A range semver cannot parse (an npm: alias, a
// URL) is reported as a problem rather than guessed at.
const assert = require('assert');
const fs = require('fs');
const path = require('path');

let semver;
try {
  semver = require('semver');
} catch (err) {
  if (err.code !== 'MODULE_NOT_FOUND') throw err;
  // Same contract as the jsdom DOM suites (#108): a fresh clone without
  // `npm ci` warns locally; CI, where `npm ci` always runs, fails loudly.
  if (process.env.CI) {
    throw new Error('semver is missing after npm ci - the overrides test cannot run', { cause: err });
  }
  console.log("SKIP (warning): semver absent - run 'npm ci' to run the npm overrides test");
  process.exit(0);
}

const DEP_FIELDS = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies'];

// Returns a list of problems; empty means every override is still earning its keep.
function staleOverrides(pkg, lock) {
  const problems = [];
  for (const [name, spec] of Object.entries(pkg.overrides || {})) {
    if (typeof spec !== 'string') {
      problems.push(`${name}: nested override objects are not checked by this test; extend it`);
      continue;
    }
    if (!semver.validRange(spec)) {
      problems.push(`${name}: override "${spec}" is not a semver range this test can check`);
      continue;
    }
    const declared = [];
    for (const [key, meta] of Object.entries(lock.packages || {})) {
      if (key === '') continue; // the root project is not a parent
      for (const field of DEP_FIELDS) {
        const range = meta[field] && meta[field][name];
        if (range) declared.push({ parent: key.replace(/^.*node_modules\//, ''), range });
      }
    }
    const unparsable = declared.filter((d) => !semver.validRange(d.range));
    if (unparsable.length) {
      const who = unparsable.map((d) => `${d.parent} (${d.range})`).join(', ');
      problems.push(`${name}: parent range is not semver, cannot decide [${who}]`);
    } else if (declared.length === 0) {
      problems.push(`${name}: no package in the lockfile depends on it; remove the override`);
    } else if (declared.every((d) => semver.subset(d.range, spec))) {
      const who = declared.map((d) => `${d.parent} (${d.range})`).join(', ');
      problems.push(`${name}: every parent range is already within "${spec}" [${who}]; remove the override`);
    }
  }
  return problems;
}

// The checker must be able to fail AND must not fire early: synthetic cases.
const lockWith = (ranges) => ({
  packages: Object.fromEntries([['', {}], ...ranges.map((r, i) => [`node_modules/p${i}`, { dependencies: { dep: r } }])]),
});
const pkgWith = (spec) => ({ overrides: { dep: spec } });
const OVR = '^11.0.3';
const needed = (ranges, why) => assert.deepStrictEqual(staleOverrides(pkgWith(OVR), lockWith(ranges)), [], why);
const stale = (ranges, why) => assert.strictEqual(staleOverrides(pkgWith(OVR), lockWith(ranges)).length, 1, why);

needed(['^10.2.6'], 'a ^10 parent still needs it');
needed(['^10.2.12'], 'a patch number >= 11 is not a major');
needed(['10.x'], '10.x cannot reach 11');
needed(['>=10 <11'], 'a bounded range below 11 still needs it');
needed(['11.0.0'], 'an exact pin below the override floor still needs it');
needed(['>=9'], 'an open-ended range can still resolve 9.x/10.x');
needed(['10 || 11'], 'an alternative that admits 10.x still needs it');
needed(['^12.0.0'], 'a disjoint range is not a reason to delete it');
needed(['^11.1.0', '^10.2.4'], 'one ^10 parent is enough');

stale(['^11.1.0'], 'a parent wholly inside ^11.0.3 -> stale');
stale(['^11.0.3', '11.2.0'], 'every parent inside the override range -> stale');
stale([], 'no parent at all -> stale');

assert.strictEqual(staleOverrides(pkgWith(OVR), lockWith(['npm:other@1'])).length, 1, 'non-semver parent is flagged, not guessed');
assert.strictEqual(staleOverrides(pkgWith('$dep'), lockWith(['^10'])).length, 1, 'non-semver override is flagged');
assert.strictEqual(staleOverrides({ overrides: { dep: { sub: '1' } } }, lockWith(['^1'])).length, 1, 'nested form is flagged, not skipped');

// The real tree.
const root = path.join(__dirname, '..');
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
const lock = JSON.parse(fs.readFileSync(path.join(root, 'package-lock.json'), 'utf8'));
const problems = staleOverrides(pkg, lock);
assert.deepStrictEqual(problems, [], `stale npm overrides:\n  ${problems.join('\n  ')}`);
console.log(`npm overrides: ${Object.keys(pkg.overrides || {}).length} checked, all still needed`);
