package preloader

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/container"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/pathmap"
)

// fakeCache records Warm calls and reports nothing resident.
type fakeCache struct {
	warmed   []warmCall
	resident int64
	warmErr  error // returned by Warm when set
	// residentPaths, when set, marks specific paths as fully resident
	// regardless of the resident field, so a test can mix resident and cold
	// targets in one run.
	residentPaths map[string]bool
}
type warmCall struct {
	path           string
	offset, length int64
}

func (f *fakeCache) Warm(path string, offset, length int64) error {
	f.warmed = append(f.warmed, warmCall{path, offset, length})
	return f.warmErr
}
func (f *fakeCache) Resident(path string, _, length int64) (int64, bool, error) {
	if f.residentPaths[path] {
		return length, true, nil // fully resident
	}
	if f.resident < 0 {
		return 0, false, nil // residency unknown
	}
	return f.resident, true, nil
}

type fakeFS map[string]int64 // path -> size

func (m fakeFS) Stat(path string) (int64, error) {
	sz, ok := m[path]
	if !ok {
		return 0, io.EOF // stand-in for "not found"
	}
	return sz, nil
}

func testCfg() Config {
	return Config{TargetSeconds: 20, MinHeadBytes: 8 << 20, MaxHeadBytes: 250 << 20, TailBytes: 1 << 20}
}

func TestHeadBytesDurationBased(t *testing.T) {
	// 25 Mbps over 20s = 25e6/8*20 = 62.5 MB, within clamp.
	it := core.MediaItem{BitrateBps: 25_000_000}
	got := HeadBytes(testCfg(), it)
	want := int64(20) * 25_000_000 / 8
	if got != want {
		t.Errorf("HeadBytes = %d, want %d", got, want)
	}
}

func TestPlanHeadReportsTruncation(t *testing.T) {
	cfg := testCfg() // 20s target, 250 MiB clamp

	// Below the crossover the request is honored in full and nothing is lost.
	within := PlanHead(cfg, core.MediaItem{BitrateBps: 60_000_000})
	if within.Truncated {
		t.Error("Truncated = true at 60 Mbps, which fits inside the clamp")
	}
	if within.CoveredSeconds < 19.9 || within.CoveredSeconds > 20.1 {
		t.Errorf("CoveredSeconds = %.2f, want the configured 20s", within.CoveredSeconds)
	}

	// Above it the clamp overrides the duration. This is the case #112 exists
	// for: the operator asked for 20s and gets less, with nothing to say so.
	over := PlanHead(cfg, core.MediaItem{BitrateBps: 128_000_000})
	if !over.Truncated {
		t.Fatal("Truncated = false at 128 Mbps, where 250 MiB cannot hold 20s")
	}
	if over.Bytes != cfg.MaxHeadBytes {
		t.Errorf("Bytes = %d, want the clamp %d", over.Bytes, cfg.MaxHeadBytes)
	}
	// 250 MiB at 128 Mbps is about 16.4s - materially short of 20s, and short of
	// the ~9.9s worst-case spin-up plus seek and transfer margin (#5).
	if over.CoveredSeconds > 17 || over.CoveredSeconds < 16 {
		t.Errorf("CoveredSeconds = %.2f, want about 16.4", over.CoveredSeconds)
	}

	// Raising target_seconds must NOT change a truncated result - that is the
	// property that makes the dial misleading, so it is pinned rather than
	// assumed.
	cfg30 := cfg
	cfg30.TargetSeconds = 30
	if got := PlanHead(cfg30, core.MediaItem{BitrateBps: 128_000_000}); got.Bytes != over.Bytes {
		t.Errorf("raising target_seconds changed a clamped head: %d -> %d", over.Bytes, got.Bytes)
	}
}

