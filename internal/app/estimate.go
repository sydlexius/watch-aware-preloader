package app

import (
	"context"
	"log/slog"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/estimate"
	"github.com/doxazo-net/watch-aware-preloader/internal/libscope"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
	"github.com/doxazo-net/watch-aware-preloader/internal/scorer"
)

// estimateResumeFrontBytes is a fixed, deliberately-high allowance for the
// front-metadata window a resume/seeking target warms. The real front window is
// the parsed container FrontEnd (<= 16 MiB), which needs disk I/O to measure; the
// projection uses this constant upper bound instead so the meter errs high (warns
// before the budget is actually blown) without touching the array.
const estimateResumeFrontBytes = 16 << 20

// projectBytes estimates the bytes a single target would warm, without any disk
// I/O. It is the same two-stage sizing a real sweep uses (preloader.HeadBytes is
// bitrate-based when BitrateBps is known and geometry-based - SizeBytes/Runtime -
// otherwise) plus the flat tail window, plus a fixed front allowance for resume
// targets. It intentionally over-estimates slightly relative to a real sweep.
// The projection deliberately uses the SAME sizing call as a real sweep. When
// max_head_mb truncates a head (#112), the projected bytes are truncated
// identically - so the budget meter keeps agreeing with what the sweep will
// actually warm. Surfacing the truncation in the estimate payload would need an
// estimate.json schema bump in lock step with the PHP reader, so it is left to
// the change that decides what the UI should do about it.
//
// Pool-resident sizing (#113) is deliberately NOT mirrored here. That
// optimisation lives on the Preloader method path, so once it is wired the
// projection over-estimates every pool-resident item. Teaching the projection
// about placement would mean one stat on the union path plus up to one per
// member PER TARGET, breaking this function's whole no-disk-I/O contract. The
// projection already errs high on purpose (see estimateResumeFrontBytes), and
// over-projecting a pool item errs the same safe way: the meter warns before
// the budget is actually blown. Do not "fix" this into resolving placement.
//
// The divergence is larger than the resume-front analogy above suggests: a
// pool item's real sized head is preloader.MinHeadBytes (8 MiB default), while
// this projection can charge up to preloader.MaxHeadBytes (250 MiB default) for
// the same item - up to roughly 30x over the real cost, not the flat 16 MiB
// gap the resume allowance adds. This is not a corner case: a typical Unraid
// setup lands new content on the cache pool before the mover relocates it, so
// the recently-added tier is largely pool-resident and the budget bar it feeds
// is user-facing. The gap is deliberate, but NO issue
// owns surfacing it in the budget meter yet, so a reader who trips over it is
// not duplicating tracked work by opening one.
func projectBytes(cfg preloader.Config, it core.MediaItem, tier core.Tier) int64 {
	b := preloader.HeadBytes(cfg, it) + cfg.TailBytes
	if tier == core.TierResume {
		b += estimateResumeFrontBytes
	}
	return b
}

// estimateCeilingPerUserTier bounds how many items each (user, tier) contributes
// to the projection, so the estimate.json payload and any client-side max-items
// slider both stay bounded. Meta.CeilingTruncated flags when it bit.
const estimateCeilingPerUserTier = 200

// estimateBingeLookupsPerUser bounds the binge-ahead projection separately and
// much lower: each binge row costs one /Shows/{id}/Episodes request, and the
// estimate projects binge for every user even when the tier is off, so the
// 200-item ceiling would mean up to 200 extra requests per user per Estimate.
const estimateBingeLookupsPerUser = 25

// bingeLookupLimiter wraps a Provider so EpisodeAfter answers "nothing to warm"
// once a user has spent estimateBingeLookupsPerUser lookups, without a request.
// It is used only by the estimate, so a real sweep's binge tier is unaffected.
type bingeLookupLimiter struct {
	Provider
	used    map[string]int
	limited bool // a lookup was refused, so the projection is truncated
}

func (b *bingeLookupLimiter) EpisodeAfter(ctx context.Context, userID, seriesID, itemID string) (core.MediaItem, bool, error) {
	if b.used[userID] >= estimateBingeLookupsPerUser {
		b.limited = true
		return core.MediaItem{}, false, nil
	}
	b.used[userID]++
	return b.Provider.EpisodeAfter(ctx, userID, seriesID, itemID)
}

// newLibraryAttributor returns a function mapping an item's server path to the ID
// of the first library it falls under, or "" if none. It reuses libscope per
// library so attribution matches the same host-path prefix logic library scoping
// uses. A library whose Locations do not map (libscope falls back to allow-all)
// is skipped for attribution rather than swallowing every item.
func newLibraryAttributor(libs []core.Library, toHost libscope.ToHost) func(serverPath string) string {
	type libScope struct {
		id    string
		scope *libscope.Scope
	}
	scopes := make([]libScope, 0, len(libs))
	for _, l := range libs {
		s, fellBack := libscope.New([]libscope.Library{{ID: l.ID, Locations: l.Locations}}, []string{l.ID}, toHost)
		if fellBack {
			continue // unmappable library cannot attribute; leave its items blank
		}
		scopes = append(scopes, libScope{id: l.ID, scope: s})
	}
	return func(serverPath string) string {
		for _, ls := range scopes {
			if ls.scope.Allowed(serverPath) {
				return ls.id
			}
		}
		return ""
	}
}

