package app

import (
	"bytes"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// captureLog returns a logger writing into buf, for asserting on warnings.
func captureLog(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewTextHandler(buf, nil))
}

var testUsers = []core.User{
	{ID: "id-a", Name: "Alice"},
	{ID: "id-b", Name: "Bob"},
	{ID: "id-c", Name: "Cara"},
}

func TestResolveRanksUsesConfigOrderNotServerOrder(t *testing.T) {
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"Cara", "Alice"} // deliberately not server order
	cfg.Tiers.Order = config.DefaultTierOrder()

	got := ResolveRanks(cfg, testUsers, discardLog())

	if got.UserRank["id-c"] != 0 || got.UserRank["id-a"] != 1 {
		t.Fatalf("UserRank = %v, want cara=0 alice=1", got.UserRank)
	}
	if _, ok := got.TierRank["id-b"]; ok {
		t.Fatal("bob is not enrolled and must contribute nothing")
	}
}

func TestResolveRanksEmptyEnabledIsAllUsersEqualRank(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()

	got := ResolveRanks(cfg, testUsers, discardLog())

	for _, id := range []string{"id-a", "id-b", "id-c"} {
		if got.UserRank[id] != 0 {
			t.Fatalf("UserRank[%s] = %d, want 0 (equal rank)", id, got.UserRank[id])
		}
		if len(got.TierRank[id]) != 3 {
			t.Fatalf("TierRank[%s] = %v, want all three tiers", id, got.TierRank[id])
		}
	}
}

func TestResolveRanksOverrideBindsByNameOrID(t *testing.T) {
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"id-a", "Bob"}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{
		"Alice": {core.TierNextUp},                  // by display name
		"id-b":  {core.TierResume, core.TierNextUp}, // by ID
	}

	got := ResolveRanks(cfg, testUsers, discardLog())

	if want := (map[core.Tier]int{core.TierNextUp: 0}); !reflect.DeepEqual(got.TierRank["id-a"], want) {
		t.Fatalf("TierRank[id-a] = %v, want %v", got.TierRank["id-a"], want)
	}
	if want := (map[core.Tier]int{core.TierResume: 0, core.TierNextUp: 1}); !reflect.DeepEqual(got.TierRank["id-b"], want) {
		t.Fatalf("TierRank[id-b] = %v, want %v", got.TierRank["id-b"], want)
	}
}

func TestResolveRanksInheritsByAbsence(t *testing.T) {
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"Alice", "Bob"}
	cfg.Tiers.Order = config.TierOrder{core.TierResume, core.TierNextUp}
	cfg.Tiers.Override = map[string]config.TierOrder{"Bob": {core.TierNextUp}}

	got := ResolveRanks(cfg, testUsers, discardLog())

	want := map[core.Tier]int{core.TierResume: 0, core.TierNextUp: 1}
	if !reflect.DeepEqual(got.TierRank["id-a"], want) {
		t.Fatalf("alice TierRank = %v, want the global %v", got.TierRank["id-a"], want)
	}
}

func TestResolveRanksDuplicateUserKeepsFirstRank(t *testing.T) {
	// Alice listed twice, by name and by ID. She must keep her first (best) rank,
	// and the duplicate must not perturb the users listed after her.
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"Alice", "id-a", "Bob"}
	cfg.Tiers.Order = config.DefaultTierOrder()

	got := ResolveRanks(cfg, testUsers, discardLog())

	if got.UserRank["id-a"] != 0 {
		t.Fatalf("UserRank[id-a] = %d, want 0 (first rank retained)", got.UserRank["id-a"])
	}
	if got.UserRank["id-b"] != 1 {
		t.Fatalf("UserRank[id-b] = %d, want 1 (duplicate must not collide)", got.UserRank["id-b"])
	}
	if len(got.UserRank) != 2 {
		t.Fatalf("UserRank = %v, want exactly alice and bob", got.UserRank)
	}
}