func TestPlanHeadFloorIsNotTruncation(t *testing.T) {
	// MinHeadBytes raising a small request grants MORE coverage than asked for.
	// Reporting that as truncation would cry wolf on every low-bitrate item.
	got := PlanHead(testCfg(), core.MediaItem{BitrateBps: 1_000_000})
	if got.Truncated {
		t.Error("Truncated = true when the floor raised the head; nothing was lost")
	}
	if got.CoveredSeconds < 20 {
		t.Errorf("CoveredSeconds = %.2f, want at least the 20s target", got.CoveredSeconds)
	}
}

func TestPlanHeadUnknownBitrateReportsNoFalseTruncation(t *testing.T) {
	// No bitrate and no runtime: the head falls to the floor and CoveredSeconds
	// is unknowable. It must not claim a truncation it cannot substantiate.
	got := PlanHead(testCfg(), core.MediaItem{})
	if got.Truncated {
		t.Error("Truncated = true for an item with no bitrate to measure against")
	}
	if got.Bytes != testCfg().MinHeadBytes {
		t.Errorf("Bytes = %d, want the floor", got.Bytes)
	}
}

func TestHeadBytesMatchesPlanHead(t *testing.T) {
	// HeadBytes is kept as the thin wrapper both callers already use; if the two
	// ever diverge the estimate and the preloader would disagree about the same
	// item, which is how a budget meter starts lying.
	cfg := testCfg()
	for _, bps := range []int64{0, 1_000_000, 25_000_000, 60_000_000, 128_000_000} {
		it := core.MediaItem{BitrateBps: bps}
		if HeadBytes(cfg, it) != PlanHead(cfg, it).Bytes {
			t.Errorf("HeadBytes and PlanHead disagree at %d bps", bps)
		}
	}
}

func TestHeadBytesClampsLow(t *testing.T) {
	it := core.MediaItem{BitrateBps: 1_000_000} // 20s = 2.5MB < 8MB floor
	if got := HeadBytes(testCfg(), it); got != 8<<20 {
		t.Errorf("HeadBytes = %d, want floor 8MiB", got)
	}
}

func TestHeadBytesFallbackToSizeOverRuntime(t *testing.T) {
	// 600 MiB over 20 min => ~4.2 Mbps => 20s head ~10 MiB, above the 8 MiB floor.
	// A fallback that silently clamps to MinHeadBytes would return exactly 8 MiB and fail this check.
	cfg := testCfg()
	it := core.MediaItem{SizeBytes: 600 << 20, Runtime: 20 * time.Minute}
	got := HeadBytes(cfg, it)
	if got <= cfg.MinHeadBytes {
		t.Fatalf("HeadBytes = %d, want strictly > MinHeadBytes (%d); fallback may be clamping to floor", got, cfg.MinHeadBytes)
	}
}

func TestTruncationReachesTheLogDuringASweep(t *testing.T) {
	// The point of #112 is that the operator can SEE the truncation. A struct
	// field nobody prints would fix nothing, so this drives a real sweep and
	// asserts the warning reaches a real handler.
	var buf bytes.Buffer
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/peak.mkv": 40 << 30}
	p := New(testCfg(), cache, pathmap.New(nil), fs,
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	// 128 Mbps: 20s wants ~320 MiB, past the 250 MiB clamp.
	targets := []core.PreloadTarget{
		{Item: core.MediaItem{ID: "peak", ServerPath: "/mnt/user/TV/peak.mkv", BitrateBps: 128_000_000}, Tier: core.TierNextUp},
	}
	p.Run(context.Background(), targets, 1<<40)

	out := buf.String()
	if !strings.Contains(out, "head truncated by max_head_mb") {
		t.Fatalf("truncation was not logged; output was:\n%s", out)
	}
	// The numbers are what make it actionable - "truncated" alone does not say
	// by how much, and the operator needs that to judge raising max_head_mb.
	if !strings.Contains(out, "covered_seconds=16") {
		t.Errorf("log omits the actual coverage; output was:\n%s", out)
	}
	if !strings.Contains(out, "target_seconds=20") {
		t.Errorf("log omits the requested target; output was:\n%s", out)
	}
}

func TestNoTruncationWarningWhenTheHeadFits(t *testing.T) {
	// Warning on every item would train the operator to ignore it.
	var buf bytes.Buffer
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/hd.mkv": 8 << 30}
	p := New(testCfg(), cache, pathmap.New(nil), fs,
		slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn})))

	targets := []core.PreloadTarget{
		{Item: core.MediaItem{ID: "hd", ServerPath: "/mnt/user/TV/hd.mkv", BitrateBps: 25_000_000}, Tier: core.TierNextUp},
	}
	p.Run(context.Background(), targets, 1<<40)

	if strings.Contains(buf.String(), "head truncated") {
		t.Errorf("warned on an item that fits inside the clamp:\n%s", buf.String())
	}
}

