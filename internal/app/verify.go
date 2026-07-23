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

// ReportResidency checks each warmed range's page-cache residency, logs per-range
// results, and returns the mean resident percent across ranges with a known result.
// anyKnown is false when residency cannot be determined on this platform (no mincore).
// measuredCount is the number of ranges for which residency was known and measured.
func ReportResidency(cache pagecache.Cache, warmed []preloader.WarmedRange, log *slog.Logger) (meanPct float64, anyKnown bool, measuredCount int) {
	var sum float64
	var n int
	for _, r := range warmed {
		pct, known, err := VerifyResidency(cache, r.Path, r.Offset, r.Length)
		if err != nil {
			log.Warn("residency check failed", "path", r.Path, "err", err)
			continue
		}
		if !known {
			continue
		}
		method := "mincore"
		if m, ok := cache.(pagecache.Methoder); ok {
			method = m.Method(r.Path)
		}
		log.Info("residency", "path", r.Path, "percent", pct, "method", method)
		sum += pct
		n++
	}
	if n == 0 {
		return 0, false, 0
	}
	return sum / float64(n), true, n
}
