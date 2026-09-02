package app

import (
	"context"
	"io"
	"log/slog"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/scorer"
)

func discardLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

// allTiers returns a TiersConfig with every tier enabled and no cap (the
// applyDefaults result), for tests that call the pipeline directly.
// allTiers mirrors what applyDefaults resolves for an all-tiers-enabled config:
// the dials plus the matching order. Order carries enablement for the scorer, so
// omitting it here would mean "warm nothing".
func allTiers() config.TiersConfig {
	return config.TiersConfig{
		Resume:        config.TierDial{Enabled: true},
		NextUp:        config.TierDial{Enabled: true},
		RecentlyAdded: config.TierDial{Enabled: true},
		Order:         config.DefaultTierOrder(),
	}
}

// allTiersRanked resolves allTiers() into RankOpts for the given users, matching
// what the sweep call chain feeds CollectCandidates. Enrollment and per-user tier
// enablement live in RankOpts, so a collection test needs both halves.
func allTiersRanked(users []core.User) scorer.RankOpts {
	return ResolveRanks(&config.Config{Tiers: allTiers()}, users, discardLog())
}

type stubProvider struct {
	users     []core.User
	libraries []core.Library
	resume    map[string][]core.MediaItem
	nextUp    map[string][]core.MediaItem
	latest    map[string][]core.MediaItem
	playing   map[string]bool

	// Per-tier call recorders, appended with the user ID each fetch is made for.
	// A tier disabled for a user must leave no entry: the skipped API call is
	// behavior, not an optimization.
	resumeCalls []string
	nextUpCalls []string
	latestCalls []string
}

func (s *stubProvider) Users(context.Context) ([]core.User, error) { return s.users, nil }
func (s *stubProvider) Libraries(context.Context) ([]core.Library, error) {
	return s.libraries, nil
}
func (s *stubProvider) Resume(_ context.Context, id string) ([]core.MediaItem, error) {
	s.resumeCalls = append(s.resumeCalls, id)
	return s.resume[id], nil
}
func (s *stubProvider) NextUp(_ context.Context, id string) ([]core.MediaItem, error) {
	s.nextUpCalls = append(s.nextUpCalls, id)
	return s.nextUp[id], nil
}
func (s *stubProvider) RecentlyAdded(_ context.Context, id string) ([]core.MediaItem, error) {
	s.latestCalls = append(s.latestCalls, id)
	return s.latest[id], nil
}
func (s *stubProvider) NowPlayingIDs(context.Context) (map[string]bool, error) {
	return s.playing, nil
}