func TestPoolResidentSizesWithoutASpinUpAllowance(t *testing.T) {
	// A pool never spins down, so the spin-up allowance buys nothing there. The
	// head should drop to the floor, freeing budget for array-resident items.
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/cache/TV/a.mkv": 8 << 30, "/mnt/user/TV/b.mkv": 8 << 30}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	onPool := func(hostPath string) bool { return strings.HasPrefix(hostPath, "/mnt/cache/") }

	p := New(testCfg(), cache, pathmap.New(nil), fs, log, WithPoolResident(onPool))

	pooled := p.planWarm(
		core.PreloadTarget{Item: core.MediaItem{ServerPath: "/mnt/cache/TV/a.mkv", BitrateBps: 25_000_000}, Tier: core.TierNextUp},
		"/mnt/cache/TV/a.mkv", 8<<30)
	if pooled.head != testCfg().MinHeadBytes {
		t.Errorf("pooled head = %d, want the floor %d", pooled.head, testCfg().MinHeadBytes)
	}

	// Same item on the array keeps the full duration-based head.
	onArray := p.planWarm(
		core.PreloadTarget{Item: core.MediaItem{ServerPath: "/mnt/user/TV/b.mkv", BitrateBps: 25_000_000}, Tier: core.TierNextUp},
		"/mnt/user/TV/b.mkv", 8<<30)
	want := HeadBytes(testCfg(), core.MediaItem{BitrateBps: 25_000_000})
	if onArray.head != want {
		t.Errorf("array head = %d, want the full %d", onArray.head, want)
	}
	if onArray.head <= pooled.head {
		t.Error("array head is not larger than the pooled head; the optimisation did nothing")
	}
}

func TestUncertainPlacementSizesForTheArray(t *testing.T) {
	// The safety property: the optimisation applies ONLY on a positive answer.
	// A wrong SMALL head silently reintroduces the stall this project removes,
	// so every uncertain case must size as if the file were on a spinning disk.
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/a.mkv": 8 << 30}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	it := core.MediaItem{ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 25_000_000}
	want := HeadBytes(testCfg(), it)

	for name, opts := range map[string][]Option{
		"no resolver configured":      nil,
		"resolver says not on a pool": {WithPoolResident(func(string) bool { return false })},
	} {
		p := New(testCfg(), cache, pathmap.New(nil), fs, log, opts...)
		got := p.planWarm(core.PreloadTarget{Item: it, Tier: core.TierNextUp}, "/mnt/user/TV/a.mkv", 8<<30)
		if got.head != want {
			t.Errorf("%s: head = %d, want the full array-sized %d", name, got.head, want)
		}
	}
}

