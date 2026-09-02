package app

import (
	"log/slog"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/scorer"
)

// SweepOptions bundles the per-sweep configuration knobs that thread through the
// sweep call chain (SweepAndRecord -> RunOnce). Grouping them keeps those
// signatures stable as the settings UI adds more selection dials.
//
// Field consumption by layer: RunOnce reads Users, EnabledLibraries, Tiers,
// Ranks, and Budget; SweepAndRecord additionally reads Mode and StatusPath for
// the status write. The lower-level CollectCandidates keeps explicit params and
// is fed the collection fields by RunOnce.
type SweepOptions struct {
	// Users is the provider's user list, carried so one fetch per sweep feeds
	// both rank resolution and collection. Its ORDER is load-bearing and is not
	// recoverable from Ranks: equal-rank users (the all-users default) fall back
	// to the provider's order via the scorer's stable sort.
	//
	// Users is authoritative for enrollment ITERATION and Ranks for ELIGIBILITY,
	// so Ranks must be resolved FROM this same list: a user in Ranks but absent
	// from Users is never iterated and so silently contributes nothing.
	Users            []core.User
	EnabledLibraries []string           // enabled library IDs, empty = all libraries (library-scope filter)
	Tiers            config.TiersConfig // per-tier max-items dials
	Ranks            scorer.RankOpts    // resolved enrollment, user rank, and per-user tier order
	Budget           int64              // preload byte budget for this sweep
	Mode             string             // status.json run mode: "once", "verify", or "daemon"
	StatusPath       string             // path the status file is written to
}

// SweepOptionsFromConfig builds the sweep options from the resolved config, with
// the per-run budget and mode supplied by the caller (both vary per invocation:
// budget is sampled from available RAM at sweep time, mode from the run path).
// It takes the provider's user list because rank resolution must map configured
// names to IDs.
func SweepOptionsFromConfig(cfg *config.Config, users []core.User, budget int64, mode string, log *slog.Logger) SweepOptions {
	return SweepOptions{
		Users:            users,
		EnabledLibraries: cfg.Libraries.Enabled,
		Tiers:            cfg.Tiers,
		Ranks:            ResolveRanks(cfg, users, log),
		Budget:           budget,
		Mode:             mode,
		StatusPath:       cfg.StatusPath,
	}
}
