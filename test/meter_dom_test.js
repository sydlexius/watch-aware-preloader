// DOM-layer tests for the settings-page cache-budget meter (#85).
//
// test/meter_test.js covers the PURE wapAggregate function. Everything between
// that function and the rendered page - reading the live form selection,
// painting the bar/state/copy, the stale-estimate note, and the malformed-input
// guard - had no automated coverage, and that is exactly the layer where the
// review findings on #83 lived. This exercises it headlessly with jsdom: no
// browser, no network, no Unraid host.
//
// The scripts are plain script-scope files (no module system), so each test
// builds a document, then evaluates meter.js inside that window - the same way
// the settings page loads it via <script src>.

const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const { JSDOM } = require('jsdom');

const METER_JS = fs.readFileSync(
    path.join(__dirname, '../src/usr/local/emhttp/plugins/watch-aware-preloader/js/meter.js'),
    'utf8',
);

const GIB = 1073741824;

// A row as the -estimate island emits it. The wire shape is deliberately terse
// (the island is inlined into the page): t=tier, u=user, l=library, b=bytes,
// r=rank. Rank drives both the per-tier cap and the budget cutline, so it is
// explicit here rather than defaulted.
let nextRank = 0;
function row(tier, bytes, user, rank) {
    return {
        t: tier,
        b: bytes,
        u: user || 'u1',
        l: 'lib1',
        r: rank === undefined ? ++nextRank : rank,
    };
}

// Build a page carrying the meter markup, the estimate data island, and the
// control inputs the meter listens to. `controls` overrides the form state.
function build(estimate, controls) {
    const c = Object.assign(
        { users: ['u1'], libraries: ['lib1'], tiers: { RESUME: true, NEXTUP: true, RECENT: true }, ram: null, target: null },
        controls || {},
    );
    const checkbox = (name, value, checked) =>
        `<input type="checkbox" name="${name}" value="${value}"${checked ? ' checked' : ''}>`;

    let html = '<!doctype html><html><body>';
    html += '<div id="wap-meter"><div class="wap-bar-fill"></div>';
    html += '<div class="wap-meter-text"></div><div class="wap-drop"></div><div class="wap-stale"></div></div>';
    for (const u of c.users) { html += checkbox('USERS[]', u, true); }
    for (const l of c.libraries) { html += checkbox('LIBRARIES[]', l, true); }
    for (const [k, on] of Object.entries(c.tiers)) {
        html += checkbox(`TIER_${k}_ENABLED`, '1', on);
        html += `<input name="TIER_${k}_MAX" value="0">`;
    }
    if (c.ram !== null) { html += `<input name="RAM_PERCENT" value="${c.ram}">`; }
    if (c.target !== null) { html += `<input name="TARGET_SECONDS" value="${c.target}">`; }
    if (c.scalarUsers !== undefined) { html += `<input name="USERS" value="${c.scalarUsers}">`; }
    // The island holds the estimate as JSON; a string is injected verbatim so a
    // malformed payload can be exercised.
    const payload = typeof estimate === 'string' ? estimate : JSON.stringify(estimate);
    html += `<script type="application/json" id="wap-estimate">${payload}</script>`;
    html += '</body></html>';

    // meter.js self-initializes on DOMContentLoaded while the document is still
    // 'loading', and otherwise runs immediately. A freshly-constructed JSDOM is
    // still 'loading', and its DOMContentLoaded has already gone by the time we
    // eval - so evaluating right away would register a listener that never
    // fires and the meter would silently never paint. Constructing the DOM
    // eagerly and evaluating once the document has settled reproduces the
    // browser's real ordering.
    const dom = new JSDOM(html, { runScripts: 'outside-only' });
    return { dom: dom, html: html };
}

// Finish the load, then evaluate the script the way a <script src> would.
function load(built) {
    const w = built.dom.window;
    return new Promise((resolve) => {
        const go = () => { w.eval(METER_JS); resolve(w); };
        if (w.document.readyState === 'loading') {
            w.addEventListener('load', go, { once: true });
        } else {
            go();
        }
    });
}

const meterOf = (w) => w.document.getElementById('wap-meter');
const textOf = (w) => meterOf(w).querySelector('.wap-meter-text').textContent;
const dropOf = (w) => meterOf(w).querySelector('.wap-drop');
const staleOf = (w) => meterOf(w).querySelector('.wap-stale');

test('under budget paints the ok state with a proportional bar', async () => {
    const w = await load(build({ budget_bytes: 10 * GIB, rows: [row('resume', 2 * GIB)] }));
    assert.strictEqual(meterOf(w).getAttribute('data-state'), 'ok');
    // jsdom normalizes the CSS value, so compare on the normalized form.
    assert.strictEqual(meterOf(w).querySelector('.wap-bar-fill').style.width, '20%');
    assert.match(textOf(w), /2\.0 GiB projected of 10\.0 GiB budget/);
    assert.strictEqual(dropOf(w).style.display, 'none', 'nothing drops under budget');
});

