package app

import (
	"bytes"
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
		mean, anyKnown, _ := ReportResidency(cache, warmed, log)
		if !anyKnown {
			t.Fatal("expected anyKnown=true")
		}
		if mean != 80.0 {
			t.Errorf("mean = %v, want 80.0", mean)
		}
	})

	t.Run("unknown", func(t *testing.T) {
		cache := residentCache{known: false}
		warmed := []preloader.WarmedRange{
			{Path: "/a", Offset: 0, Length: 100},
		}
		_, anyKnown, _ := ReportResidency(cache, warmed, log)
		if anyKnown {
			t.Error("expected anyKnown=false on platforms without mincore")
		}
	})
}

func TestReportResidencyLogsMethod(t *testing.T) {
	var buf bytes.Buffer
	log := slog.New(slog.NewTextHandler(&buf, nil))
	cache := methoderCache{resident: 1 << 20, method: "timing"}
	warmed := []preloader.WarmedRange{{Path: "/mnt/user/x.mkv", Offset: 0, Length: 1 << 20}}

		mean, known, _ := ReportResidency(cache, warmed, log)
	if !known || mean != 100 {
		t.Fatalf("mean=%v known=%v, want 100 true", mean, known)
	}
	if !strings.Contains(buf.String(), "method=timing") {
		t.Errorf("residency log missing method=timing:\n%s", buf.String())
	}
}
