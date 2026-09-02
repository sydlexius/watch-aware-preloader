package app

import (
	"context"
	"io"
	"log/slog"
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/pathmap"
	"github.com/doxazo-net/watch-aware-preloader/internal/preloader"
)

// stubCache is a no-op page-cache stub: Warm always succeeds, residency unknown.
type stubCache struct{}

func (stubCache) Warm(_ string, _, _ int64) error                    { return nil }
func (stubCache) Resident(_ string, _, _ int64) (int64, bool, error) { return 0, false, nil }

func TestRunOnceExcludesNowPlayingEndToEnd(t *testing.T) {
	// UserID mirrors the real provider, which stamps it on every item it returns
	// (emby.embyItem.toCore). Ranking keys tier order off it, so an unstamped
	// item belongs to no user and contributes nothing.
	p := &stubProvider{
		users:   []core.User{{ID: "1", Name: "jesse"}},
		resume:  map[string][]core.MediaItem{"1": {{ID: "r1", ServerPath: "/x/r1.mkv", BitrateBps: 8_000_000, UserID: "1"}}},
		nextUp:  map[string][]core.MediaItem{"1": {{ID: "playing", ServerPath: "/x/p.mkv", BitrateBps: 8_000_000, UserID: "1"}}},
		latest:  map[string][]core.MediaItem{},
		playing: map[string]bool{"playing": true},
	}
	// fakeFS from preloader_test isn't visible here; use a stub FS inline.
	fs := stubFS{"/x/r1.mkv": 5 << 30, "/x/p.mkv": 5 << 30}
	cfg := preloader.Config{TargetSeconds: 20, MinHeadBytes: 8 << 20, MaxHeadBytes: 250 << 20, TailBytes: 1 << 20}
	pre := preloader.New(cfg, stubCache{}, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))

	opts := SweepOptions{Users: p.users, Tiers: allTiers(), Ranks: allTiersRanked(p.users), Budget: 1 << 40}
	stats, err := RunOnce(context.Background(), p, pre, opts, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatal(err)
	}
	// "playing" is excluded; only r1 should be preloaded.
	if stats.Preloaded != 1 {
		t.Errorf("Preloaded = %d, want 1 (now-playing excluded)", stats.Preloaded)
	}
}

type stubFS map[string]int64

func (m stubFS) Stat(path string) (int64, error) {
	if sz, ok := m[path]; ok {
		return sz, nil
	}
	return 0, io.EOF
}

func TestPlayingSignature(t *testing.T) {
	// Same id set in different insertion orders must yield the same signature,
	// and distinct sets must differ.
	a := playingSignature(map[string]bool{"c": true, "a": true, "b": true})
	b := playingSignature(map[string]bool{"a": true, "b": true, "c": true})
	if a != b {
		t.Errorf("signature not order-independent: %q vs %q", a, b)
	}
	if want := "a,b,c"; a != want {
		t.Errorf("signature = %q, want %q", a, want)
	}
	if empty := playingSignature(map[string]bool{}); empty != "" {
		t.Errorf("empty signature = %q, want \"\"", empty)
	}
	if playingSignature(map[string]bool{"a": true}) == playingSignature(map[string]bool{"a": true, "b": true}) {
		t.Error("distinct id sets produced identical signatures")
	}
}