func TestRunSkipsMissingAndBudgets(t *testing.T) {
	cache := &fakeCache{resident: -1} // unknown residency => always warm
	fs := fakeFS{"/mnt/user/TV/a.mkv": 5 << 30}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))

	targets := []core.PreloadTarget{
		{Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 25_000_000}, Tier: core.TierNextUp},
		{Item: core.MediaItem{ID: "missing", ServerPath: "/mnt/user/TV/none.mkv", BitrateBps: 25_000_000}, Tier: core.TierNextUp},
	}
	// Budget only fits one head + tail.
	budget := HeadBytes(testCfg(), targets[0].Item) + testCfg().TailBytes + 1
	stats := p.Run(context.Background(), targets, budget)

	if stats.Preloaded != 1 {
		t.Errorf("Preloaded = %d, want 1", stats.Preloaded)
	}
	if stats.Missing != 1 {
		t.Errorf("Missing = %d, want 1", stats.Missing)
	}
	if len(cache.warmed) == 0 || cache.warmed[0].path != "/mnt/user/TV/a.mkv" {
		t.Errorf("expected warm of a.mkv, got %+v", cache.warmed)
	}
}

func TestRunResumeUsesOffset(t *testing.T) {
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/a.mkv": 5 << 30}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, ResumeOffset: 10 * time.Minute},
		Tier: core.TierResume,
	}}
	p.Run(context.Background(), targets, 1<<40)
	// offset = 600s * 8e6/8 = 600 * 1e6 = 600_000_000 bytes
	if cache.warmed[0].offset != 600_000_000 {
		t.Errorf("resume offset = %d, want 600000000", cache.warmed[0].offset)
	}
}

func TestRunResumeOffsetBitrateFallback(t *testing.T) {
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/a.mkv": 600 << 20}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// No BitrateBps: bitrate must be derived from SizeBytes/Runtime, else the
	// resume item wrongly warms from the file head (offset 0).
	it := core.MediaItem{
		ID: "a", ServerPath: "/mnt/user/TV/a.mkv",
		SizeBytes: 600 << 20, Runtime: 20 * time.Minute, ResumeOffset: 10 * time.Minute,
	}
	p.Run(context.Background(), []core.PreloadTarget{{Item: it, Tier: core.TierResume}}, 1<<40)
	if len(cache.warmed) == 0 {
		t.Fatal("expected a warm call")
	}
	// bps = 600MiB/1200s*8; offset = 600s * bps/8 = 300MiB.
	if want := int64(300 << 20); cache.warmed[0].offset != want {
		t.Errorf("resume offset = %d, want %d (bitrate fallback)", cache.warmed[0].offset, want)
	}
}

func TestRunTailOverlapNotDoubleCharged(t *testing.T) {
	cache := &fakeCache{resident: -1} // always warm
	const size = 5 << 20
	fs := fakeFS{"/m/a.mkv": size}
	// Head clamps to 4MiB; tail (2MiB) would start at 3MiB, overlapping the head
	// window [0,4MiB), so it must clamp to [4MiB,5MiB) = 1MiB, not a full 2MiB.
	cfg := Config{TargetSeconds: 20, MinHeadBytes: 1 << 20, MaxHeadBytes: 4 << 20, TailBytes: 2 << 20}
	p := New(cfg, cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/m/a.mkv", BitrateBps: 1_000_000_000},
		Tier: core.TierNextUp,
	}}
	stats := p.Run(context.Background(), targets, 1<<40)
	if want := int64(5 << 20); stats.BytesWarmed != want {
		t.Errorf("BytesWarmed = %d, want %d (overlapping tail must not be double-charged)", stats.BytesWarmed, want)
	}
	if len(cache.warmed) != 2 {
		t.Fatalf("want 2 warm calls (head+tail), got %d: %+v", len(cache.warmed), cache.warmed)
	}
	if cache.warmed[1].offset != 4<<20 || cache.warmed[1].length != 1<<20 {
		t.Errorf("tail warm = offset %d len %d, want offset %d len %d",
			cache.warmed[1].offset, cache.warmed[1].length, 4<<20, 1<<20)
	}
}