func TestResolveRanksUnknownEnabledUserIgnored(t *testing.T) {
	// An enabled entry matching no known user is skipped, and must not consume a
	// rank from the users that follow it.
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"Ghost", "Bob"}
	cfg.Tiers.Order = config.DefaultTierOrder()

	got := ResolveRanks(cfg, testUsers, discardLog())

	if len(got.UserRank) != 1 {
		t.Fatalf("UserRank = %v, want only bob", got.UserRank)
	}
	if got.UserRank["id-b"] != 0 {
		t.Fatalf("UserRank[id-b] = %d, want 0 (the skipped ghost must not consume a rank)", got.UserRank["id-b"])
	}
}

func TestResolveRanksEmptyResolvedOrderStaysEnrolled(t *testing.T) {
	// An empty order is legal ("warm nothing") and only ever explicit, so the user
	// stays enrolled with an empty order and the sweep says nothing about it.
	for _, tc := range []struct {
		name string
		mut  func(*config.Config)
	}{
		{"global order empty", func(c *config.Config) {
			c.Tiers.Order = config.TierOrder{}
		}},
		{"override empty", func(c *config.Config) {
			c.Tiers.Order = config.DefaultTierOrder()
			c.Tiers.Override = map[string]config.TierOrder{"Alice": {}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := &config.Config{}
			cfg.Users.Enabled = []string{"Alice"}
			tc.mut(cfg)

			var buf bytes.Buffer
			got := ResolveRanks(cfg, testUsers, captureLog(&buf))

			if _, ok := got.TierRank["id-a"]; !ok {
				t.Fatal("alice must stay enrolled: an empty order is legal, not an error")
			}
			if len(got.TierRank["id-a"]) != 0 {
				t.Fatalf("TierRank[id-a] = %v, want empty", got.TierRank["id-a"])
			}
			if buf.Len() != 0 {
				t.Fatalf("an explicit warm-nothing order must log nothing, got %q", buf.String())
			}
		})
	}
}

func TestResolveRanksUnknownOverrideIgnored(t *testing.T) {
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"Alice"}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{"Ghost": {core.TierResume}}

	got := ResolveRanks(cfg, testUsers, discardLog()) // must not panic

	if len(got.TierRank) != 1 {
		t.Fatalf("TierRank = %v, want only alice", got.TierRank)
	}
}

// dupNameUsers has two users sharing the display name "Alice", so a name-keyed
// reference to her is ambiguous while an ID-keyed one is not.
var dupNameUsers = []core.User{
	{ID: "id-a", Name: "Alice"},
	{ID: "id-d", Name: "Alice"},
	{ID: "id-b", Name: "Bob"},
}

func TestResolveRanksAmbiguousNameIsRejected(t *testing.T) {
	// Two users named Alice: enrolling "Alice" must resolve to NEITHER. Picking
	// the first would bind enrollment to the server's arbitrary list order and
	// warm the wrong person's media.
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"Alice", "Bob"}
	cfg.Tiers.Order = config.DefaultTierOrder()

	var buf bytes.Buffer
	got := ResolveRanks(cfg, dupNameUsers, captureLog(&buf))

	if _, ok := got.UserRank["id-a"]; ok {
		t.Error("id-a enrolled from an ambiguous name")
	}
	if _, ok := got.UserRank["id-d"]; ok {
		t.Error("id-d enrolled from an ambiguous name")
	}
	if got.UserRank["id-b"] != 0 {
		t.Errorf("UserRank[id-b] = %d, want 0 (the skip must not consume a rank)", got.UserRank["id-b"])
	}
	if !strings.Contains(buf.String(), "matches several users") {
		t.Errorf("expected an ambiguity warning, got: %s", buf.String())
	}
}

func TestResolveRanksExactIDResolvesDespiteDuplicateNames(t *testing.T) {
	// The operator disambiguates with an ID; that must still work.
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"id-d"}
	cfg.Tiers.Order = config.DefaultTierOrder()

	got := ResolveRanks(cfg, dupNameUsers, discardLog())

	if got.UserRank["id-d"] != 0 || len(got.UserRank) != 1 {
		t.Fatalf("UserRank = %v, want only id-d at rank 0", got.UserRank)
	}
}

