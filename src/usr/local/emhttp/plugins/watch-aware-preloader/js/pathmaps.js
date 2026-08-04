'use strict';
// Watch-Aware Preloader path-map row editor (#27). The design spec calls for
// repeatable from/to rows; the .cfg wire format stays the single semicolon-
// joined `from=>to` string that rc.preloadd already parses, so this is a UI
// change only - no .cfg, render, or engine contract moves.
//
// The hidden PATH_MAPS input is rendered by PHP carrying the SAVED value, and is
// only rewritten when a row changes. That ordering is deliberate: if this script
// fails to load, the hidden field still posts the saved value unchanged, so the
// operator loses the ability to edit but never loses their mapping. /update.php
// merges posted keys over the existing .cfg, so a posted-empty value WOULD
// overwrite a good one - hence never clearing the field except on a real edit.
//
// The pure serializer is kept DOM-free so it is testable headlessly.

// wapPathMapPairs renders the wire format from row values.
// rows = [{ from:string, to:string }]. A row missing either side is DROPPED:
// a half-filled row is an in-progress edit, not a rule, and emitting `from=>`
// would render a path_map with an empty target.
//
// `;` and `=>` are the structural separators, so any occurrence INSIDE a value
// is stripped rather than escaped - the .cfg format has no escape for them, and
// leaving them in would silently split one rule into two. This mirrors
// wap_cfg_csv_from_list stripping commas from list items for delimiter safety.
function wapPathMapClean(s) {
  return String(s == null ? '' : s)
    .replace(/=>/g, '')
    .replace(/;/g, '')
    .trim();
}

function wapPathMapPairs(rows) {
  return rows
    .map(function (r) {
      return { from: wapPathMapClean(r.from), to: wapPathMapClean(r.to) };
    })
    .filter(function (r) { return r.from !== '' && r.to !== ''; })
    .map(function (r) { return r.from + '=>' + r.to; })
    .join('; ');
}

// wapPathMapParse splits a saved wire value back into rows, for the initial
// render. Tolerant by design: it is reading a value a human may have typed into
// the previous free-text field, so a malformed pair is dropped rather than
// throwing and blanking the whole editor.
function wapPathMapParse(value) {
  return String(value == null ? '' : value)
    .split(';')
    .map(function (chunk) {
      const i = chunk.indexOf('=>');
      if (i < 0) { return null; }
      const from = chunk.slice(0, i).trim();
      const to = chunk.slice(i + 2).trim();
      return (from === '' || to === '') ? null : { from: from, to: to };
    })
    .filter(function (r) { return r !== null; });
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = {
    wapPathMapPairs: wapPathMapPairs,
    wapPathMapParse: wapPathMapParse,
    wapPathMapClean: wapPathMapClean,
  };
}

// --- DOM layer (browser only) -------------------------------------------------

function wapPathMapRows(host) {
  return Array.prototype.slice.call(host.querySelectorAll(':scope > [data-wap-pathmap-row]'));
}

function wapPathMapReadRows(host) {
  return wapPathMapRows(host).map(function (row) {
    const f = row.querySelector('[data-wap-pathmap="from"]');
    const t = row.querySelector('[data-wap-pathmap="to"]');
    return { from: f ? f.value : '', to: t ? t.value : '' };
  });
}

// Rewrite the hidden field from the current rows. Called ONLY from a real edit
// (input, or an add/remove click) so an untouched page cannot blank the value.
function wapPathMapSync(host) {
  const hidden = document.querySelector('input[name="PATH_MAPS"]');
  if (!hidden) { return; }
  hidden.value = wapPathMapPairs(wapPathMapReadRows(host));
}

function wapPathMapNewRow(host) {
  const first = wapPathMapRows(host)[0];
  if (!first) { return null; }
  const row = first.cloneNode(true);
  const inputs = row.querySelectorAll('[data-wap-pathmap]');
  for (let i = 0; i < inputs.length; i++) {
    inputs[i].value = '';
    inputs[i].removeAttribute('id'); // ids are unique per template row
  }
  host.appendChild(row);
  return row;
}

// A single visible row is kept at all times: removing the last one would leave
// no template to clone and no way back to a non-empty editor.
function wapPathMapRemove(host, row) {
  const rows = wapPathMapRows(host);
  if (rows.length <= 1) {
    const inputs = row.querySelectorAll('[data-wap-pathmap]');
    for (let i = 0; i < inputs.length; i++) { inputs[i].value = ''; }
  } else {
    row.parentNode.removeChild(row);
  }
  wapPathMapSync(host);
}

function wapInitPathMaps() {
  const host = document.querySelector('[data-wap-pathmap-list]');
  if (!host) { return; }

  // Bound on the document, not the host: the Add button is a SIBLING of the row
  // list (it belongs to the field, not to any one row), so a host-bound listener
  // would never see its clicks. Each handler re-checks that its target belongs
  // to this field's list before acting.
  document.addEventListener('click', function (ev) {
    const add = ev.target.closest('[data-wap-pathmap-add]');
    if (add) {
      ev.preventDefault();
      const row = wapPathMapNewRow(host);
      if (row) {
        const first = row.querySelector('[data-wap-pathmap="from"]');
        if (first) { first.focus(); }
      }
      wapPathMapSync(host);
      return;
    }
    const del = ev.target.closest('[data-wap-pathmap-remove]');
    if (del) {
      ev.preventDefault();
      const row = del.closest('[data-wap-pathmap-row]');
      if (row) { wapPathMapRemove(host, row); }
    }
  });

  host.addEventListener('input', function (ev) {
    if (ev.target.hasAttribute && ev.target.hasAttribute('data-wap-pathmap')) {
      wapPathMapSync(host);
    }
  });
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wapInitPathMaps);
  } else {
    wapInitPathMaps();
  }
}