func TestRunSmallFileWarmsTail(t *testing.T) {
	cache := &fakeCache{resident: -1} // always warm
	const size = 3 << 20
	fs := fakeFS{"/m/a.mkv": size}
	// File (3MiB) is below TailBytes (4MiB) and the head clamps to 2MiB, stopping
	// before EOF; the [2MiB,3MiB) suffix must still be warmed (regression: the old
	// `size > TailBytes` guard left it cold).
	cfg := Config{TargetSeconds: 20, MinHeadBytes: 1 << 20, MaxHeadBytes: 2 << 20, TailBytes: 4 << 20}
	p := New(cfg, cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/m/a.mkv", BitrateBps: 1_000_000_000},
		Tier: core.TierNextUp,
	}}
	stats := p.Run(context.Background(), targets, 1<<40)
	if len(cache.warmed) != 2 {
		t.Fatalf("want 2 warm calls (head+tail), got %d: %+v", len(cache.warmed), cache.warmed)
	}
	if cache.warmed[1].offset != 2<<20 || cache.warmed[1].length != 1<<20 {
		t.Errorf("tail warm = offset %d len %d, want offset %d len %d",
			cache.warmed[1].offset, cache.warmed[1].length, 2<<20, 1<<20)
	}
	if want := int64(3 << 20); stats.BytesWarmed != want {
		t.Errorf("BytesWarmed = %d, want %d", stats.BytesWarmed, want)
	}
}

func TestRunResumeOffsetOnlyForResumeTier(t *testing.T) {
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/a.mkv": 5 << 30}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Same item as TestRunResumeUsesOffset but Tier=TierNextUp; offset must NOT be applied.
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, ResumeOffset: 10 * time.Minute},
		Tier: core.TierNextUp,
	}}
	p.Run(context.Background(), targets, 1<<40)
	if len(cache.warmed) == 0 {
		t.Fatal("expected at least one Warm call")
	}
	if cache.warmed[0].offset != 0 {
		t.Errorf("warm offset = %d, want 0 (resume offset must not apply to non-resume tier)", cache.warmed[0].offset)
	}
}

func TestRunWarmedRangesPopulated(t *testing.T) {
	cache := &fakeCache{resident: -1}
	fs := fakeFS{"/mnt/user/TV/a.mkv": 5 << 30}
	cfg := testCfg()
	p := New(cfg, cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	item := core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 25_000_000}
	targets := []core.PreloadTarget{
		{Item: item, Tier: core.TierNextUp},
	}
	stats := p.Run(context.Background(), targets, 1<<40)

	if len(stats.Warmed) != 2 {
		t.Fatalf("Warmed = %v, want 2 entries (head+tail)", stats.Warmed)
	}
	wantHead := WarmedRange{
		Path:   "/mnt/user/TV/a.mkv",
		Offset: 0,
		Length: HeadBytes(cfg, item),
	}
	if stats.Warmed[0] != wantHead {
		t.Errorf("Warmed[0] = %+v, want %+v", stats.Warmed[0], wantHead)
	}
	const size = 5 << 30
	wantTail := WarmedRange{
		Path:   "/mnt/user/TV/a.mkv",
		Offset: size - cfg.TailBytes,
		Length: cfg.TailBytes,
	}
	if stats.Warmed[1] != wantTail {
		t.Errorf("Warmed[1] = %+v, want %+v", stats.Warmed[1], wantTail)
	}
}

func TestRunWarmErrorNotCountedPreloaded(t *testing.T) {
	cache := &fakeCache{resident: -1, warmErr: io.ErrUnexpectedEOF}
	fs := fakeFS{"/mnt/user/TV/a.mkv": 5 << 30}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 25_000_000},
		Tier: core.TierNextUp,
	}}
	stats := p.Run(context.Background(), targets, 1<<40)
	if stats.Preloaded != 0 {
		t.Errorf("Preloaded = %d, want 0 when Warm returns an error", stats.Preloaded)
	}
}