func TestResolveRanksOverrideExactIDBeatsNameAlias(t *testing.T) {
	// "Alice" and "id-a" are two override keys for ONE user. Override is a map, so
	// applying them in iteration order would let Go's randomized ordering decide
	// the winner. The exact ID must win every time.
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"id-a"}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{
		"Alice": {core.TierNextUp},
		"id-a":  {core.TierResume},
	}

	want := map[core.Tier]int{core.TierResume: 0}
	// Repeated because the defect this guards is map-iteration nondeterminism: a
	// single pass could pass by luck.
	for i := 0; i < 50; i++ {
		got := ResolveRanks(cfg, testUsers, discardLog())
		if !reflect.DeepEqual(got.TierRank["id-a"], want) {
			t.Fatalf("run %d: TierRank[id-a] = %v, want %v (exact ID wins)", i, got.TierRank["id-a"], want)
		}
	}
}

func TestResolveRanksOverrideAmbiguousNameIgnored(t *testing.T) {
	cfg := &config.Config{}
	cfg.Users.Enabled = []string{"id-a"}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{"Alice": {core.TierNextUp}}

	var buf bytes.Buffer
	got := ResolveRanks(cfg, dupNameUsers, captureLog(&buf))

	// id-a falls back to the global order rather than taking the ambiguous override.
	want := map[core.Tier]int{core.TierResume: 0, core.TierNextUp: 1, core.TierRecentlyAdded: 2}
	if !reflect.DeepEqual(got.TierRank["id-a"], want) {
		t.Fatalf("TierRank[id-a] = %v, want the global order %v", got.TierRank["id-a"], want)
	}
	if !strings.Contains(buf.String(), "ambiguous user name") {
		t.Errorf("expected an ambiguity warning, got: %s", buf.String())
	}
}

// An override key may arrive in its .cfg-key spelling (dashes as underscores),
// because rc.preloadd can only recover the dashed id for a user named in the
// enabled list - and that list is empty in the "all users" default, which is the
// shipped default.cfg value. Before this bound, every override saved in that
// state was silently dropped.
func TestResolveRanksOverrideBindsByCfgKeySpelling(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{
		"id_a": {core.TierRecentlyAdded}, // id-a in .cfg-key spelling
	}

	got := ResolveRanks(cfg, testUsers, discardLog())

	want := map[core.Tier]int{core.TierRecentlyAdded: 0}
	if !reflect.DeepEqual(got.TierRank["id-a"], want) {
		t.Fatalf("TierRank[id-a] = %v, want %v", got.TierRank["id-a"], want)
	}
	// Everyone else still inherits the global order.
	if len(got.TierRank["id-b"]) != len(config.DefaultTierOrder()) {
		t.Fatalf("TierRank[id-b] = %v, want the global order", got.TierRank["id-b"])
	}
}

// The .cfg-key tier is a LAST resort: an exact ID must outrank it, or a config
// carrying both spellings would resolve by map iteration order.
func TestResolveRanksOverrideExactIDBeatsCfgKeySpelling(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{
		"id-a": {core.TierResume},        // exact id
		"id_a": {core.TierRecentlyAdded}, // .cfg-key spelling of the same user
	}

	for i := 0; i < 20; i++ { // map order is randomized; the winner must not be
		got := ResolveRanks(cfg, testUsers, discardLog())
		want := map[core.Tier]int{core.TierResume: 0}
		if !reflect.DeepEqual(got.TierRank["id-a"], want) {
			t.Fatalf("TierRank[id-a] = %v, want the exact-id order %v", got.TierRank["id-a"], want)
		}
	}
}

