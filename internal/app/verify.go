package app

import (
	"log/slog"

	"github.com/doxazo-net/watch-aware-preloader/internal/pagecache"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
)

// VerifyResidency reports what percentage of [offset, offset+length) is resident
// in the page cache. known is false on platforms without residency support.
func VerifyResidency(cache pagecache.Cache, hostPath string, offset, length int64) (float64, bool, error) {
	if length <= 0 {
		return 0, true, nil
	}
	resident, known, err := cache.Resident(hostPath, offset, length)
	if err != nil || !known {
		return 0, known, err
	}
	return float64(resident) / float64(length) * 100, true, nil
}

// ResidencyReport summarizes a verify sweep's residency probing. Every warmed
// range increments exactly one counter, so Measured+Failed+Unsupported is the
// number of ranges probed and callers can tell "nothing to measure", "probing
// failed", and "probing unsupported" apart instead of collapsing them into one
// negative signal.
type ResidencyReport struct {
	// MeanPct is the mean resident percent across measured ranges. It is
	// meaningful only when Measured > 0. A zero-length range counts as
	// measured at 0%, so it pulls the mean down.
	MeanPct float64
	// Measured counts ranges whose residency was determined.
	Measured int
	// Failed counts ranges whose residency probe returned an error (I/O,
	// bad path). These say nothing about platform support.
	Failed int
	// Unsupported counts ranges the platform could not probe at all
	// (no mincore). In practice a cache reports this for every range or
	// none, since it is a property of the platform rather than the file.
	Unsupported int
}

// Probed reports how many warmed ranges were classified, i.e. the length of the
// warmed slice ReportResidency was given.
func (r ResidencyReport) Probed() int { return r.Measured + r.Failed + r.Unsupported }

// ReportResidency checks each warmed range's page-cache residency, logs
// per-range results, and returns a report classifying every range.
func ReportResidency(cache pagecache.Cache, warmed []preloader.WarmedRange, log *slog.Logger) ResidencyReport {
	var rep ResidencyReport
	var sum float64
	for _, r := range warmed {
		pct, known, err := VerifyResidency(cache, r.Path, r.Offset, r.Length)
		if err != nil {
			log.Warn("residency check failed", "path", r.Path, "err", err)
			rep.Failed++
			continue
		}
		if !known {
			rep.Unsupported++
			continue
		}
		method := "mincore"
		if m, ok := cache.(pagecache.Methoder); ok {
			method = m.Method(r.Path)
		}
		log.Info("residency", "path", r.Path, "percent", pct, "method", method)
		sum += pct
		rep.Measured++
	}
	if rep.Measured > 0 {
		rep.MeanPct = sum / float64(rep.Measured)
	}
	return rep
}

// VerifyCompleteMessage returns the completion message for a verify sweep that
// produced rep. Each outcome gets its own message so an operator is never told
// the platform is unsupported when the real cause was an empty sweep or a
// failing probe (issue #94).
func VerifyCompleteMessage(rep ResidencyReport) string {
	switch {
	case rep.Probed() == 0:
		return "verify complete (no items preloaded this sweep - nothing to measure)"
	case rep.Measured > 0:
		return "verify complete"
	case rep.Failed > 0:
		return "verify complete (residency probes failed - see the residency check failed warnings above)"
	default:
		return "verify complete (residency unavailable on this platform - mincore is Linux-only)"
	}
}