func TestRunResumeWarmsFrontAndExactCueTail(t *testing.T) {
	cache := &fakeCache{resident: -1} // always warm
	const size = int64(20 << 30)
	fs := fakeFS{"/mnt/user/4K/a.mkv": size}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	// Inject a layout: front metadata ends at 200 KiB; cue index starts 8 MiB
	// before EOF (a long-film cue index the flat 1 MiB tail would miss).
	const frontEnd = int64(200 << 10)
	cueStart := size - (8 << 20)
	p.inspect = func(_ string, _ int64) (container.Layout, bool) {
		return container.Layout{FrontEnd: frontEnd, CueStart: cueStart}, true
	}
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/4K/a.mkv", BitrateBps: 80_000_000, ResumeOffset: 30 * time.Minute},
		Tier: core.TierResume,
	}}
	stats := p.Run(context.Background(), targets, 1<<40)

	// Expect three warm calls in warm order: head (fatal, first), then the
	// best-effort front [0,200KiB) and tail [cueStart,EOF).
	if len(cache.warmed) != 3 {
		t.Fatalf("want 3 warm calls (head+front+tail), got %d: %+v", len(cache.warmed), cache.warmed)
	}
	front := cache.warmed[1]
	if front.offset != 0 || front.length != frontEnd {
		t.Errorf("front warm = offset %d len %d, want offset 0 len %d", front.offset, front.length, frontEnd)
	}
	tail := cache.warmed[2]
	if tail.offset != cueStart || tail.length != size-cueStart {
		t.Errorf("cue tail = offset %d len %d, want offset %d len %d", tail.offset, tail.length, cueStart, size-cueStart)
	}

	// stats.Warmed must record all three ranges (front, head, tail) so -verify
	// can check residency of the cue tail, not just the head.
	if len(stats.Warmed) != 3 {
		t.Fatalf("Warmed = %v, want 3 entries (front+head+tail)", stats.Warmed)
	}
	wantFront := WarmedRange{Path: "/mnt/user/4K/a.mkv", Offset: 0, Length: frontEnd}
	if stats.Warmed[0] != wantFront {
		t.Errorf("Warmed[0] = %+v, want %+v", stats.Warmed[0], wantFront)
	}
	wantHead := WarmedRange{Path: "/mnt/user/4K/a.mkv", Offset: cache.warmed[0].offset, Length: cache.warmed[0].length}
	if stats.Warmed[1] != wantHead {
		t.Errorf("Warmed[1] = %+v, want %+v", stats.Warmed[1], wantHead)
	}
	wantTail := WarmedRange{Path: "/mnt/user/4K/a.mkv", Offset: cueStart, Length: size - cueStart}
	if stats.Warmed[2] != wantTail {
		t.Errorf("Warmed[2] = %+v, want %+v", stats.Warmed[2], wantTail)
	}
}

func TestRunResumeNearEOFSuppressesOverlappingCueTail(t *testing.T) {
	// A resume whose content window reaches EOF: a parsed cue index inside that
	// window must NOT produce a separate tail warm (clampTailToContent collapses
	// it), so BytesWarmed counts only the front + head bytes, with no overlap or
	// double-count.
	cache := &fakeCache{resident: -1} // always warm
	const size = int64(20 << 20)
	fs := fakeFS{"/mnt/user/4K/a.mkv": size}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	const frontEnd = int64(100 << 10)
	// CueStart sits inside the content window [offset, EOF): resume offset is
	// 18 MB (bitrate 8 Mbps => 1 MB/s * 18 s), head clamps to size-offset.
	p.inspect = func(_ string, _ int64) (container.Layout, bool) {
		return container.Layout{FrontEnd: frontEnd, CueStart: 19_000_000}, true
	}
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/4K/a.mkv", BitrateBps: 8_000_000, ResumeOffset: 18 * time.Second},
		Tier: core.TierResume,
	}}
	stats := p.Run(context.Background(), targets, 1<<40)

	// Only head + front are warmed; the overlapping cue tail is suppressed.
	if len(cache.warmed) != 2 {
		t.Fatalf("want 2 warm calls (head+front, tail suppressed), got %d: %+v", len(cache.warmed), cache.warmed)
	}
	var sum int64
	for _, w := range cache.warmed {
		if w.length < 0 {
			t.Errorf("negative warm length: %+v", w)
		}
		if w.offset+w.length > size {
			t.Errorf("warm range past EOF: offset %d len %d (size %d)", w.offset, w.length, size)
		}
		sum += w.length
	}
	if stats.BytesWarmed != sum {
		t.Errorf("BytesWarmed = %d, want %d (must equal the unique warmed bytes, no tail double-count)", stats.BytesWarmed, sum)
	}
}

