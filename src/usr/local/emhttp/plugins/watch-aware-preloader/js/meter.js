'use strict';
// Watch-Aware Preloader budget meter. The projection math lives in the Go engine
// (-estimate); this only re-aggregates the precomputed, anonymized rows the page
// embeds, so the meter reacts instantly to control changes. No item identity is
// ever present here - rows are {u,t,l,b,r} only.

// wapAggregate filters the rows by the current selection, applies each
// (user,tier) max-items cap in global-rank order, sums the projected bytes, and
// finds the budget cutline (the engine warms in rank order and stops when the
// byte budget is exhausted, so everything past the cutline will not warm).
// sel = { users:Set<string>, libraries:Set<string>,
//         tiers:{ 'resume':{enabled,max}, 'next-up':{...}, 'recently-added':{...} } }
// An empty users/libraries Set means "all" (matches the engine's empty-list rule).
function wapAggregate(rows, budgetBytes, sel) {
  const allUsers = sel.users.size === 0;
  const allLibs = sel.libraries.size === 0;
  const kept = rows.filter(function (r) {
    const tier = sel.tiers[r.t];
    return (allUsers || sel.users.has(r.u)) &&
           (allLibs || sel.libraries.has(r.l)) &&
           tier && tier.enabled;
  });

  // Per-(user,tier) cap, applied in rank order (keep the highest-priority items).
  const buckets = Object.create(null);
  for (let i = 0; i < kept.length; i++) {
    const r = kept[i];
    const key = r.u + '|' + r.t;
    (buckets[key] || (buckets[key] = [])).push(r);
  }
  const capped = [];
  for (const key in buckets) {
    const g = buckets[key].sort(function (a, b) { return a.r - b.r; });
    const tier = key.split('|')[1];
    const max = (sel.tiers[tier].max | 0);
    const take = max > 0 ? g.slice(0, max) : g;
    for (let j = 0; j < take.length; j++) { capped.push(take[j]); }
  }

  // Global rank order for the budget cutline.
  capped.sort(function (a, b) { return a.r - b.r; });

  let cum = 0, projected = 0, cutIndex = -1;
  for (let i = 0; i < capped.length; i++) {
    projected += capped[i].b;
    cum += capped[i].b;
    if (cutIndex < 0 && cum > budgetBytes) {
      cutIndex = i; // this item is the first that overruns the budget
    }
  }
  const dropByTier = {};
  let dropCount = 0;
  if (cutIndex >= 0) {
    for (let i = cutIndex; i < capped.length; i++) {
      dropByTier[capped[i].t] = (dropByTier[capped[i].t] || 0) + 1;
      dropCount++;
    }
  }
  return {
    projected: projected,
    budget: budgetBytes,
    over: projected > budgetBytes,
    count: capped.length,
    dropCount: dropCount,
    dropByTier: dropByTier,
  };
}

if (typeof module !== 'undefined' && module.exports) {
  module.exports = { wapAggregate: wapAggregate };
}

// --- DOM layer (browser only) -------------------------------------------------
// Reads the embedded estimate island + live form state, repaints #wap-meter on
// every control change. The control tier keys map to the estimate tier tokens.
const WAP_TIER_KEYS = { RESUME: 'resume', NEXTUP: 'next-up', RECENT: 'recently-added' };

// wapSelectionSet reads the current selection for USERS[]/LIBRARIES[]. When the
// checkbox group is rendered (pickers available), it returns the checked values
// (empty = all). When it is NOT rendered (pickers unavailable, e.g. no successful
// Test connection yet), the page emits a hidden scalar CSV of the SAVED selection
// instead; fall back to that so a saved narrowed selection is not misread as "all".
function wapSelectionSet(checkboxName, scalarName) {
  const set = new Set();
  const boxes = document.querySelectorAll('input[name="' + checkboxName + '"]');
  if (boxes.length > 0) {
    for (let i = 0; i < boxes.length; i++) { if (boxes[i].checked) { set.add(boxes[i].value); } }
    return set;
  }
  const scalar = document.querySelector('input[name="' + scalarName + '"]');
  if (scalar && scalar.value) {
    scalar.value.split(',').forEach(function (v) { v = v.trim(); if (v) { set.add(v); } });
  }
  return set;
}

function wapReadSelection() {
  const tiers = {};
  Object.keys(WAP_TIER_KEYS).forEach(function (k) {
    const en = document.querySelector('input[name="TIER_' + k + '_ENABLED"]');
    const mx = document.querySelector('input[name="TIER_' + k + '_MAX"]');
    tiers[WAP_TIER_KEYS[k]] = {
      enabled: en ? en.checked : true,
      max: mx ? (parseInt(mx.value, 10) || 0) : 0,
    };
  });
  return { users: wapSelectionSet('USERS[]', 'USERS'), libraries: wapSelectionSet('LIBRARIES[]', 'LIBRARIES'), tiers: tiers };
}

