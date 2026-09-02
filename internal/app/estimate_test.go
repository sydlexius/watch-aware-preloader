package app

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/pathmap"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
)

func estCfg() preloader.Config {
	return preloader.Config{TargetSeconds: 20, MinHeadBytes: 8 << 20, MaxHeadBytes: 250 << 20, TailBytes: 16 << 20}
}

func TestProjectBytesAddsTailAndResumeFront(t *testing.T) {
	cfg := estCfg()
	// Bitrate known: HeadBytes = TargetSeconds * bps/8, clamped. 8 Mbps * 20s / 8 = 20 MB.
	it := core.MediaItem{BitrateBps: 8_000_000}
	head := preloader.HeadBytes(cfg, it)

	nextUp := projectBytes(cfg, it, core.TierNextUp)
	if nextUp != head+cfg.TailBytes {
		t.Errorf("next-up projected = %d, want head+tail = %d", nextUp, head+cfg.TailBytes)
	}
	resume := projectBytes(cfg, it, core.TierResume)
	if resume != head+cfg.TailBytes+estimateResumeFrontBytes {
		t.Errorf("resume projected = %d, want head+tail+front = %d", resume, head+cfg.TailBytes+estimateResumeFrontBytes)
	}
	if resume <= nextUp {
		t.Errorf("resume (%d) should exceed next-up (%d) by the front allowance", resume, nextUp)
	}
}

func TestProjectBytesGeometryFallback(t *testing.T) {
	cfg := estCfg()
	// No bitrate: HeadBytes derives bps from SizeBytes/Runtime. 1 GiB over 1000s.
	it := core.MediaItem{SizeBytes: 1 << 30, Runtime: 1000 * time.Second}
	if got := projectBytes(cfg, it, core.TierNextUp); got != preloader.HeadBytes(cfg, it)+cfg.TailBytes {
		t.Errorf("geometry projected = %d, want head+tail", got)
	}
	if preloader.HeadBytes(cfg, it) <= 0 {
		t.Fatal("expected a positive geometry-derived head; sizing fallback not exercised")
	}
}

func TestProjectWarmSetFullUniverse(t *testing.T) {
	// Two users, resume + next-up items; one library scoping /mnt/user/TV.
	p := &stubProvider{
		users:     []core.User{{ID: "3", Name: "jesse"}, {ID: "7", Name: "rachel"}},
		libraries: []core.Library{{ID: "L1", Locations: []string{"/mnt/user/TV"}}},
		resume: map[string][]core.MediaItem{
			"3": {{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, UserID: "3"}},
		},
		nextUp: map[string][]core.MediaItem{
			"7": {{ID: "b", ServerPath: "/mnt/user/TV/b.mkv", BitrateBps: 8_000_000, UserID: "7"}},
		},
		latest:  map[string][]core.MediaItem{},
		playing: map[string]bool{},
	}
	toHost := pathmap.New(nil).ToHost // identity mapper: host path == server path
	est, err := ProjectWarmSet(context.Background(), p, estCfg(), 16<<30, 50, "2026-07-10T00:00:00Z", toHost, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if est.SchemaVersion != 1 || est.BudgetBytes != 16<<30 || est.CeilingPerUserTier != estimateCeilingPerUserTier {
		t.Errorf("header wrong: %+v", est)
	}
	if len(est.Rows) != 2 || est.Meta.ItemCount != 2 {
		t.Fatalf("expected 2 rows, got %d: %+v", len(est.Rows), est.Rows)
	}
	// Rows carry anon keys, are attributed to L1, and are rank-ordered (resume first).
	if est.Rows[0].T != "resume" || est.Rows[0].U != "3" || est.Rows[0].L != "L1" || est.Rows[0].R != 0 {
		t.Errorf("row0 wrong: %+v", est.Rows[0])
	}
	if est.Rows[1].T != "next-up" || est.Rows[1].U != "7" || est.Rows[1].R != 1 {
		t.Errorf("row1 wrong: %+v", est.Rows[1])
	}
	// The resume row's bytes include the front allowance; the next-up row does not.
	if est.Rows[0].B <= est.Rows[1].B {
		t.Errorf("resume bytes (%d) should exceed next-up bytes (%d)", est.Rows[0].B, est.Rows[1].B)
	}
}

func TestProjectWarmSetUnattributableLibraryIsBlank(t *testing.T) {
	// Item path under no known library location -> l == "".
	p := &stubProvider{
		users:     []core.User{{ID: "3", Name: "jesse"}},
		libraries: []core.Library{{ID: "L1", Locations: []string{"/mnt/user/Movies"}}},
		nextUp:    map[string][]core.MediaItem{"3": {{ID: "x", ServerPath: "/mnt/user/TV/x.mkv", BitrateBps: 8_000_000, UserID: "3"}}},
		resume:    map[string][]core.MediaItem{},
		latest:    map[string][]core.MediaItem{},
		playing:   map[string]bool{},
	}
	toHost := pathmap.New(nil).ToHost
	est, err := ProjectWarmSet(context.Background(), p, estCfg(), 1<<30, 50, "t", toHost, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	if len(est.Rows) != 1 || est.Rows[0].L != "" {
		t.Errorf("expected 1 row with blank library, got %+v", est.Rows)
	}
}

func TestNewLibraryAttributorSkipsUnmappableLibrary(t *testing.T) {
	// L1's location does not map (toHost returns ok=false), so libscope falls
	// back to allow-all for it; the attributor must SKIP it rather than let its
	// allow-all scope swallow every item. L2 maps normally.
	toHost := func(p string) (string, bool) {
		if p == "/nomap" {
			return "", false
		}
		return p, true
	}
	libs := []core.Library{
		{ID: "L1", Locations: []string{"/nomap"}},
		{ID: "L2", Locations: []string{"/mnt/user/TV"}},
	}
	attr := newLibraryAttributor(libs, toHost)
	if got := attr("/mnt/user/TV/x.mkv"); got != "L2" {
		t.Errorf("attr(TV item) = %q, want L2 (L1 unmappable must be skipped, not swallow it)", got)
	}
	if got := attr("/other/y.mkv"); got != "" {
		t.Errorf("attr(unknown path) = %q, want empty", got)
	}
}