// ProjectWarmSet computes a side-effect-free warm-set projection over the FULL
// candidate universe (all users, all libraries, all three tiers, capped to
// estimateCeilingPerUserTier per user/tier), so the settings page can filter it
// down client-side. It does read-only server queries only - no disk/page-cache
// I/O - and returns anonymized rows in global rank order. generatedAt is an
// RFC3339 UTC timestamp supplied by the caller (kept out of here so the function
// is deterministic and testable).
func ProjectWarmSet(ctx context.Context, p Provider, cfg preloader.Config, budgetBytes int64, ramPercent int, generatedAt string, toHost libscope.ToHost, log *slog.Logger) (estimate.Estimate, error) {
	// Binge-ahead is projected too, although it is off by default: the meter
	// filters rows by the page's CURRENT tier toggles, so a tier missing from
	// the projection would read as zero bytes the moment an operator ticks it,
	// and the meter must err high, never low. The cost is one extra read-only
	// request per next-up item, at most estimateBingeLookupsPerUser per user,
	// paid only when the operator runs Estimate.
	full := config.TiersConfig{
		Resume:        config.TierDial{Enabled: true, MaxItems: estimateCeilingPerUserTier},
		NextUp:        config.TierDial{Enabled: true, MaxItems: estimateCeilingPerUserTier},
		RecentlyAdded: config.TierDial{Enabled: true, MaxItems: estimateCeilingPerUserTier},
		BingeAhead:    config.TierDial{Enabled: true, MaxItems: estimateBingeLookupsPerUser},
		Order:         config.AllTiers(),
	}
	users, err := p.Users(ctx)
	if err != nil {
		return estimate.Estimate{}, err
	}
	// The estimate projects the full universe: every user, every tier, default
	// order, equal rank. Applying the configured cascade here would bake the
	// current selection into estimate.json, and the meter's whole job is to let
	// the operator explore selections the engine is not currently running.
	fullCfg := &config.Config{Tiers: full}
	ranks := ResolveRanks(fullCfg, users, log)

	limiter := &bingeLookupLimiter{Provider: p, used: map[string]int{}}
	cands, playing, err := CollectCandidates(ctx, limiter, users, nil, full, ranks, toHost, log)
	if err != nil {
		return estimate.Estimate{}, err
	}
	targets := scorer.Rank(cands, playing, ranks)

	libs, err := p.Libraries(ctx)
	if err != nil {
		return estimate.Estimate{}, err
	}
	attribute := newLibraryAttributor(libs, toHost)

	rows := make([]estimate.Row, 0, len(targets))
	for r, t := range targets {
		rows = append(rows, estimate.Row{
			U: t.Item.UserID,
			T: t.Tier.String(),
			L: attribute(t.Item.ServerPath),
			B: projectBytes(cfg, t.Item, t.Tier),
			R: r,
		})
	}

	// CeilingTruncated is computed from the pre-Rank candidates (cands), not from
	// targets. CollectCandidates caps each (user,tier) raw fetch to
	// estimateCeilingPerUserTier via capItems, so a bucket that hit the cap has
	// exactly the ceiling count in cands - but scorer.Rank then dedupes across
	// tiers and drops now-playing items, which can shrink a capped bucket below
	// the ceiling in targets and mask the truncation. Counting cands instead
	// means, at worst, an exact-count-200 bucket that wasn't actually truncated
	// upstream gets flagged anyway; that's the safe direction for an advisory
	// flag (over-report, never under-report).
	bucket := map[[2]string]int{} // (user,tier) -> count, for the truncation flag
	for _, c := range cands {
		bucket[[2]string{c.Item.UserID, c.Tier.String()}]++
	}
	truncated := limiter.limited
	for k, n := range bucket {
		ceiling := estimateCeilingPerUserTier
		if k[1] == core.TierBingeAhead.String() {
			ceiling = estimateBingeLookupsPerUser
		}
		if n >= ceiling {
			truncated = true
			break
		}
	}
	return estimate.Estimate{
		SchemaVersion:      estimate.SchemaVersion,
		GeneratedAt:        generatedAt,
		BudgetBytes:        budgetBytes,
		CeilingPerUserTier: estimateCeilingPerUserTier,
		Rows:               rows,
		Meta: estimate.Meta{
			TargetSeconds:    cfg.TargetSeconds,
			RAMPercent:       ramPercent,
			ItemCount:        len(rows),
			CeilingTruncated: truncated,
		},
	}, nil
}