// wapEstimateStale reports whether the operator has changed RAM% or target
// seconds since this estimate was computed. Those feed the budget and per-item
// sizing, which only the Go engine can recompute - so a change makes the shown
// projection stale until "Estimate budget" is clicked again.
function wapEstimateStale(est) {
  const meta = est.meta || {};
  const ramEl = document.querySelector('input[name="RAM_PERCENT"]');
  const tgtEl = document.querySelector('input[name="TARGET_SECONDS"]');
  if (ramEl && meta.ram_percent != null && parseInt(ramEl.value, 10) !== meta.ram_percent) { return true; }
  if (tgtEl && meta.target_seconds != null && parseInt(tgtEl.value, 10) !== meta.target_seconds) { return true; }
  return false;
}

function wapFmtGiB(bytes) { return (bytes / 1073741824).toFixed(1) + ' GiB'; }

// wapTierLabel maps a tier token to its display label (matching the settings
// page's tier names). Falls back to the raw token for an unknown tier.
const WAP_TIER_LABELS = { resume: 'Resume', 'next-up': 'Next-up', 'recently-added': 'Recently added' };
function wapTierLabel(t) { return WAP_TIER_LABELS[t] || t; }

function wapPaint(est) {
  const meter = document.getElementById('wap-meter');
  if (!meter) { return; }
  // budget_bytes can be 0 if the engine could not read available RAM. Treat that
  // as "budget unavailable": show projected bytes only, no percentage/over/drops.
  const hasBudget = est.budget_bytes > 0;
  const rows = Array.isArray(est.rows) ? est.rows : [];
  const a = wapAggregate(rows, est.budget_bytes || 0, wapReadSelection());
  const pct = hasBudget ? (a.projected / est.budget_bytes) * 100 : 0;
  const over = hasBudget && a.over;
  const state = !hasBudget ? 'ok' : (pct > 100 ? 'over' : (pct > 90 ? 'caution' : 'ok'));
  const bar = meter.querySelector('.wap-bar-fill');
  bar.style.width = Math.min(pct, 100).toFixed(1) + '%';
  meter.setAttribute('data-state', state);
  let line = wapFmtGiB(a.projected) + ' projected';
  if (hasBudget) {
    line += ' of ' + wapFmtGiB(est.budget_bytes) + ' budget';
    if (over) { line += ' (over by ' + wapFmtGiB(a.projected - est.budget_bytes) + ')'; }
  } else {
    line += ' (budget unavailable)';
  }
  meter.querySelector('.wap-meter-text').textContent = line;

  const drop = meter.querySelector('.wap-drop');
  if (over && a.dropCount > 0) {
    const parts = Object.keys(a.dropByTier).map(function (t) { return wapTierLabel(t) + ' ' + a.dropByTier[t]; });
    const noun = a.dropCount === 1 ? ' item' : ' items';
    drop.textContent = a.dropCount + noun + " past the cutline won't warm - " + parts.join(', ');
    drop.style.display = '';
  } else {
    drop.style.display = 'none';
  }

  const staleEl = meter.querySelector('.wap-stale');
  if (staleEl) {
    if (wapEstimateStale(est)) {
      staleEl.textContent = 'RAM budget or target seconds changed since this estimate - click Estimate budget to refresh.';
      staleEl.style.display = '';
    } else {
      staleEl.style.display = 'none';
    }
  }
}

function wapInitMeter() {
  const island = document.getElementById('wap-estimate');
  const meter = document.getElementById('wap-meter');
  if (!island || !meter) { return; }
  let est;
  try { est = JSON.parse(island.textContent || '{}'); } catch { return; }
  if (!est || !Array.isArray(est.rows)) { return; }
  const repaint = function () { wapPaint(est); };
  const inputs = document.querySelectorAll(
    'input[name="USERS[]"], input[name="LIBRARIES[]"], ' +
    'input[name^="TIER_"][name$="_ENABLED"], input[name^="TIER_"][name$="_MAX"], ' +
    'input[name="RAM_PERCENT"], input[name="TARGET_SECONDS"]'
  );
  for (let i = 0; i < inputs.length; i++) {
    inputs[i].addEventListener('change', repaint);
    inputs[i].addEventListener('input', repaint);
  }
  repaint(); // initial paint from the saved selection
}

if (typeof document !== 'undefined') {
  if (document.readyState === 'loading') {
    document.addEventListener('DOMContentLoaded', wapInitMeter);
  } else {
    wapInitMeter();
  }
}