// Two ids that differ only by '-' vs '_' collide under the .cfg-key transform.
// Each is still its own EXACT id, so exact-match precedence must bind each key to
// its own user with no bleed in either direction.
func TestResolveRanksCfgKeyCollisionBindsExactIDsOnly(t *testing.T) {
	users := []core.User{{ID: "x-y", Name: "Xavier"}, {ID: "x_y", Name: "Yvonne"}}
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{"x-y": {core.TierResume}}

	var buf bytes.Buffer
	got := ResolveRanks(cfg, users, captureLog(&buf))

	// "x-y" is an EXACT id, so it binds; "x_y" must NOT inherit it.
	if !reflect.DeepEqual(got.TierRank["x-y"], map[core.Tier]int{core.TierResume: 0}) {
		t.Fatalf("TierRank[x-y] = %v, want the exact-id override", got.TierRank["x-y"])
	}
	if len(got.TierRank["x_y"]) != len(config.DefaultTierOrder()) {
		t.Fatalf("TierRank[x_y] = %v, want the global order (no bleed)", got.TierRank["x_y"])
	}

	// Now the ambiguous direction: only the .cfg-key spelling is configured.
	cfg.Tiers.Override = map[string]config.TierOrder{"x_y": {core.TierResume}}
	buf.Reset()
	got = ResolveRanks(cfg, users, captureLog(&buf))
	// "x_y" is Yvonne's EXACT id, so it must bind to her and not to Xavier.
	if !reflect.DeepEqual(got.TierRank["x_y"], map[core.Tier]int{core.TierResume: 0}) {
		t.Fatalf("TierRank[x_y] = %v, want the exact-id override", got.TierRank["x_y"])
	}
	if len(got.TierRank["x-y"]) != len(config.DefaultTierOrder()) {
		t.Fatalf("TierRank[x-y] = %v, want the global order (no bleed)", got.TierRank["x-y"])
	}
}

// A key that matches nothing exactly but normalizes onto TWO different users is
// genuinely ambiguous. Binding it to either would warm one household member's
// media under another's override, so it is refused and warned - the same posture
// as an ambiguous display name.
func TestResolveRanksCfgKeyAmbiguityRefused(t *testing.T) {
	// "a_b" is neither user's exact id nor exact name, yet normalizes onto both:
	// via id "a-b" for one and via display name "a-b" for the other.
	users := []core.User{
		{ID: "a-b", Name: "Ann"},
		{ID: "id-z", Name: "a-b"},
	}
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{"a_b": {core.TierResume}}

	var buf bytes.Buffer
	got := ResolveRanks(cfg, users, captureLog(&buf))

	for _, id := range []string{"a-b", "id-z"} {
		if len(got.TierRank[id]) != len(config.DefaultTierOrder()) {
			t.Fatalf("TierRank[%s] = %v, want the global order (ambiguous override refused)", id, got.TierRank[id])
		}
	}
	if !strings.Contains(buf.String(), "ambiguous") {
		t.Fatalf("expected an ambiguity warning, got: %s", buf.String())
	}
}

// A key can be one user's EXACT display name and ANOTHER user's .cfg-key
// spelling at the same time. Both readings are plausible, so the conflict must be
// refused: resolving it by whichever tier is checked first handed a bystander
// another user's override silently. This is the axis a mutation that reordered
// the two tiers slipped through, so it is pinned explicitly.
func TestResolveRanksNameVsCfgKeyConflictRefused(t *testing.T) {
	users := []core.User{
		{ID: "id-a", Name: "bob_smith"}, // exact-name match for the key
		{ID: "bob-smith", Name: "Bob"},  // .cfg-key-spelling match for the key
	}
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{"bob_smith": {core.TierResume}}

	var buf bytes.Buffer
	got := ResolveRanks(cfg, users, captureLog(&buf))

	for _, id := range []string{"id-a", "bob-smith"} {
		if len(got.TierRank[id]) != len(config.DefaultTierOrder()) {
			t.Fatalf("TierRank[%s] = %v, want the global order (conflict refused)", id, got.TierRank[id])
		}
	}
	if !strings.Contains(buf.String(), "ambiguous") {
		t.Fatalf("expected an ambiguity warning, got: %s", buf.String())
	}
}

// The ordinary unique-name match must still resolve: a name normalizes onto
// itself, so its own keyed entry is not a conflict. Without this the guard above
// would refuse every name-keyed override.
func TestResolveRanksUniqueNameStillResolves(t *testing.T) {
	cfg := &config.Config{}
	cfg.Tiers.Order = config.DefaultTierOrder()
	cfg.Tiers.Override = map[string]config.TierOrder{"Alice": {core.TierNextUp}}

	got := ResolveRanks(cfg, testUsers, discardLog())

	want := map[core.Tier]int{core.TierNextUp: 0}
	if !reflect.DeepEqual(got.TierRank["id-a"], want) {
		t.Fatalf("TierRank[id-a] = %v, want %v", got.TierRank["id-a"], want)
	}
}