func TestRunResumeFallsBackToFlatTailOnParseFailure(t *testing.T) {
	cache := &fakeCache{resident: -1}
	const size = int64(5 << 30)
	fs := fakeFS{"/mnt/user/TV/a.mkv": size}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	p.inspect = func(_ string, _ int64) (container.Layout, bool) {
		return container.Layout{}, false // parse failure -> flat tail, no front
	}
	targets := []core.PreloadTarget{{
		Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, ResumeOffset: 10 * time.Minute},
		Tier: core.TierResume,
	}}
	p.Run(context.Background(), targets, 1<<40)
	// No front warm; head first, flat 1 MiB tail second.
	if len(cache.warmed) != 2 {
		t.Fatalf("want 2 warm calls (head+flat tail), got %d: %+v", len(cache.warmed), cache.warmed)
	}
	if cache.warmed[1].length != testCfg().TailBytes {
		t.Errorf("fallback tail len = %d, want flat %d", cache.warmed[1].length, testCfg().TailBytes)
	}
}

func TestRunByUserCounts(t *testing.T) {
	cache := &fakeCache{resident: -1} // nothing resident -> everything preloads
	fs := fakeFS{
		"/mnt/user/TV/a.mkv": 5 << 30,
		"/mnt/user/TV/b.mkv": 5 << 30,
		"/mnt/user/TV/c.mkv": 5 << 30,
	}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{
		{Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, UserID: "3"}, Tier: core.TierResume},
		{Item: core.MediaItem{ID: "b", ServerPath: "/mnt/user/TV/b.mkv", BitrateBps: 8_000_000, UserID: "3"}, Tier: core.TierNextUp},
		{Item: core.MediaItem{ID: "c", ServerPath: "/mnt/user/TV/c.mkv", BitrateBps: 8_000_000, UserID: "7"}, Tier: core.TierNextUp},
	}
	stats := p.Run(context.Background(), targets, 1<<40)

	if stats.Preloaded != 3 {
		t.Fatalf("Preloaded = %d, want 3", stats.Preloaded)
	}
	if got := stats.ByUser["3"]; got != 2 {
		t.Errorf("ByUser[3] = %d, want 2", got)
	}
	if got := stats.ByUser["7"]; got != 1 {
		t.Errorf("ByUser[7] = %d, want 1", got)
	}
}

func TestRunByUserCountsSkipResident(t *testing.T) {
	cache := &fakeCache{resident: 1 << 40} // everything fully resident -> skip branch
	fs := fakeFS{
		"/mnt/user/TV/a.mkv": 5 << 30,
		"/mnt/user/TV/b.mkv": 5 << 30,
		"/mnt/user/TV/c.mkv": 5 << 30,
	}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{
		{Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, UserID: "3"}, Tier: core.TierResume},
		{Item: core.MediaItem{ID: "b", ServerPath: "/mnt/user/TV/b.mkv", BitrateBps: 8_000_000, UserID: "3"}, Tier: core.TierNextUp},
		{Item: core.MediaItem{ID: "c", ServerPath: "/mnt/user/TV/c.mkv", BitrateBps: 8_000_000, UserID: "7"}, Tier: core.TierNextUp},
	}
	stats := p.Run(context.Background(), targets, 1<<40)

	if stats.Skipped != len(targets) {
		t.Fatalf("Skipped = %d, want %d", stats.Skipped, len(targets))
	}
	if stats.Preloaded != 0 {
		t.Fatalf("Preloaded = %d, want 0", stats.Preloaded)
	}
	// Skipped-resident items must NOT be counted in ByUser/ByTier: those maps
	// track only what was actually preloaded, so with nothing preloaded they
	// are empty.
	if len(stats.ByUser) != 0 {
		t.Errorf("ByUser = %v, want empty (skipped items not counted)", stats.ByUser)
	}
	if len(stats.ByTier) != 0 {
		t.Errorf("ByTier = %v, want empty (skipped items not counted)", stats.ByTier)
	}
}

