// Command preloadd is the watch-aware media preloader daemon.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/app"
	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/estimate"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/emby"
	"github.com/doxazo-net/watch-aware-preloader/internal/pagecache"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
	"github.com/doxazo-net/watch-aware-preloader/internal/secrets"
)

var version = "dev"

// selectMode resolves the run mode from the four flag values.
// Priority:
//  1. -verify   -> "verify"   (one sweep + residency report, then exit)
//  2. -estimate -> "estimate" (warm-set projection only, no page-cache I/O, then exit)
//  3. -daemon   -> "daemon"   (resident loop; opt-in for long-running service use)
//  4. default / -once -> "once" (one sweep, then exit; cron re-invokes each interval)
//
// Combining -once and -daemon is an error: they express conflicting lifecycle intent.
// The DEFAULT (no flags) is "once" so that a bare `preloadd` invocation is safe to
// run under cron - it fetches fresh library state, preloads, and exits. The daemon
// loop is strictly opt-in via -daemon.
func selectMode(once, daemon, verify, estimateMode bool) (string, error) {
	// -verify is priority 1, then -estimate; both win even when -once and
	// -daemon are both set. The mutual-exclusion check only applies to the
	// once/daemon lifecycle. The param is estimateMode (not estimate) so it does
	// not shadow the imported estimate package.
	if verify {
		return "verify", nil
	}
	if estimateMode {
		return "estimate", nil
	}
	if once && daemon {
		return "", errors.New("-once and -daemon are mutually exclusive")
	}
	if daemon {
		return "daemon", nil
	}
	return "once", nil // covers both explicit -once and the bare invocation
}

func main() {
	// Read-only diagnostic subcommands (list-users, list-libraries,
	// detect-pathmaps) short-circuit the daemon flag flow.
	if dispatchSubcommand(os.Args) {
		return
	}

	cfgPath := flag.String("config", "config.toml", "path to config file")
	verify := flag.Bool("verify", false, "run one sweep, then report cache residency and exit")
	once := flag.Bool("once", false, "run exactly one sweep then exit (cron model; default when no mode flag is given)")
	daemon := flag.Bool("daemon", false, "run the resident periodic loop (opt-in; use for long-running service installs)")
	estimateMode := flag.Bool("estimate", false, "compute a warm-set projection (estimate.json) and exit; no page-cache I/O")
	flag.Parse()

	log := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo}))
	log.Info("preloadd starting", "version", version)

	mode, err := selectMode(*once, *daemon, *verify, *estimateMode)
	if err != nil {
		log.Error("invalid flags", "err", err)
		os.Exit(1)
	}

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		log.Error("config load failed", "err", err)
		os.Exit(1)
	}

	apiKey, err := secrets.APIKey(cfg.SecretPath)
	if err != nil {
		log.Error("loading API key failed", "err", err)
		os.Exit(1)
	}

	client, err := emby.New(cfg.Server.URL, apiKey, nil)
	if err != nil {
		log.Error("emby client init failed", "err", err)
		os.Exit(1)
	}

	preCfg := preloader.Config{
		TargetSeconds: cfg.Preload.TargetSeconds,
		MinHeadBytes:  cfg.Preload.MinHeadMB << 20,
		MaxHeadBytes:  cfg.Preload.MaxHeadMB << 20,
		TailBytes:     cfg.Preload.TailMB << 20,
	}
	mapper := buildMapper(context.Background(), cfg.PathMap, execRunner, log)
	pre := preloader.New(preCfg, pagecache.New(cfg.Residency.ProbeBytes, cfg.Residency.ProbeThreshold, cfg.Residency.ProbeTimeout, log), mapper, preloader.DefaultFS(), log)

	d := app.NewDaemon(cfg, client, pre, log)

	switch mode {
	case "verify":
		stats, verifyErr := app.SweepWithUsers(context.Background(), client, pre, cfg, d.Budget(), "verify", log)
		if verifyErr != nil {
			log.Error("verify sweep failed", "err", verifyErr)
			os.Exit(1)
		}
		mean, known, _ := app.ReportResidency(pagecache.New(cfg.Residency.ProbeBytes, cfg.Residency.ProbeThreshold, cfg.Residency.ProbeTimeout, log), stats.Warmed, log)
		if len(stats.Warmed) == 0 {
			log.Info("verify complete (no items preloaded this sweep - nothing to measure)", "preloaded", stats.Preloaded, "skipped", stats.Skipped, "missing", stats.Missing)
		} else if !known {
			log.Info("verify complete (residency unavailable on this platform - mincore is Linux-only)", "preloaded", stats.Preloaded, "skipped", stats.Skipped, "missing", stats.Missing)
		} else {
			log.Info("verify complete", "mean_resident_pct", mean, "preloaded", stats.Preloaded, "skipped", stats.Skipped, "missing", stats.Missing)
		}

	case "once":
		stats, sweepErr := app.SweepWithUsers(context.Background(), client, pre, cfg, d.Budget(), "once", log)
		if sweepErr != nil {
			log.Error("sweep failed", "err", sweepErr)
			os.Exit(1)
		}
		log.Info("one-shot sweep done",
			"preloaded", stats.Preloaded,
			"skipped", stats.Skipped,
			"missing", stats.Missing)

	case "daemon":
		ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
		loopErr := d.Loop(ctx)
		stop() // release signal resources before exit
		if loopErr != nil && ctx.Err() == nil {
			log.Error("daemon loop exited with error", "err", loopErr)
			os.Exit(1)
		}
		log.Info("preloadd stopped")

	case "estimate":
		est, estErr := app.ProjectWarmSet(context.Background(), client, preCfg, d.Budget(),
			cfg.Preload.RAMPercent, time.Now().UTC().Format(time.RFC3339), mapper.ToHost, log)
		if estErr != nil {
			log.Error("estimate failed", "err", estErr)
			os.Exit(1)
		}
		estPath := filepath.Join(filepath.Dir(cfg.StatusPath), "estimate.json")
		if err := estimate.Write(estPath, est); err != nil {
			log.Error("writing estimate failed", "err", err)
			os.Exit(1)
		}
		log.Info("estimate complete",
			"items", est.Meta.ItemCount,
			"budget_bytes", est.BudgetBytes,
			"ceiling_truncated", est.Meta.CeilingTruncated,
			"path", estPath)

	default:
		// Should be unreachable given selectMode's exhaustive switch.
		log.Error("internal error: unknown mode", "mode", mode)
		fmt.Fprintf(os.Stderr, "unknown mode %q\n", mode)
		os.Exit(1)
	}
}
