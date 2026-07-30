'use strict';
// Watch-Aware Preloader tier-order editor. The DOM list is the editor; a hidden
// CSV input is the wire format - /update.php MERGES the posted fields into the
// flat .cfg, so the hidden input's value IS what gets saved, but NOT posting a
// field leaves its old value in place rather than clearing it (which is why an
// override removal needs its own always-posted flag, see wapOverrideApply). The
// array logic is kept pure (wapReorder/wapOrderCsv) so it is testable headlessly
// with no DOM.

// wapReorder returns a NEW list with the item at index SWAPPED with the one at
// index+delta. Swap and "move by delta" coincide at the only delta the UI uses
// (+/-1); they diverge for |delta| > 1, so the name is deliberately not "move".
// An out-of-range move returns an unchanged copy: the caller disables the button,
// but a keyboard or scripted call must not throw or wrap around.
function wapReorder(list, index, delta) {
  const out = list.slice();
  const to = index + delta;
  if (index < 0 || index >= out.length || to < 0 || to >= out.length) {
    return out;
  }
  const tmp = out[index];
  out[index] = out[to];
  out[to] = tmp;
  return out;
}

// wapOrderCsv renders the wire format from the list's rows, in list order.
// entries = [{ tier:string, enabled:boolean }]. A disabled tier is OMITTED:
// absence from the order IS disablement - the engine has no separate per-tier
// enabled flag in this shape.
function wapOrderCsv(entries) {
  return entries
    .filter(function (e) { return e.enabled; })
    .map(function (e) { return e.tier; })
    .join(',');
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = { wapReorder: wapReorder, wapOrderCsv: wapOrderCsv };
}

// --- DOM layer (browser only) -------------------------------------------------

// Rows are DIRECT CHILDREN of their list host. The :scope > guard is load-bearing:
// a per-user tier list nests inside a row of the user-rank list, so a descendant
// query on the outer host would swallow the inner list's rows.
const WAP_ROW_SEL = ':scope > [data-wap-tier], :scope > [data-wap-user]';

// wapRowKey reads a row's identity. A tier row is keyed by its engine tier token;
// a user row by its user id. Both land in the CSV verbatim - no mapping layer.
function wapRowKey(row) {
  const t = row.getAttribute('data-wap-tier');
  return t !== null ? t : row.getAttribute('data-wap-user');
}

// wapOrderRows reads the list's rows into the pure layer's entry shape, in DOM
// order. A row with no checkbox is enabled (the row is not operator-toggleable).
function wapOrderRows(host) {
  const rows = Array.prototype.slice.call(host.querySelectorAll(WAP_ROW_SEL));
  return rows.map(function (r) {
    const box = r.querySelector('input[type="checkbox"]');
    return { tier: wapRowKey(r), enabled: box ? box.checked : true, row: r };
  });
}

// wapOrderSync writes the current CSV to the backing field.
// A fieldless list (data-wap-order="") has no wire format to write: DOM order IS
// the wire format, because USERS[] checkboxes post in DOM order.
function wapOrderSync(host, field) {
  if (!field) { return; }
  const next = wapOrderCsv(wapOrderRows(host));
  if (field.value === next) { return; }
  field.value = next;
}

// wapRowButton finds a row's own move button. Ownership is checked against the
// list host because a user row CONTAINS that user's tier list, whose rows carry
// move buttons of their own; a bare querySelector could return one of those.
function wapRowButton(host, row, dir) {
  const btns = row.querySelectorAll('button[data-wap-move="' + dir + '"]');
  for (let i = 0; i < btns.length; i++) {
    if (btns[i].closest('[data-wap-order]') === host) { return btns[i]; }
  }
  return null;
}

// wapOrderButtons disables the moves that cannot happen (first item up, last
// item down) so the control's affordance matches wapReorder's no-op.
function wapOrderButtons(host) {
  const rows = Array.prototype.slice.call(host.querySelectorAll(WAP_ROW_SEL));
  rows.forEach(function (r, i) {
    const up = wapRowButton(host, r, 'up');
    const down = wapRowButton(host, r, 'down');
    if (up) { up.disabled = i === 0; }
    if (down) { down.disabled = i === rows.length - 1; }
  });
}

