// DOM-layer tests for the path-map row editor (#27).
//
// The risk this file exists to pin is SILENT DATA LOSS. /update.php overlays
// posted keys onto the existing .cfg, so a posted-EMPTY PATH_MAPS overwrites a
// working mapping while an unposted key would have survived. Every assertion
// about the hidden field is really an assertion that a save cannot quietly wipe
// the operator's rules.

const assert = require('node:assert');
const fs = require('node:fs');
const path = require('node:path');
const test = require('node:test');
const { JSDOM } = require('jsdom');

const PATHMAPS_JS = fs.readFileSync(
    path.join(__dirname, '../src/usr/local/emhttp/plugins/watch-aware-preloader/js/pathmaps.js'),
    'utf8',
);

// Mirrors what the .page renders: a hidden field carrying the saved value, one
// row per saved rule (or a single blank row), and the add button.
function build(saved) {
    const rows = String(saved || '')
        .split(';')
        .map((c) => {
            const i = c.indexOf('=>');
            if (i < 0) { return null; }
            const from = c.slice(0, i).trim();
            const to = c.slice(i + 2).trim();
            return (from === '' || to === '') ? null : { from, to };
        })
        .filter(Boolean);
    if (rows.length === 0) { rows.push({ from: '', to: '' }); }

    let html = '<!doctype html><html><body><form>';
    html += `<input type="hidden" name="PATH_MAPS" value="${saved || ''}">`;
    html += '<div data-wap-pathmap-list="">';
    for (const r of rows) {
        html += '<div data-wap-pathmap-row="">';
        html += `<input type="text" data-wap-pathmap="from" value="${r.from}">`;
        html += `<input type="text" data-wap-pathmap="to" value="${r.to}">`;
        html += '<button type="button" data-wap-pathmap-remove="">-</button>';
        html += '</div>';
    }
    html += '</div><button type="button" data-wap-pathmap-add="">Add rule</button>';
    html += '</form></body></html>';
    return new JSDOM(html, { runScripts: 'outside-only' });
}

// meter.js/pathmaps.js self-initialize on DOMContentLoaded while the document is
// still 'loading'; a fresh JSDOM has already passed that point by the time we
// eval, so wait for load before evaluating (same ordering as the real page).
function load(dom) {
    const w = dom.window;
    return new Promise((resolve) => {
        const go = () => { w.eval(PATHMAPS_JS); resolve(w); };
        if (w.document.readyState === 'loading') {
            w.addEventListener('load', go, { once: true });
        } else {
            go();
        }
    });
}

const hiddenOf = (w) => w.document.querySelector('input[name="PATH_MAPS"]');
const rowsOf = (w) => w.document.querySelectorAll('[data-wap-pathmap-row]');
const fromOf = (w, i) => w.document.querySelectorAll('[data-wap-pathmap="from"]')[i];
const toOf = (w, i) => w.document.querySelectorAll('[data-wap-pathmap="to"]')[i];
const type = (w, el, value) => { el.value = value; el.dispatchEvent(new w.Event('input', { bubbles: true })); };
const click = (w, el) => el.dispatchEvent(new w.Event('click', { bubbles: true }));

test('a saved value renders one row per rule', async () => {
    const w = await load(build('/share=>/mnt/user; /media=>/mnt/user/media'));
    assert.strictEqual(rowsOf(w).length, 2);
    assert.strictEqual(fromOf(w, 0).value, '/share');
    assert.strictEqual(toOf(w, 1).value, '/mnt/user/media');
});

test('merely loading the page does not touch the saved value', async () => {
    // The silent-wipe guard: init must not rewrite the hidden field. If it did,
    // opening Settings and saving anything else would rewrite PATH_MAPS from
    // whatever the rows happened to parse to.
    const saved = '/share=>/mnt/user';
    const w = await load(build(saved));
    assert.strictEqual(hiddenOf(w).value, saved, 'the saved value survives an untouched page');
});

test('editing a row rewrites the hidden field', async () => {
    const w = await load(build('/share=>/mnt/user'));
    type(w, toOf(w, 0), '/mnt/disk1');
    assert.strictEqual(hiddenOf(w).value, '/share=>/mnt/disk1');
});

test('adding a rule appends a blank row and keeps existing rules', async () => {
    const w = await load(build('/share=>/mnt/user'));
    click(w, w.document.querySelector('[data-wap-pathmap-add]'));
    assert.strictEqual(rowsOf(w).length, 2);
    assert.strictEqual(fromOf(w, 1).value, '', 'the new row is empty');
    assert.strictEqual(hiddenOf(w).value, '/share=>/mnt/user',
        'a blank row adds nothing to the wire value');

    type(w, fromOf(w, 1), '/media');
    type(w, toOf(w, 1), '/mnt/user/media');
    assert.strictEqual(hiddenOf(w).value, '/share=>/mnt/user; /media=>/mnt/user/media');
});

test('removing a rule drops it from the hidden field', async () => {
    const w = await load(build('/share=>/mnt/user; /media=>/mnt/user/media'));
    click(w, w.document.querySelectorAll('[data-wap-pathmap-remove]')[0]);
    assert.strictEqual(rowsOf(w).length, 1);
    assert.strictEqual(hiddenOf(w).value, '/media=>/mnt/user/media');
});

test('removing the last rule clears it but keeps an editable row', async () => {
    // Removing the final row outright would leave no template to clone and no
    // way back to a non-empty editor.
    const w = await load(build('/share=>/mnt/user'));
    click(w, w.document.querySelector('[data-wap-pathmap-remove]'));
    assert.strictEqual(rowsOf(w).length, 1, 'one row always remains');
    assert.strictEqual(fromOf(w, 0).value, '', 'and it is blank');
    assert.strictEqual(hiddenOf(w).value, '', 'clearing the only rule IS an intentional empty save');
});

test('a half-filled row does not reach the wire value', async () => {
    const w = await load(build('/share=>/mnt/user'));
    click(w, w.document.querySelector('[data-wap-pathmap-add]'));
    type(w, fromOf(w, 1), '/media'); // target still blank - mid-edit
    assert.strictEqual(hiddenOf(w).value, '/share=>/mnt/user',
        'an in-progress row is not a rule and must not emit from=>');
});

test('a separator typed into a row cannot inject a second rule', async () => {
    const w = await load(build(''));
    type(w, fromOf(w, 0), '/a;/b=>/c');
    type(w, toOf(w, 0), '/mnt/user');
    assert.strictEqual(hiddenOf(w).value, '/a/b/c=>/mnt/user',
        'the separators are stripped rather than splitting the rule');
    assert.strictEqual(hiddenOf(w).value.split(';').length, 1, 'still exactly one rule');
});

test('an empty saved value renders one blank row and stays empty', async () => {
    const w = await load(build(''));
    assert.strictEqual(rowsOf(w).length, 1);
    assert.strictEqual(hiddenOf(w).value, '');
});
