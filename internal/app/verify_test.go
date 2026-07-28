package app

import (
	"bytes"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
)

type residentCache struct {
	resident int64
	known    bool
}

func (c residentCache) Warm(string, int64, int64) error { return nil }
func (c residentCache) Resident(_ string, _ int64, length int64) (int64, bool, error) {
	if !c.known {
		return 0, false, nil
	}
	if c.resident > length {
		return length, true, nil
	}
	return c.resident, true, nil
}

// erroringCache fails every residency probe, standing in for an I/O or bad-path
// failure on a platform that does support mincore.
type erroringCache struct{}

func (erroringCache) Warm(string, int64, int64) error { return nil }
func (erroringCache) Resident(string, int64, int64) (int64, bool, error) {
	return 0, false, errProbeFailed
}

var errProbeFailed = errors.New("probe failed")

// methoderCache reports a fixed residency byte count and a fixed method.
type methoderCache struct {
	resident int64
	method   string
}

func (m methoderCache) Warm(string, int64, int64) error { return nil }
func (m methoderCache) Resident(_ string, _, length int64) (int64, bool, error) {
	return m.resident, true, nil
}
func (m methoderCache) Method(string) string { return m.method }

func TestVerifyResidencyPercent(t *testing.T) {
	pct, known, err := VerifyResidency(residentCache{resident: 50, known: true}, "/x", 0, 100)
	if err != nil || !known {
		t.Fatalf("err=%v known=%v", err, known)
	}
	if pct != 50.0 {
		t.Errorf("pct = %v, want 50", pct)
	}
}

func TestVerifyResidencyUnknown(t *testing.T) {
	_, known, _ := VerifyResidency(residentCache{known: false}, "/x", 0, 100)
	if known {
		t.Error("expected known=false on platforms without mincore")
	}
}

func TestReportResidency(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))

	t.Run("known", func(t *testing.T) {
		cache := residentCache{resident: 80, known: true}
		warmed := []preloader.WarmedRange{
			{Path: "/a", Offset: 0, Length: 100},
			{Path: "/b", Offset: 0, Length: 100},
		}
		rep := ReportResidency(cache, warmed, log)
		if rep.Measured != 2 {
			t.Fatalf("Measured = %d, want 2", rep.Measured)
		}
		if rep.Failed != 0 || rep.Unsupported != 0 {
			t.Errorf("Failed=%d Unsupported=%d, want 0 0", rep.Failed, rep.Unsupported)
		}
		if rep.MeanPct != 80.0 {
			t.Errorf("MeanPct = %v, want 80.0", rep.MeanPct)
		}
	})

	t.Run("unsupported", func(t *testing.T) {
		cache := residentCache{known: false}
		warmed := []preloader.WarmedRange{
			{Path: "/a", Offset: 0, Length: 100},
		}
		rep := ReportResidency(cache, warmed, log)
		if rep.Measured != 0 {
			t.Errorf("Measured = %d, want 0 on platforms without mincore", rep.Measured)
		}
		if rep.Unsupported != 1 {
			t.Errorf("Unsupported = %d, want 1", rep.Unsupported)
		}
		if rep.Failed != 0 {
			t.Errorf("Failed = %d, want 0 - an unsupported probe is not a failure", rep.Failed)
		}
	})

	// A probe that errors must be counted as a failure, never as
	// unsupported: conflating the two is what made verify claim
	// "mincore is Linux-only" on Linux (issue #94).
	t.Run("probe failures are not unsupported", func(t *testing.T) {
		cache := erroringCache{}
		warmed := []preloader.WarmedRange{
			{Path: "/a", Offset: 0, Length: 100},
			{Path: "/b", Offset: 0, Length: 100},
		}
		rep := ReportResidency(cache, warmed, log)
		if rep.Failed != 2 {
			t.Errorf("Failed = %d, want 2", rep.Failed)
		}
		if rep.Unsupported != 0 {
			t.Errorf("Unsupported = %d, want 0 - an I/O error is not a platform limitation", rep.Unsupported)
		}
		if rep.Measured != 0 {
			t.Errorf("Measured = %d, want 0", rep.Measured)
		}
	})

	t.Run("empty warmed set", func(t *testing.T) {
		rep := ReportResidency(residentCache{resident: 80, known: true}, nil, log)
		if rep != (ResidencyReport{}) {
			t.Errorf("rep = %+v, want zero value for an empty warmed set", rep)
		}
	})
}

func TestReportResidencyLogsMethod(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cache := methoderCache{resident: 1 << 20, method: "timing"}
	warmed := []preloader.WarmedRange{{Path: "/mnt/user/x.mkv", Offset: 0, Length: 1 << 20}}

	rep := ReportResidency(cache, warmed, log)
	if rep.Measured != 1 || rep.MeanPct != 100 {
		t.Fatalf("Measured=%d MeanPct=%v, want 1 100", rep.Measured, rep.MeanPct)
	}
	if !strings.Contains(buf.String(), "method=timing") {
		t.Errorf("residency log missing method=timing:\n%s", buf.String())
	}
}

// TestVerifyCompleteMessage covers the verify-mode reporting branch that
// cmd/preloadd/main.go emits, including the zero-measured case from issue #94.
func TestVerifyCompleteMessage(t *testing.T) {
	tests := []struct {
		name        string
		warmedCount int
		rep         ResidencyReport
		want        string
	}{
		{
			name:        "nothing preloaded",
			warmedCount: 0,
			want:        "verify complete (no items preloaded this sweep - nothing to measure)",
		},
		{
			name:        "measured",
			warmedCount: 2,
			rep:         ResidencyReport{MeanPct: 80, Measured: 2},
			want:        "verify complete",
		},
		{
			name:        "all probes failed",
			warmedCount: 2,
			rep:         ResidencyReport{Failed: 2},
			want:        "verify complete (residency probes failed - see the residency check failed warnings above)",
		},
		{
			name:        "unsupported platform",
			warmedCount: 2,
			rep:         ResidencyReport{Unsupported: 2},
			want:        "verify complete (residency unavailable on this platform - mincore is Linux-only)",
		},
		{
			name:        "mixed failure and unsupported",
			warmedCount: 2,
			rep:         ResidencyReport{Failed: 1, Unsupported: 1},
			want:        "verify complete (residency partly unavailable on this platform and partly failing - mincore is Linux-only)",
		},
		{
			name:        "measured wins over a partial failure",
			warmedCount: 3,
			rep:         ResidencyReport{MeanPct: 90, Measured: 2, Failed: 1},
			want:        "verify complete",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := VerifyCompleteMessage(tt.warmedCount, tt.rep); got != tt.want {
				t.Errorf("VerifyCompleteMessage() = %q, want %q", got, tt.want)
			}
		})
	}
}

// A sweep that preloaded items on Linux, where every probe then errors, must
// not claim the platform lacks mincore - the exact mislabeling CodeRabbit
// flagged on PR #95.
func TestVerifyCompleteMessageDoesNotBlamePlatformForProbeErrors(t *testing.T) {
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	warmed := []preloader.WarmedRange{{Path: "/a", Offset: 0, Length: 100}}

	rep := ReportResidency(erroringCache{}, warmed, log)
	got := VerifyCompleteMessage(len(warmed), rep)

	if strings.Contains(got, "mincore is Linux-only") {
		t.Errorf("probe failure mislabeled as an unsupported platform: %q", got)
	}
}
