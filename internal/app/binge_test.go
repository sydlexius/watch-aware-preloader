package app

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// bingeStub is one user with three next-up episodes (one with no series) and
// a known episode after each of the two that have a series.
func bingeStub() *stubProvider {
	return &stubProvider{
		users: []core.User{{ID: "u1", Name: "Alice"}},
		nextUp: map[string][]core.MediaItem{"u1": {
			{ID: "a3", ServerPath: "/m/a3", SeriesID: "A"},
			{ID: "m1", ServerPath: "/m/m1"}, // no series: never looked ahead
			{ID: "b7", ServerPath: "/m/b7", SeriesID: "B"},
		}},
		after: map[string]core.MediaItem{
			"a3": {ID: "a4", ServerPath: "/m/a4", SeriesID: "A"},
			"b7": {ID: "b8", ServerPath: "/m/b8", SeriesID: "B"},
		},
	}
}

// collect runs CollectCandidates for tiers and returns the candidate IDs per tier.
func collect(ctx context.Context, t *testing.T, p *stubProvider, tiers config.TiersConfig) (map[core.Tier][]string, error) {
	t.Helper()
	ranks := ResolveRanks(&config.Config{Tiers: tiers}, p.users, discardLog())
	cands, _, err := CollectCandidates(ctx, p, p.users, nil, tiers, ranks, nil, discardLog())
	got := map[core.Tier][]string{}
	for _, c := range cands {
		if c.Item.UserID != "u1" {
			t.Errorf("candidate %s has UserID %q, want u1 stamped", c.Item.ID, c.Item.UserID)
		}
		got[c.Tier] = append(got[c.Tier], c.Item.ID)
	}
	return got, err
}

func TestBingeAheadAbsentMeansNoEpisodeFetch(t *testing.T) {
	p := bingeStub()
	got, err := collect(context.Background(), t, p, allTiers())
	if err != nil {
		t.Fatal(err)
	}
	if len(p.episodeCalls) != 0 || len(got[core.TierBingeAhead]) != 0 {
		t.Fatalf("binge-ahead is not in the order but made calls %v / candidates %v",
			p.episodeCalls, got[core.TierBingeAhead])
	}
}

func TestBingeAheadWithNextUpFetchesNextUpOnce(t *testing.T) {
	p := bingeStub()
	tiers := config.TiersConfig{Order: config.TierOrder{core.TierNextUp, core.TierBingeAhead}}
	got, err := collect(context.Background(), t, p, tiers)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.nextUpCalls, []string{"u1"}) {
		t.Errorf("NextUp called %v, want exactly once", p.nextUpCalls)
	}
	if want := []string{"a3", "m1", "b7"}; !slices.Equal(got[core.TierNextUp], want) {
		t.Errorf("next-up candidates = %v, want %v", got[core.TierNextUp], want)
	}
	if want := []string{"a4", "b8"}; !slices.Equal(got[core.TierBingeAhead], want) {
		t.Errorf("binge-ahead candidates = %v, want %v", got[core.TierBingeAhead], want)
	}
	// The no-series item is skipped; the others go out with user, series, item.
	if want := []string{"u1/A/a3", "u1/B/b7"}; !slices.Equal(p.episodeCalls, want) {
		t.Errorf("EpisodeAfter calls = %v, want %v", p.episodeCalls, want)
	}
}

func TestBingeAheadWithoutNextUpTier(t *testing.T) {
	// Next-up is fetched once, as input only: no next-up candidate is emitted.
	p := bingeStub()
	tiers := config.TiersConfig{Order: config.TierOrder{core.TierResume, core.TierBingeAhead}}
	got, err := collect(context.Background(), t, p, tiers)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(p.nextUpCalls, []string{"u1"}) {
		t.Errorf("NextUp called %v, want exactly once", p.nextUpCalls)
	}
	if len(got[core.TierNextUp]) != 0 {
		t.Errorf("next-up tier is off but emitted %v", got[core.TierNextUp])
	}
	if want := []string{"a4", "b8"}; !slices.Equal(got[core.TierBingeAhead], want) {
		t.Errorf("binge-ahead candidates = %v, want %v", got[core.TierBingeAhead], want)
	}
}

