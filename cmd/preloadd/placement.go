package main

import (
	"log/slog"

	"github.com/doxazo-net/watch-aware-preloader/internal/diskresolve"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
)

// poolResidentOpts builds the preloader option that lets a sweep size
// pool-resident files without the spin-up allowance (#113), or returns nothing
// when placement cannot be resolved.
//
// Members are discovered ONCE per process. Under the primary run model - a
// cron-invoked one-shot - that is exactly once per sweep. Under --daemon the
// list is frozen for the process lifetime, which is fine because changing
// Unraid array membership requires an array stop/start.
//
// There is deliberately no "am I on Unraid?" check. A host without /mnt fails
// discovery; a host with /mnt but no union share fails every resolve. Both
// yield no option or a false predicate, and sizing stays conservative. A
// positive host check would be a thing that could be WRONG, and a wrong
// positive here sizes down a file that really is on a spinning disk.
func poolResidentOpts(fs diskresolve.FS, mntRoot string, log *slog.Logger) []preloader.Option {
	members, err := diskresolve.Discover(fs, mntRoot)
	if err != nil {
		log.Info("placement resolution unavailable; sizing every item for the array",
			"root", mntRoot, "err", err)
		return nil
	}
	if len(members) == 0 {
		log.Info("placement resolution unavailable; sizing every item for the array",
			"root", mntRoot, "reason", "no array members found")
		return nil
	}

	resolver := diskresolve.New(fs, members)
	pools := resolver.PoolMembers()
	if len(pools) == 0 {
		// Members exist but none classify as a pool, so IsPool can never return
		// true - the predicate would be wired up but permanently inert. Say so
		// explicitly rather than logging a misleading "enabled".
		log.Info("placement resolution enabled but inert; no pool members found, sizing every item for the array",
			"root", mntRoot, "members", len(members))
		return nil
	}
	log.Info("placement resolution enabled", "root", mntRoot, "members", len(members), "pool_members", pools)
	return []preloader.Option{preloader.WithPoolResident(resolver.IsPool)}
}