func TestCollectCandidatesTiersAndPlaying(t *testing.T) {
	p := &stubProvider{
		users:   []core.User{{ID: "1", Name: "jesse"}},
		resume:  map[string][]core.MediaItem{"1": {{ID: "r1"}}},
		nextUp:  map[string][]core.MediaItem{"1": {{ID: "n1"}}},
		latest:  map[string][]core.MediaItem{"1": {{ID: "l1"}}},
		playing: map[string]bool{"x": true},
	}
	cands, playing, err := CollectCandidates(context.Background(), p, p.users, nil, allTiers(), allTiersRanked(p.users), nil, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 3 {
		t.Fatalf("expected 3 candidates, got %d", len(cands))
	}
	tierByID := map[string]core.Tier{}
	for _, c := range cands {
		tierByID[c.Item.ID] = c.Tier
	}
	if tierByID["r1"] != core.TierResume || tierByID["n1"] != core.TierNextUp || tierByID["l1"] != core.TierRecentlyAdded {
		t.Errorf("tiers assigned wrong: %v", tierByID)
	}
	if !playing["x"] {
		t.Error("now-playing set not returned")
	}
}

func TestCollectCandidatesLibraryScope(t *testing.T) {
	// Two candidates: one in the Movies library, one in Music. Scoping to
	// Movies (id "m") must drop the Music item.
	p := &stubProvider{
		users: []core.User{{ID: "1", Name: "jesse"}},
		libraries: []core.Library{
			{ID: "m", Name: "Movies", Locations: []string{"/share/Movies"}},
			{ID: "u", Name: "Music", Locations: []string{"/share/Music"}},
		},
		latest: map[string][]core.MediaItem{"1": {
			{ID: "movie", ServerPath: `\\host\Movies\a.mkv`},
			{ID: "song", ServerPath: `\\host\Music\b.flac`},
		}},
	}
	// toHost mirrors the mapper for this topology: UNC and /share both -> /mnt/user.
	toHost := func(path string) (string, bool) {
		if len(path) > 2 && path[:2] == `\\` {
			rest := path[2:]
			for i := 0; i < len(rest); i++ {
				if rest[i] == '\\' {
					return "/mnt/user/" + replaceBackslash(rest[i+1:]), true
				}
			}
			return "", false
		}
		if len(path) >= 7 && path[:7] == "/share/" {
			return "/mnt/user/" + path[7:], true
		}
		return "", false
	}
	cands, _, err := CollectCandidates(context.Background(), p, p.users, []string{"m"}, allTiers(), allTiersRanked(p.users), toHost, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 || cands[0].Item.ID != "movie" {
		t.Fatalf("library scope should keep only the Movies item, got %+v", cands)
	}
}

func TestCollectCandidatesTierDials(t *testing.T) {
	p := &stubProvider{
		users:  []core.User{{ID: "1", Name: "jesse"}},
		resume: map[string][]core.MediaItem{"1": {{ID: "r1"}}},
		nextUp: map[string][]core.MediaItem{"1": {{ID: "n1"}}},
		latest: map[string][]core.MediaItem{"1": {{ID: "l1"}, {ID: "l2"}, {ID: "l3"}}},
	}
	// Disable next-up entirely; cap recently-added to 2; keep resume on. The
	// order carries enablement (next-up is absent from it); the dials carry caps.
	tiers := config.TiersConfig{
		Resume:        config.TierDial{Enabled: true},
		NextUp:        config.TierDial{Enabled: false},
		RecentlyAdded: config.TierDial{Enabled: true, MaxItems: 2},
		Order:         config.TierOrder{core.TierResume, core.TierRecentlyAdded},
	}
	ranks := ResolveRanks(&config.Config{Tiers: tiers}, p.users, discardLog())
	cands, _, err := CollectCandidates(context.Background(), p, p.users, nil, tiers, ranks, nil, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	ids := map[string]core.Tier{}
	for _, c := range cands {
		ids[c.Item.ID] = c.Tier
	}
	if _, ok := ids["n1"]; ok {
		t.Error("disabled next-up tier should contribute no candidates")
	}
	if _, ok := ids["r1"]; !ok {
		t.Error("enabled resume tier should contribute")
	}
	// recently-added capped at 2: l1,l2 kept, l3 dropped.
	if _, ok := ids["l3"]; ok {
		t.Error("recently-added cap of 2 should drop the third item")
	}
	if _, ok := ids["l1"]; !ok {
		t.Error("recently-added cap of 2 should keep the first item")
	}
	if len(cands) != 3 { // r1 + l1 + l2
		t.Errorf("expected 3 candidates (resume 1 + recently-added 2), got %d", len(cands))
	}
}

func TestCollectCandidatesSkipsTierDisabledForOneUser(t *testing.T) {
	// Alice keeps next-up, Bob disabled it. Bob's NextUp endpoint must not be
	// called at all - the saved API call is behavior, not an optimization.
	p := &stubProvider{
		users: []core.User{{ID: "id-a", Name: "Alice"}, {ID: "id-b", Name: "Bob"}},
	}
	opts := scorer.RankOpts{
		TierRank: map[string]map[core.Tier]int{
			"id-a": {core.TierResume: 0, core.TierNextUp: 1},
			"id-b": {core.TierResume: 0},
		},
		UserRank: map[string]int{"id-a": 0, "id-b": 1},
	}
	_, _, err := CollectCandidates(context.Background(), p, p.users, nil, config.TiersConfig{}, opts, nil, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.nextUpCalls; !slices.Equal(got, []string{"id-a"}) {
		t.Fatalf("NextUp called for %v, want only [id-a]", got)
	}
	if got := p.resumeCalls; !slices.Equal(got, []string{"id-a", "id-b"}) {
		t.Fatalf("Resume called for %v, want both users", got)
	}
}

func TestCollectCandidatesSkipsUnenrolledUser(t *testing.T) {
	// Bob is absent from TierRank, so he is not enrolled: no tier of his is
	// fetched at all.
	p := &stubProvider{
		users: []core.User{{ID: "id-a", Name: "Alice"}, {ID: "id-b", Name: "Bob"}},
	}
	opts := scorer.RankOpts{
		TierRank: map[string]map[core.Tier]int{"id-a": {core.TierResume: 0}},
		UserRank: map[string]int{"id-a": 0},
	}
	_, _, err := CollectCandidates(context.Background(), p, p.users, nil, config.TiersConfig{}, opts, nil, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if got := p.resumeCalls; !slices.Equal(got, []string{"id-a"}) {
		t.Fatalf("Resume called for %v, want only [id-a]", got)
	}
	if len(p.nextUpCalls)+len(p.latestCalls) != 0 {
		t.Fatalf("unenrolled user was fetched: nextUp=%v latest=%v", p.nextUpCalls, p.latestCalls)
	}
}

func TestCollectCandidatesStampsUserID(t *testing.T) {
	// The collection loop stamps UserID rather than trusting the provider to.
	// RankOpts.slot answers an unstamped item with a bare skip, so an adapter
	// that forgot the field would warm nothing and still report a clean sweep.
	p := &stubProvider{
		users:  []core.User{{ID: "id-a", Name: "Alice"}},
		resume: map[string][]core.MediaItem{"id-a": {{ID: "r1"}}}, // UserID deliberately unset
	}
	cands, _, err := CollectCandidates(context.Background(), p, p.users, nil, allTiers(), allTiersRanked(p.users), nil, discardLog())
	if err != nil {
		t.Fatal(err)
	}
	if len(cands) != 1 {
		t.Fatalf("expected 1 candidate, got %d", len(cands))
	}
	if cands[0].Item.UserID != "id-a" {
		t.Fatalf("UserID = %q, want id-a (the loop must stamp it)", cands[0].Item.UserID)
	}
}

func replaceBackslash(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] == '\\' {
			b[i] = '/'
		}
	}
	return string(b)
}

// A Provider implementation that imports no vendor package must satisfy the
// interface and run through the pipeline. This is the property the refactor
// exists to establish (#3): before it, Provider named emby.User and
// emby.Library, so the pipeline could not be exercised without the Emby
// package in the build graph at all.
//
// HONEST LIMIT, so nobody reads more into this than it proves: emby.User is a
// type ALIAS for core.User, so the two are the same type and this would still
// compile if Provider were respelled with the emby names. It therefore does
// NOT guard against the interface regaining a vendor spelling - verified by
// mutation, which compiled clean.
//
// What actually guards that is the import graph: internal/app has no non-test
// import of internal/mediaserver/emby, and TestAppDoesNotImportAVendorPackage
// below asserts exactly that. Read the two together.
type vendorNeutralProvider struct{}

func (vendorNeutralProvider) Users(context.Context) ([]core.User, error) { return nil, nil }
func (vendorNeutralProvider) Libraries(context.Context) ([]core.Library, error) {
	return nil, nil
}
func (vendorNeutralProvider) Resume(context.Context, string) ([]core.MediaItem, error) {
	return nil, nil
}
func (vendorNeutralProvider) NextUp(context.Context, string) ([]core.MediaItem, error) {
	return nil, nil
}
func (vendorNeutralProvider) RecentlyAdded(context.Context, string) ([]core.MediaItem, error) {
	return nil, nil
}
func (vendorNeutralProvider) NowPlayingIDs(context.Context) (map[string]bool, error) {
	return nil, nil
}

var _ Provider = vendorNeutralProvider{}

func TestProviderAcceptsANonVendorImplementation(t *testing.T) {
	cands, playing, err := CollectCandidates(context.Background(), vendorNeutralProvider{},
		nil, nil, config.TiersConfig{}, scorer.RankOpts{}, nil, discardLog())
	if err != nil {
		t.Fatalf("CollectCandidates with a non-vendor provider: %v", err)
	}
	if len(cands) != 0 || len(playing) != 0 {
		t.Errorf("got %d candidates and %d playing from an empty provider, want none",
			len(cands), len(playing))
	}
}

// The pipeline must not depend on a specific media server. This reads the
// actual import graph rather than trusting a type spelling, which is what the
// alias above cannot do: `go list` reports what the package really pulls in.
//
// A second adapter (#3) is only possible while this holds. If it fails, some
// non-test file in internal/app has taken a vendor import and the interface has
// quietly re-coupled.
func TestAppDoesNotImportAVendorPackage(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "go", "list", "-deps", ".").Output()
	if err != nil {
		t.Skipf("go list unavailable: %v", err)
	}
	for _, dep := range strings.Split(string(out), "\n") {
		if strings.Contains(dep, "internal/mediaserver/") {
			t.Errorf("internal/app depends on %s; the pipeline must name only core "+
				"types so a second adapter stays possible", dep)
		}
	}
}
