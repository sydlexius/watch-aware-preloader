// Package app assembles the media-server client, scorer, and preloader into a
// runnable pipeline.
package app

import (
	"context"
	"log/slog"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/libscope"
	"github.com/doxazo-net/watch-aware-preloader/internal/scorer"
)

// Provider is the media-server surface the pipeline needs.
//
// It names only core types, so any adapter satisfying it works: the pipeline
// has no compile-time dependency on a particular server. That is what makes a
// second adapter possible (#3) - an interface naming a vendor type can only
// ever have one implementation.
//
// The method set is deliberately the SUBSET the pipeline uses, not everything a
// server offers, so an adapter implements what preloading needs rather than a
// whole API.
type Provider interface {
	Users(ctx context.Context) ([]core.User, error)
	Libraries(ctx context.Context) ([]core.Library, error)
	Resume(ctx context.Context, userID string) ([]core.MediaItem, error)
	NextUp(ctx context.Context, userID string) ([]core.MediaItem, error)
	RecentlyAdded(ctx context.Context, userID string) ([]core.MediaItem, error)
	NowPlayingIDs(ctx context.Context) (map[string]bool, error)
}

// capItems returns at most limit items (in the server's order, which is
// relevance-ordered per tier). limit <= 0 means no cap.
func capItems(items []core.MediaItem, limit int) []core.MediaItem {
	if limit > 0 && len(items) > limit {
		return items[:limit]
	}
	return items
}

// CollectCandidates fetches each enrolled user's enabled signal tiers plus the
// global now-playing set. Enrollment and per-user tier enablement both come from
// opts: a user absent from opts.TierRank is not enrolled, and a tier absent from
// their map is skipped entirely (no fetch). Per-tier MaxItems dials still cap
// each user's contribution (0 = no cap).
//
// users is supplied by the caller rather than fetched here because rank
// resolution already needs it, and because its ORDER is load-bearing in a way
// opts cannot express: equal-rank users (the all-users default) fall back to the
// provider's order via the scorer's stable sort.
//
// When enabledLibraries is non-empty, candidates are filtered to items under one
// of those libraries; toHost normalizes item paths and library locations to a
// common host-path namespace. An empty enabledLibraries leaves candidates
// unfiltered.
func CollectCandidates(ctx context.Context, p Provider, users []core.User, enabledLibraries []string, tiers config.TiersConfig, opts scorer.RankOpts, toHost libscope.ToHost, log *slog.Logger) ([]scorer.Candidate, map[string]bool, error) {
	playing, err := p.NowPlayingIDs(ctx)
	if err != nil {
		return nil, nil, err
	}

	fetch := map[core.Tier]func(context.Context, string) ([]core.MediaItem, error){
		core.TierResume:        p.Resume,
		core.TierNextUp:        p.NextUp,
		core.TierRecentlyAdded: p.RecentlyAdded,
	}

	var cands []scorer.Candidate
	for _, u := range users {
		order, enrolled := opts.TierRank[u.ID]
		if !enrolled {
			continue
		}
		// Iterate the default order, not the user's map, so fetches are
		// deterministic. The map's positions drive priority; iteration order does
		// not.
		for _, tier := range config.DefaultTierOrder() {
			if _, on := order[tier]; !on {
				continue
			}
			get, ok := fetch[tier]
			if !ok {
				continue // reserved tier with no provider endpoint yet
			}
			items, err := get(ctx, u.ID)
			if err != nil {
				return nil, nil, err
			}
			for _, it := range capItems(items, tiers.Dial(tier).MaxItems) {
				// Stamp UserID here rather than trusting the adapter to: ranking
				// keys every per-user decision off it, and RankOpts.slot answers an
				// unstamped item with a silent skip, so a forgetful adapter would
				// warm nothing and still report a clean sweep.
				it.UserID = u.ID
				cands = append(cands, scorer.Candidate{Item: it, Tier: tier})
			}
		}
	}

	if len(enabledLibraries) > 0 {
		libs, err := p.Libraries(ctx)
		if err != nil {
			return nil, nil, err
		}
		cands = filterByLibraries(cands, libs, enabledLibraries, toHost, log)
	}
	return cands, playing, nil
}

// filterByLibraries keeps only candidates whose item falls under one of the
// enabled libraries, using toHost to normalize item paths and library locations
// to a common host-path namespace. When the requested scope cannot be applied
// (a bad library ID, or paths that do not map), it logs a warning and warms all
// libraries rather than failing silently.
func filterByLibraries(cands []scorer.Candidate, libs []core.Library, enabledLibraries []string, toHost libscope.ToHost, log *slog.Logger) []scorer.Candidate {
	scopeLibs := make([]libscope.Library, len(libs))
	for i, l := range libs {
		scopeLibs[i] = libscope.Library{ID: l.ID, Locations: l.Locations}
	}
	scope, fellBack := libscope.New(scopeLibs, enabledLibraries, toHost)
	if fellBack && log != nil {
		log.Warn("library scope requested but no selected library resolved to a host path; warming all libraries",
			"enabled_libraries", enabledLibraries)
	}
	filtered := make([]scorer.Candidate, 0, len(cands))
	for _, c := range cands {
		if scope.Allowed(c.Item.ServerPath) {
			filtered = append(filtered, c)
		}
	}
	return filtered
}