func TestBingeAheadBoundedByNextUpCap(t *testing.T) {
	// The NextUp cap bounds the follow-up requests even with the NextUp tier off.
	p := bingeStub()
	p.nextUp["u1"] = []core.MediaItem{
		{ID: "a3", ServerPath: "/m/a3", SeriesID: "A"}, {ID: "b7", ServerPath: "/m/b7", SeriesID: "B"}, {ID: "c1", ServerPath: "/m/c1", SeriesID: "C"}, {ID: "d1", ServerPath: "/m/d1", SeriesID: "D"},
	}
	tiers := config.TiersConfig{
		NextUp: config.TierDial{MaxItems: 2},
		Order:  config.TierOrder{core.TierBingeAhead},
	}
	if _, err := collect(context.Background(), t, p, tiers); err != nil {
		t.Fatal(err)
	}
	if want := []string{"u1/A/a3", "u1/B/b7"}; !slices.Equal(p.episodeCalls, want) {
		t.Errorf("EpisodeAfter calls = %v, want only the first 2 next-up items %v", p.episodeCalls, want)
	}
}

func TestBingeAheadCapStopsRequestsEarly(t *testing.T) {
	p := bingeStub()
	tiers := config.TiersConfig{
		BingeAhead: config.TierDial{MaxItems: 1},
		Order:      config.TierOrder{core.TierBingeAhead},
	}
	got, err := collect(context.Background(), t, p, tiers)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[core.TierBingeAhead], []string{"a4"}) || len(p.episodeCalls) != 1 {
		t.Errorf("binge cap 1: candidates %v after calls %v, want [a4] after one call",
			got[core.TierBingeAhead], p.episodeCalls)
	}
}

func TestBingeAheadOneFailingSeriesDoesNotAbortTheSweep(t *testing.T) {
	p := bingeStub()
	p.resume = map[string][]core.MediaItem{"u1": {{ID: "r1", ServerPath: "/m/r1"}}}
	p.afterErr = map[string]error{"A": errors.New("status 404")}
	tiers := config.TiersConfig{Order: config.TierOrder{core.TierResume, core.TierBingeAhead}}
	got, err := collect(context.Background(), t, p, tiers)
	if err != nil {
		t.Fatalf("one series failing aborted the sweep: %v", err)
	}
	if !slices.Equal(got[core.TierResume], []string{"r1"}) || !slices.Equal(got[core.TierBingeAhead], []string{"b8"}) {
		t.Errorf("got %v, want resume r1 and binge-ahead b8 (series A skipped)", got)
	}
}

func TestBingeAheadCancelledContextAborts(t *testing.T) {
	p := bingeStub()
	p.afterErr = map[string]error{"A": context.Canceled}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	tiers := config.TiersConfig{Order: config.TierOrder{core.TierBingeAhead}}
	if _, err := collect(ctx, t, p, tiers); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled", err)
	}
}

func TestBingeAheadDropsASuccessorWithNoMediaFile(t *testing.T) {
	// #170: the shared guard also covers the binge result.
	p := bingeStub()
	p.after["a3"] = core.MediaItem{ID: "a4", SeriesID: "A"}
	got, err := collect(context.Background(), t, p, config.TiersConfig{Order: config.TierOrder{core.TierBingeAhead}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[core.TierBingeAhead], []string{"b8"}) {
		t.Fatalf("binge-ahead candidates = %v, want [b8]", got[core.TierBingeAhead])
	}
}

func TestBingeAheadUnwarmableSuccessorDoesNotFillTheCap(t *testing.T) {
	// A successor with no media file must not take the only binge slot, or the
	// later guard drops it and the tier ends up empty although series B has a
	// warmable successor.
	p := bingeStub()
	p.after["a3"] = core.MediaItem{ID: "a4", SeriesID: "A"}
	tiers := config.TiersConfig{
		BingeAhead: config.TierDial{MaxItems: 1},
		Order:      config.TierOrder{core.TierBingeAhead},
	}
	got, err := collect(context.Background(), t, p, tiers)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(got[core.TierBingeAhead], []string{"b8"}) {
		t.Fatalf("binge cap 1: candidates = %v, want [b8]", got[core.TierBingeAhead])
	}
}
