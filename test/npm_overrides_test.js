'use strict';
// Every npm "overrides" entry in package.json must still be needed.
//
// An override forces a transitive dev dependency past what its parent declares
// (minimatch ^11 under ESLint's ^10, for GHSA-g3hv-75wg-m249). Once every parent
// declares a range that already admits the override's major, the override is
// redundant and silently masks future resolution. Dependabot and npm audit stay
// quiet in that state, so this test is what flags it: when it fails, delete the
// override from package.json and run `npm install` to refresh the lockfile.
//
// Range handling is deliberately simple (no semver dependency in this dev-only
// suite): a declared range "admits" the override's major when it is open-ended
// (*, x, >, >=) or names a major at or above it. Anything else (^10.2.6, ~10.1,
// 10.x) is a parent that still needs the override.
const assert = require('assert');
const fs = require('fs');
const path = require('path');

const DEP_FIELDS = ['dependencies', 'devDependencies', 'optionalDependencies', 'peerDependencies'];

function majorOf(spec) {
  const m = /(\d+)/.exec(spec);
  return m ? Number(m[1]) : NaN;
}

function admitsMajor(range, major) {
  if (/[*x>]/.test(range)) return true;
  // Major = the FIRST number of each comparator, never a minor or patch
  // (^10.2.12 is major 10, not 12).
  const majors = range.split(/\|\||\s+/).filter(Boolean).map(majorOf);
  return majors.some((n) => n >= major);
}

// Returns a list of problems; empty means every override is still earning its keep.
function staleOverrides(pkg, lock) {
  const problems = [];
  for (const [name, spec] of Object.entries(pkg.overrides || {})) {
    if (typeof spec !== 'string') {
      problems.push(`${name}: nested override objects are not checked by this test; extend it`);
      continue;
    }
    const major = majorOf(spec);
    if (Number.isNaN(major)) {
      problems.push(`${name}: cannot read a major version from override "${spec}"`);
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
    if (declared.length === 0) {
      problems.push(`${name}: no package in the lockfile depends on it; remove the override`);
    } else if (declared.every((d) => admitsMajor(d.range, major))) {
      const who = declared.map((d) => `${d.parent} (${d.range})`).join(', ');
      problems.push(`${name}: every parent already admits ${major}.x [${who}]; remove the override "${spec}"`);
    }
  }
  return problems;
}

// The checker must be able to fail: synthetic cases for each verdict.
const lockWith = (ranges) => ({
  packages: Object.fromEntries([['', {}], ...ranges.map((r, i) => [`node_modules/p${i}`, { dependencies: { dep: r } }])]),
});
const pkgWith = (spec) => ({ overrides: { dep: spec } });
assert.deepStrictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith(['^10.2.6'])), [], 'a ^10 parent still needs it');
assert.deepStrictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith(['^10.2.12'])), [], 'a patch number >= 11 is not a major');
assert.deepStrictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith(['^11.1.0', '^10.2.4'])), [], 'one ^10 parent is enough');
assert.strictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith(['^11.1.0'])).length, 1, 'all parents on ^11 -> stale');
assert.strictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith(['>=9'])).length, 1, 'open-ended range admits 11 -> stale');
assert.strictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith(['10 || 11'])).length, 1, 'a range naming 11 -> stale');
assert.strictEqual(staleOverrides(pkgWith('^11.0.3'), lockWith([])).length, 1, 'no parent at all -> stale');
assert.strictEqual(staleOverrides({ overrides: { dep: { sub: '1' } } }, lockWith(['^1'])).length, 1, 'nested form is flagged, not skipped');

// The real tree.
const root = path.join(__dirname, '..');
const pkg = JSON.parse(fs.readFileSync(path.join(root, 'package.json'), 'utf8'));
const lock = JSON.parse(fs.readFileSync(path.join(root, 'package-lock.json'), 'utf8'));
const problems = staleOverrides(pkg, lock);
assert.deepStrictEqual(problems, [], `stale npm overrides:\n  ${problems.join('\n  ')}`);
console.log(`npm overrides: ${Object.keys(pkg.overrides || {}).length} checked, all still needed`);
