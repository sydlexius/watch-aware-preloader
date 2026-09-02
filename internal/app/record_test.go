package app

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/pathmap"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
	"github.com/doxazo-net/watch-aware-preloader/internal/status"
)

func TestBuildStatusMapsFields(t *testing.T) {
	stats := preloader.RunStats{
		Preloaded:   3,
		Skipped:     1,
		Missing:     2,
		BytesWarmed: 1024,
		ByTier:      map[core.Tier]int{core.TierResume: 1, core.TierNextUp: 3},
		ByUser:      map[string]int{"3": 2, "7": 2},
	}
	s := buildStatus("once", 8<<30, 1500*time.Millisecond, stats, nil)

	if s.SchemaVersion != status.SchemaVersion {
		t.Errorf("SchemaVersion = %d, want %d", s.SchemaVersion, status.SchemaVersion)
	}
	if s.Mode != "once" || !s.OK || s.Error != "" {
		t.Errorf("mode/ok/error wrong: %+v", s)
	}
	if s.DurationMs != 1500 || s.BudgetBytes != 8<<30 || s.BytesWarmed != 1024 {
		t.Errorf("numeric fields wrong: %+v", s)
	}
	if s.ByTier["resume"] != 1 || s.ByTier["next_up"] != 3 {
		t.Errorf("ByTier keys not stringified: %+v", s.ByTier)
	}
	if s.ByUser["3"] != 2 || s.ByUser["7"] != 2 {
		t.Errorf("ByUser wrong: %+v", s.ByUser)
	}
}

func TestBuildStatusRecordsError(t *testing.T) {
	s := buildStatus("once", 0, time.Second, preloader.RunStats{}, errors.New("boom"))
	if s.OK {
		t.Error("OK should be false on error")
	}
	if s.Error != "boom" {
		t.Errorf("Error = %q, want boom", s.Error)
	}
}

func TestSweepAndRecordWriteFailureIsNonFatal(t *testing.T) {
	p := &stubProvider{
		users:   []core.User{{ID: "1", Name: "jesse"}},
		resume:  map[string][]core.MediaItem{"1": {{ID: "r1", ServerPath: "/x/r1.mkv", BitrateBps: 8_000_000}}},
		nextUp:  map[string][]core.MediaItem{},
		latest:  map[string][]core.MediaItem{},
		playing: map[string]bool{},
	}
	fs := stubFS{"/x/r1.mkv": 5 << 30}
	cfg := preloader.Config{TargetSeconds: 20, MinHeadBytes: 8 << 20, MaxHeadBytes: 250 << 20, TailBytes: 1 << 20}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	pre := preloader.New(cfg, stubCache{}, pathmap.New(nil), fs, logger)

	// A statusPath whose parent is a regular file forces status.Write's
	// os.MkdirAll to fail.
	f := filepath.Join(t.TempDir(), "notadir")
	if err := os.WriteFile(f, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	statusPath := filepath.Join(f, "status.json")

	base := SweepOptions{Users: p.users, Tiers: allTiers(), Ranks: allTiersRanked(p.users), Budget: 1 << 40}
	wantStats, wantErr := RunOnce(context.Background(), p, pre, base, logger)
	if wantErr != nil {
		t.Fatalf("baseline RunOnce failed: %v", wantErr)
	}

	recorded := base
	recorded.Mode, recorded.StatusPath = "once", statusPath
	stats, err := SweepAndRecord(context.Background(), p, pre, recorded, logger)
	if err != nil {
		t.Fatalf("SweepAndRecord returned err = %v, want nil (write failure must not surface)", err)
	}
	if stats.Preloaded != wantStats.Preloaded {
		t.Errorf("Preloaded = %d, want %d (matching a direct RunOnce)", stats.Preloaded, wantStats.Preloaded)
	}

	if _, statErr := os.Stat(statusPath); statErr == nil {
		t.Error("expected no status.json to be created when write fails")
	}
}

// failingUsersProvider fails user enumeration; every other call is unreachable
// because the sweep never gets that far.
type failingUsersProvider struct{ *stubProvider }

func (failingUsersProvider) Users(context.Context) ([]core.User, error) {
	return nil, errors.New("server unreachable")
}

func TestSweepWithUsersRecordsUserListFailure(t *testing.T) {
	// A user-list outage is a FAILED SWEEP, not a silent early return. If it were
	// not recorded, status.json would keep advertising the last successful run and
	// the settings page would show a healthy warm set while nothing was warmed.
	statusPath := filepath.Join(t.TempDir(), "status.json")
	cfg := &config.Config{StatusPath: statusPath}
	cfg.Tiers.Order = config.DefaultTierOrder()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))

	stats, err := SweepWithUsers(context.Background(), failingUsersProvider{&stubProvider{}}, nil, cfg, 1<<30, "once", logger)

	if err == nil {
		t.Fatal("SweepWithUsers returned nil error on a user-list failure")
	}
	if stats.Preloaded != 0 {
		t.Errorf("Preloaded = %d, want 0", stats.Preloaded)
	}

	b, readErr := os.ReadFile(statusPath)
	if readErr != nil {
		t.Fatalf("status.json not written on a user-list failure: %v", readErr)
	}
	var s status.Status
	if jsonErr := json.Unmarshal(b, &s); jsonErr != nil {
		t.Fatalf("status.json is not valid JSON: %v", jsonErr)
	}
	if s.OK {
		t.Error("status.OK = true, want false on a failed sweep")
	}
	if !strings.Contains(s.Error, "listing users") {
		t.Errorf("status.Error = %q, want it to name the listing-users failure", s.Error)
	}
	if s.Mode != "once" || s.BudgetBytes != 1<<30 {
		t.Errorf("mode/budget not recorded: %+v", s)
	}
}