// TestRunByTierCountsPreloadedOnly mixes already-resident and cold targets and
// asserts the per-tier breakdown sums to Preloaded, not Preloaded+Skipped.
func TestRunByTierCountsPreloadedOnly(t *testing.T) {
	// "a" (resume) is fully resident -> skipped; "b"/"c" (next_up) are cold ->
	// preloaded. residentPaths lets the fake report residency per path.
	fs := fakeFS{
		"/mnt/user/TV/a.mkv": 5 << 30,
		"/mnt/user/TV/b.mkv": 5 << 30,
		"/mnt/user/TV/c.mkv": 5 << 30,
	}
	cache := &fakeCache{resident: -1, residentPaths: map[string]bool{"/mnt/user/TV/a.mkv": true}}
	p := New(testCfg(), cache, pathmap.New(nil), fs, slog.New(slog.NewTextHandler(io.Discard, nil)))
	targets := []core.PreloadTarget{
		{Item: core.MediaItem{ID: "a", ServerPath: "/mnt/user/TV/a.mkv", BitrateBps: 8_000_000, UserID: "3"}, Tier: core.TierResume},
		{Item: core.MediaItem{ID: "b", ServerPath: "/mnt/user/TV/b.mkv", BitrateBps: 8_000_000, UserID: "3"}, Tier: core.TierNextUp},
		{Item: core.MediaItem{ID: "c", ServerPath: "/mnt/user/TV/c.mkv", BitrateBps: 8_000_000, UserID: "7"}, Tier: core.TierNextUp},
	}
	stats := p.Run(context.Background(), targets, 1<<40)

	if stats.Preloaded != 2 || stats.Skipped != 1 {
		t.Fatalf("Preloaded=%d Skipped=%d, want 2/1", stats.Preloaded, stats.Skipped)
	}
	sumTier := 0
	for _, n := range stats.ByTier {
		sumTier += n
	}
	if sumTier != stats.Preloaded {
		t.Errorf("sum(ByTier)=%d, want Preloaded=%d", sumTier, stats.Preloaded)
	}
	if stats.ByTier[core.TierResume] != 0 {
		t.Errorf("ByTier[resume]=%d, want 0 (the resume item was skipped)", stats.ByTier[core.TierResume])
	}
	if stats.ByTier[core.TierNextUp] != 2 {
		t.Errorf("ByTier[next_up]=%d, want 2", stats.ByTier[core.TierNextUp])
	}
	// ByUser mirrors the same invariant: it sums to Preloaded and the skipped
	// user (user 3's resume item) is not double-counted.
	sumUser := 0
	for _, n := range stats.ByUser {
		sumUser += n
	}
	if sumUser != stats.Preloaded {
		t.Errorf("sum(ByUser)=%d, want Preloaded=%d", sumUser, stats.Preloaded)
	}
	if stats.ByUser["3"] != 1 { // only user 3's next_up "b" preloaded; the resume "a" was skipped
		t.Errorf("ByUser[3]=%d, want 1 (resume item skipped, next_up preloaded)", stats.ByUser["3"])
	}
	if stats.ByUser["7"] != 1 {
		t.Errorf("ByUser[7]=%d, want 1", stats.ByUser["7"])
	}
}