// wapOrderInit wires every [data-wap-order] list under root. The attribute's
// value names the hidden input carrying the CSV. An EMPTY value declares a
// fieldless list: DOM order is itself the wire format (USERS[] posts in DOM
// order), so there is nothing to sync.
function wapOrderInit(root) {
  const lists = root.querySelectorAll('[data-wap-order]');
  Array.prototype.forEach.call(lists, function (host) {
    const name = host.getAttribute('data-wap-order');
    const field = name === '' ? null : root.querySelector('input[name="' + name + '"]');
    if (name !== '' && !field) {
      // Fail loudly: a list with no backing field would silently discard every
      // reorder on save.
      console.error('wap: no hidden field for order list', name);
      return;
    }
    host.addEventListener('click', function (ev) {
      const btn = ev.target.closest('button[data-wap-move]');
      if (!btn || btn.disabled) { return; }
      // A nested list's buttons bubble to this host; that list already handled
      // them. Without this the outer list would re-handle a move it does not own.
      if (btn.closest('[data-wap-order]') !== host) { return; }
      ev.preventDefault();
      const entries = wapOrderRows(host);
      const names = entries.map(function (e) { return e.tier; });
      const i = names.indexOf(wapRowKey(btn.closest('[data-wap-tier], [data-wap-user]')));
      const next = wapReorder(names, i, btn.getAttribute('data-wap-move') === 'up' ? -1 : 1);
      if (next.join(',') === names.join(',')) { return; }
      // Re-append the rows in the new order, then resync. appendChild moves an
      // existing node, so this reorders in place.
      next.forEach(function (t) { host.appendChild(entries[names.indexOf(t)].row); });
      wapOrderButtons(host);
      wapOrderSync(host, field);
      wapRankRenumber(host);
      // The moved row's button may now be disabled at the end of its travel;
      // keep focus on the row so a keyboard operator is not dumped to the top.
      if (btn.disabled) {
        const row = btn.closest('[data-wap-tier], [data-wap-user]');
        const sibling = wapRowButton(host, row, btn.getAttribute('data-wap-move') === 'up' ? 'down' : 'up');
        if (sibling && !sibling.disabled) { sibling.focus(); }
      } else {
        btn.focus();
      }
    });
    // A checkbox toggle changes the CSV without changing DOM order. Only this
    // list's own rows count: a nested list's checkbox bubbles here too.
    host.addEventListener('change', function (ev) {
      if (ev.target.type !== 'checkbox') { return; }
      if (ev.target.closest('[data-wap-order]') !== host) { return; }
      wapOrderSync(host, field);
      wapRankRenumber(host);
    });
    wapOrderButtons(host);
    wapOrderSync(host, field);
    wapRankRenumber(host);
  });
}

// wapRankRenumber restamps the rank badges of a user list. Rank is DOM order
// among ENROLLED users only, so an unenrolled row carries no number: it is not
// in the warm set and has no rank. A list with no badges (the tier lists) is a
// no-op.
function wapRankRenumber(host) {
  let n = 0;
  Array.prototype.forEach.call(host.children, function (row) {
    const badge = row.querySelector(':scope > .wap-user-head > .wap-rank');
    if (!badge) { return; }
    const box = row.querySelector(':scope > .wap-user-head > input[type="checkbox"]');
    const on = !box || box.checked;
    badge.textContent = on ? String(++n) : '';
    row.classList.toggle('wap-off', !on);
  });
}

// wapOverrideApply reflects one override toggle into the DOM.
//
// Disabling the value field stops it overwriting the saved order (a disabled input
// is not submitted), and it must DISABLE rather than blank, because an empty value
// means "warm nothing" - the opposite of inheriting.
//
// But not-posting cannot REMOVE: /update.php merges, so an unposted key keeps its
// old value. The always-enabled flag field is what carries the removal, and it must
// be kept in step here or unticking silently does nothing. flag may be null when
// the page rendered no flag (older markup); the caller logs that case.
function wapOverrideApply(box, field, panel, flag) {
  field.disabled = !box.checked;
  panel.hidden = !box.checked;
  if (flag) { flag.value = box.checked ? '1' : '0'; }
}

// wapOverrideInit wires every [data-wap-override] toggle under root. The
// attribute's value names both the hidden field it gates and its panel.
function wapOverrideInit(root) {
  const boxes = root.querySelectorAll('input[data-wap-override]');
  Array.prototype.forEach.call(boxes, function (box) {
    const name = box.getAttribute('data-wap-override');
    const field = root.querySelector('input[type="hidden"][name="' + name + '"]');
    const panel = root.querySelector('[data-wap-override-panel="' + name + '"]');
    const flag = root.querySelector('input[data-wap-override-flag="' + name + '"]');
    if (!field || !panel) {
      // Fail loudly: an unwired toggle would leave the field's posted state stuck
      // at whatever the page rendered, silently ignoring the operator's choice.
      console.error('wap: no field/panel for override toggle', name);
      return;
    }
    if (!flag) {
      // Also loud: without the flag, unticking cannot REMOVE the saved key
      // (update.php merges), so the operator's choice would appear to save and
      // then come back on the next render.
      console.error('wap: no removal flag for override toggle', name);
    }
    box.addEventListener('change', function () { wapOverrideApply(box, field, panel, flag); });
    wapOverrideApply(box, field, panel, flag);
  });
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', function () { wapOrderInit(document); wapOverrideInit(document); });
  } else {
    wapOrderInit(document);
    wapOverrideInit(document);
  }
}
