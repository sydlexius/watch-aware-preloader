// Pure-function tests for the path-map row editor's wire format (#27).
// The DOM behavior is covered by test/pathmaps_dom_test.js.

const { wapPathMapPairs, wapPathMapParse, wapPathMapClean } =
    require('../src/usr/local/emhttp/plugins/watch-aware-preloader/js/pathmaps.js');

let failures = 0;
function check(cond, msg) {
    if (!cond) { console.error('FAIL: ' + msg); failures++; }
}

// --- wapPathMapPairs: rows -> the .cfg wire value ---
check(
    wapPathMapPairs([{ from: '/share', to: '/mnt/user' }]) === '/share=>/mnt/user',
    'a single rule renders as from=>to',
);
check(
    wapPathMapPairs([
        { from: '/share', to: '/mnt/user' },
        { from: '/media', to: '/mnt/user/media' },
    ]) === '/share=>/mnt/user; /media=>/mnt/user/media',
    'two rules join with a semicolon, matching the format rc.preloadd parses',
);
check(wapPathMapPairs([]) === '', 'no rows renders empty');

// A half-filled row is an in-progress edit, not a rule. Emitting `from=>` would
// render a path_map with an empty target.
check(
    wapPathMapPairs([{ from: '/share', to: '' }]) === '',
    'a row missing its target is dropped',
);
check(
    wapPathMapPairs([{ from: '', to: '/mnt/user' }]) === '',
    'a row missing its source is dropped',
);
check(
    wapPathMapPairs([
        { from: '/share', to: '/mnt/user' },
        { from: '', to: '' },
    ]) === '/share=>/mnt/user',
    'a trailing blank row does not emit a trailing separator',
);

// Whitespace is operator noise, not part of the path.
check(
    wapPathMapPairs([{ from: '  /share  ', to: '  /mnt/user  ' }]) === '/share=>/mnt/user',
    'values are trimmed',
);

// DELIMITER SAFETY: `;` and `=>` are structural in the wire format and it has no
// escape for them. Left in a value they would silently split one rule into two -
// the row UI hides the punctuation, so the operator would not see it happen.
check(
    wapPathMapPairs([{ from: '/a;b', to: '/mnt/c' }]) === '/ab=>/mnt/c',
    'a semicolon inside a value is stripped, not passed through',
);
check(
    wapPathMapPairs([{ from: '/a=>b', to: '/mnt/c' }]) === '/ab=>/mnt/c',
    'an arrow inside a value is stripped, not passed through',
);
check(
    wapPathMapPairs([{ from: '/a', to: '/mnt/b;/c=>/d' }]) === '/a=>/mnt/b/c/d',
    'a crafted target cannot inject a second rule',
);

// Defensive: the DOM layer reads .value, but a scripted call must not throw.
check(wapPathMapPairs([{ from: null, to: undefined }]) === '', 'null and undefined are not rules');

// --- wapPathMapParse: the saved value -> rows ---
const parsed = wapPathMapParse('/share=>/mnt/user; /media=>/mnt/user/media');
check(parsed.length === 2, 'two saved rules parse to two rows');
check(parsed[0].from === '/share' && parsed[0].to === '/mnt/user', 'first row round-trips');
check(parsed[1].from === '/media' && parsed[1].to === '/mnt/user/media', 'second row round-trips');

check(wapPathMapParse('').length === 0, 'an empty saved value parses to no rows');
check(wapPathMapParse('   ').length === 0, 'a whitespace-only value parses to no rows');

// Tolerant by design: this reads a value a human may have typed into the old
// free-text field, so a malformed pair is dropped rather than blanking the
// whole editor.
check(wapPathMapParse('/share/mnt/user').length === 0, 'a pair with no arrow is dropped');
check(
    wapPathMapParse('/share=>/mnt/user; garbage').length === 1,
    'a malformed second pair does not discard the valid first one',
);
check(wapPathMapParse('/share=>').length === 0, 'a pair with an empty target is dropped');

// A path containing '=' (legal) must not be confused with the '=>' separator.
const eq = wapPathMapParse('/a=b=>/mnt/c');
check(eq.length === 1 && eq[0].from === '/a=b' && eq[0].to === '/mnt/c',
    'only the first arrow splits the pair, so = is legal inside a path');

// ROUND TRIP: what the editor renders must parse back to the same rows, or a
// save-then-reload would quietly drift.
const rows = [{ from: '/share', to: '/mnt/user' }, { from: '/media', to: '/mnt/user/media' }];
check(
    JSON.stringify(wapPathMapParse(wapPathMapPairs(rows))) === JSON.stringify(rows),
    'rows survive a render/parse round trip unchanged',
);

check(wapPathMapClean('  /a;b=>c  ') === '/abc', 'clean strips both separators and trims');

if (failures > 0) {
    console.error(failures + ' failure(s)');
    process.exit(1);
}
console.log('PASS: wapPathMapPairs + wapPathMapParse');