test('above 90 percent paints caution without claiming anything drops', async () => {
    const w = await load(build({ budget_bytes: 10 * GIB, rows: [row('resume', 9.5 * GIB)] }));
    assert.strictEqual(meterOf(w).getAttribute('data-state'), 'caution');
    assert.strictEqual(dropOf(w).style.display, 'none');
});

test('over budget paints over, caps the bar at 100 percent, and reports the overage', async () => {
    const w = await load(build({ budget_bytes: 10 * GIB, rows: [row('resume', 8 * GIB), row('next-up', 6 * GIB)] }));
    assert.strictEqual(meterOf(w).getAttribute('data-state'), 'over');
    assert.strictEqual(meterOf(w).querySelector('.wap-bar-fill').style.width, '100%',
        'the bar clamps rather than overflowing its track');
    assert.match(textOf(w), /over by 4\.0 GiB/);
});

test('the drop note names the tier and pluralizes on count', async () => {
    // One item past the cutline: singular.
    let w = await load(build({ budget_bytes: 10 * GIB, rows: [row('resume', 8 * GIB), row('next-up', 6 * GIB)] }));
    assert.strictEqual(dropOf(w).style.display, '');
    assert.match(dropOf(w).textContent, /^1 item past the cutline won't warm - Next-up 1$/);

    // Two past the cutline: plural.
    w = await load(build({
        budget_bytes: 10 * GIB,
        rows: [row('resume', 8 * GIB), row('next-up', 6 * GIB), row('recently-added', 6 * GIB)],
    }));
    assert.match(dropOf(w).textContent, /^2 items past the cutline won't warm - /);
});

test('an unreadable RAM budget shows projected bytes only, never a false overage', async () => {
    const w = await load(build({ budget_bytes: 0, rows: [row('resume', 3 * GIB)] }));
    assert.strictEqual(meterOf(w).getAttribute('data-state'), 'ok',
        'no budget is not the same as being over budget');
    assert.match(textOf(w), /3\.0 GiB projected \(budget unavailable\)/);
    assert.doesNotMatch(textOf(w), /over by/);
    assert.strictEqual(dropOf(w).style.display, 'none');
});

test('unticking a user repaints from the live form, not the saved estimate', async () => {
    const w = await load(build(
        { budget_bytes: 10 * GIB, rows: [row('resume', 2 * GIB, 'u1'), row('resume', 3 * GIB, 'u2')] },
        { users: ['u1', 'u2'] },
    ));
    assert.match(textOf(w), /5\.0 GiB projected/);

    const u2 = w.document.querySelector('input[name="USERS[]"][value="u2"]');
    u2.checked = false;
    u2.dispatchEvent(new w.Event('change'));
    assert.match(textOf(w), /2\.0 GiB projected/, 'the meter follows the unticked selection');
});

test('with no USERS[] checkboxes the selection falls back to the scalar CSV', async () => {
    // The connect-gate branch renders no per-user checkboxes and re-posts the
    // saved selection as a hidden scalar; the meter must still scope to it.
    const w = await load(build(
        { budget_bytes: 10 * GIB, rows: [row('resume', 2 * GIB, 'u1'), row('resume', 3 * GIB, 'u2')] },
        { users: [], scalarUsers: 'u1' },
    ));
    assert.match(textOf(w), /2\.0 GiB projected/, 'only the scalar-listed user counts');
});

test('changing RAM percent after an estimate shows the stale note', async () => {
    // The staleness inputs live under `meta`, alongside the estimate the engine
    // computed them with - not at the top level.
    const est = {
        budget_bytes: 10 * GIB,
        rows: [row('resume', 2 * GIB)],
        meta: { ram_percent: 50, target_seconds: 20 },
    };
    const w = await load(build(est, { ram: 50, target: 20 }));
    assert.strictEqual(staleOf(w).style.display, 'none', 'matching inputs are not stale');

    const ram = w.document.querySelector('input[name="RAM_PERCENT"]');
    ram.value = '75';
    ram.dispatchEvent(new w.Event('input'));
    assert.strictEqual(staleOf(w).style.display, '');
    assert.match(staleOf(w).textContent, /click Estimate budget to refresh/);
});

test('a malformed rows payload is ignored rather than throwing', async () => {
    // `rows` as an object, not an array - the guard #85 calls out. wapInitMeter
    // bails, so the meter keeps its empty initial text instead of throwing and
    // taking the rest of the settings page's scripts down with it.
    //
    // Note there are TWO Array.isArray guards on this path: this one in
    // wapInitMeter, and a second in wapPaint that re-checks before aggregating.
    // Either alone prevents the throw, so removing just one still passes - the
    // redundancy is real, not a gap in this test.
    const w = await load(build({ budget_bytes: 10 * GIB, rows: {} }));
    assert.strictEqual(textOf(w), '', 'init bails on a non-array rows payload');
});

test('unparseable island JSON is ignored rather than throwing', async () => {
    const w = await load(build('{ not json'));
    assert.strictEqual(textOf(w), '');
});
