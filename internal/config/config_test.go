package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// writeAndLoad writes body to a temp config.toml (prefixed with a minimal
// valid [server] block unless body already has one) and loads it, failing
// the test on error.
func writeAndLoad(t *testing.T, body string) *Config {
	t.Helper()
	c, err := load(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return c
}

// loadErr writes body to a temp config.toml (with the same minimal-server
// prefix as writeAndLoad) and loads it, returning the error for the caller to
// inspect.
func loadErr(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return load(t, body)
}

func load(t *testing.T, body string) (*Config, error) {
	t.Helper()
	full := body
	if !strings.Contains(body, "[server]") {
		full = "[server]\ntype = \"emby\"\nurl = \"http://h:8096\"\n" + body
	}
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(full), 0o600); err != nil {
		t.Fatal(err)
	}
	return Load(p)
}

const sample = `
[server]
type = "emby"
url = "http://tower:8096"

[users]
enabled = ["jesse", "rachel"]

[preload]
ram_percent = 50
target_seconds = 20

[[path_map]]
from = "/share"
to = "/mnt/user"

[schedule]
sweep_seconds = 60
session_poll_seconds = 5
`

func TestLoadValid(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.Server.URL != "http://tower:8096" {
		t.Errorf("server parsed wrong: %+v", c.Server)
	}
	if c.Preload.RAMPercent != 50 || c.Preload.TargetSeconds != 20 {
		t.Errorf("preload parsed wrong: %+v", c.Preload)
	}
	if len(c.PathMap) != 1 || c.PathMap[0].From != "/share" {
		t.Errorf("path_map parsed wrong: %+v", c.PathMap)
	}
}

func TestValidateRejectsBadPercent(t *testing.T) {
	c := &Config{}
	c.Server.Type = "emby"
	c.Server.URL = "http://h:8096"
	c.Preload.RAMPercent = 150
	if err := c.Validate(); err == nil {
		t.Error("expected error for ram_percent > 100")
	}
}

// validBase returns a Config that passes Validate, so each sub-test can flip a
// single field negative/zero and confirm it is the field under test that fails.
func validBase() *Config {
	c := &Config{}
	c.Server.Type = "emby"
	c.Server.URL = "http://h:8096"
	c.Preload.RAMPercent = 50
	c.Preload.TargetSeconds = 20
	c.Schedule.SweepSeconds = 60
	c.Schedule.SessionPollSeconds = 5
	c.Residency.ProbeBytes = 1 << 20
	c.Residency.ProbeThreshold = 150 * time.Millisecond
	return c
}

func TestValidateRejectsNegativePreloadSizes(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
	}{
		{"min_head_mb", func(c *Config) { c.Preload.MinHeadMB = -1 }},
		{"max_head_mb", func(c *Config) { c.Preload.MaxHeadMB = -1 }},
		{"tail_mb", func(c *Config) { c.Preload.TailMB = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			tc.mut(c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected error for negative %s", tc.name)
			}
		})
	}
}

func TestValidateRejectsInvertedHeadBounds(t *testing.T) {
	c := validBase()
	c.Preload.MinHeadMB = 100
	c.Preload.MaxHeadMB = 50
	if err := c.Validate(); err == nil {
		t.Error("expected error for min_head_mb > max_head_mb (min would be silently meaningless)")
	}
}

func TestValidateRejectsNonPositiveIntervals(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
	}{
		{"sweep_seconds", func(c *Config) { c.Schedule.SweepSeconds = -1 }},
		{"session_poll_seconds", func(c *Config) { c.Schedule.SessionPollSeconds = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			tc.mut(c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected error for non-positive %s (panics time.NewTicker)", tc.name)
			}
		})
	}
}

func TestResidencyDefaults(t *testing.T) {
	c := &Config{}
	c.applyDefaults(false, nil)
	if c.Residency.ProbeBytes != 1<<20 {
		t.Errorf("ProbeBytes default = %d, want %d", c.Residency.ProbeBytes, 1<<20)
	}
	if c.Residency.ProbeThreshold != 150*time.Millisecond {
		t.Errorf("ProbeThreshold default = %v, want 150ms", c.Residency.ProbeThreshold)
	}
	if c.Residency.ProbeTimeout != 30*time.Second {
		t.Errorf("ProbeTimeout default = %v, want 30s", c.Residency.ProbeTimeout)
	}
}

func TestTierDefaultsNoDialsIsFullOrderNoCap(t *testing.T) {
	// No [tiers] block: the full default order (the thing the engine reads), and
	// no max-items cap.
	c := &Config{}
	c.applyDefaults(false, nil)
	if !reflect.DeepEqual(c.Tiers.Order, DefaultTierOrder()) {
		t.Errorf("order = %v, want the full default %v", c.Tiers.Order, DefaultTierOrder())
	}
	if c.Tiers.RecentlyAdded.MaxItems != 0 {
		t.Errorf("default MaxItems should be 0 (no cap), got %d", c.Tiers.RecentlyAdded.MaxItems)
	}
}

func TestTierExplicitConfigPreserved(t *testing.T) {
	// An operator who states every legacy dial gets exactly the enabled ones, and
	// their max_items survives defaulting.
	c := &Config{Tiers: TiersConfig{
		Resume:        TierDial{Enabled: true, MaxItems: 5},
		NextUp:        TierDial{Enabled: false},
		RecentlyAdded: TierDial{Enabled: true},
	}}
	c.applyDefaults(false, map[core.Tier]bool{
		core.TierResume:        true,
		core.TierNextUp:        false,
		core.TierRecentlyAdded: true,
	})
	if want := (TierOrder{core.TierResume, core.TierRecentlyAdded}); !reflect.DeepEqual(c.Tiers.Order, want) {
		t.Errorf("order = %v, want %v (explicitly disabled next-up dropped)", c.Tiers.Order, want)
	}
	if c.Tiers.Resume.MaxItems != 5 {
		t.Errorf("explicit resume MaxItems clobbered, got %d", c.Tiers.Resume.MaxItems)
	}
}

func TestTierAllDisabledHonoredViaLoad(t *testing.T) {
	// An operator disables every tier. The decoded TiersConfig is all-zero and
	// indistinguishable from an unset one, so the derivation reads flag PRESENCE
	// from metadata to honor this rather than resolve the full default order.
	const body = `
[server]
type = "emby"
url = "http://h:8096"
[tiers.resume]
enabled = false
[tiers.next_up]
enabled = false
[tiers.recently_added]
enabled = false
`
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Tiers.Order) != 0 {
		t.Errorf("every tier explicitly disabled must resolve to an empty order, got %v", c.Tiers.Order)
	}
}

func TestTierNoBlockFullOrderViaLoad(t *testing.T) {
	// No [tiers] block in the sample config: the full default order, no cap.
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(c.Tiers.Order, DefaultTierOrder()) {
		t.Errorf("no [tiers] block should resolve to the full default order, got %v", c.Tiers.Order)
	}
}

func TestDefaultTailMBIsFallback16(t *testing.T) {
	var c Config
	c.applyDefaults(false, nil)
	if c.Preload.TailMB != 16 {
		t.Errorf("default TailMB = %d, want 16 (flat fallback)", c.Preload.TailMB)
	}
}

func TestValidateProbeTimeout(t *testing.T) {
	// A positive value below the 15s floor is rejected (it could abort a
	// legitimate cold read); a negative value is accepted (disables the guard);
	// a value >= 15s is accepted.
	for _, tc := range []struct {
		name    string
		timeout time.Duration
		wantErr bool
	}{
		{"positive too small", 5 * time.Second, true},
		{"negative disables", -1 * time.Second, false},
		{"at floor", 15 * time.Second, false},
		{"above floor", 30 * time.Second, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			c.Residency.ProbeTimeout = tc.timeout
			err := c.Validate()
			if tc.wantErr && err == nil {
				t.Errorf("expected validation error for probe_timeout %v", tc.timeout)
			}
			if !tc.wantErr && err != nil {
				t.Errorf("unexpected error for probe_timeout %v: %v", tc.timeout, err)
			}
		})
	}
}

func TestResidencyDecodesDurationString(t *testing.T) {
	const data = `
[server]
type = "emby"
url = "http://localhost:8096"

[residency]
probe_bytes = 2097152
probe_threshold = "200ms"
`
	dir := t.TempDir()
	p := filepath.Join(dir, "config.toml")
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if c.Residency.ProbeBytes != 2<<20 {
		t.Errorf("ProbeBytes = %d, want %d", c.Residency.ProbeBytes, 2<<20)
	}
	if c.Residency.ProbeThreshold != 200*time.Millisecond {
		t.Errorf("ProbeThreshold = %v, want 200ms", c.Residency.ProbeThreshold)
	}
}

func TestLoadProbeTimeout(t *testing.T) {
	const head = `
[server]
type = "emby"
url = "http://localhost:8096"

[residency]
`
	for _, tc := range []struct {
		name string
		body string // extra line(s) under [residency]
		want time.Duration
	}{
		{"explicit 20s", `probe_timeout = "20s"`, 20 * time.Second},
		{"unset defaults to 30s", ``, 30 * time.Second},
		{"negative disables", `probe_timeout = "-1s"`, -1 * time.Second},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "config.toml")
			if err := os.WriteFile(p, []byte(head+tc.body+"\n"), 0o600); err != nil {
				t.Fatal(err)
			}
			c, err := Load(p)
			if err != nil {
				t.Fatalf("Load: %v", err)
			}
			if c.Residency.ProbeTimeout != tc.want {
				t.Errorf("ProbeTimeout = %v, want %v", c.Residency.ProbeTimeout, tc.want)
			}
		})
	}
}

func TestResidencyRejectsNonPositive(t *testing.T) {
	for _, tc := range []struct {
		name string
		mut  func(*Config)
	}{
		{"probe_bytes <= 0", func(c *Config) { c.Residency.ProbeBytes = -1 }},
		{"probe_threshold <= 0", func(c *Config) { c.Residency.ProbeThreshold = -1 }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := validBase()
			tc.mut(c)
			if err := c.Validate(); err == nil {
				t.Errorf("expected validation error for %s", tc.name)
			}
		})
	}
}

func TestStatusPathDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.StatusPath != "/var/local/preloadd/status.json" {
		t.Errorf("StatusPath default = %q, want /var/local/preloadd/status.json", c.StatusPath)
	}
}

func TestStatusPathOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	// Prepend at the root: a bare key AFTER a [table] header would bind to
	// that table (schedule.status_path), not the root status_path.
	body := "status_path = \"/tmp/custom/status.json\"\n" + sample
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.StatusPath != "/tmp/custom/status.json" {
		t.Errorf("StatusPath = %q, want /tmp/custom/status.json", c.StatusPath)
	}
}

func TestLoadRejectsAPIKeyInConfig(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	body := "[server]\ntype = \"emby\"\nurl = \"http://h:8096\"\napi_key = \"leaked\"\n"
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Load(p)
	if err == nil {
		t.Fatal("expected error: api_key must not be in config.toml")
	}
	if !strings.Contains(err.Error(), "server.api_key must not be in config.toml") {
		t.Errorf("error = %q, want it to mention server.api_key must not be in config.toml", err)
	}
}

func TestSecretPathDefault(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	if err := os.WriteFile(p, []byte(sample), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.SecretPath != "/boot/config/plugins/watch-aware-preloader/secrets.toml" {
		t.Errorf("SecretPath default = %q", c.SecretPath)
	}
}

// TestTierOrderResolution is the single table of record for how a config file
// resolves to c.Tiers.Order. Every row runs through the real Load.
func TestTierOrderResolution(t *testing.T) {
	full := TierOrder{core.TierResume, core.TierNextUp, core.TierRecentlyAdded}
	for _, tc := range []struct {
		name string
		body string
		want TierOrder
	}{
		// Legacy dials, allow-list reading: any explicit enabled = true makes the
		// explicitly-true set the whole order. An operator who opts one tier IN is
		// not asking for the other two.
		{"legacy single true is an allow-list", "[tiers.resume]\nenabled = true\n",
			TierOrder{core.TierResume}},
		{"legacy mixed: an explicit true wins over a false",
			"[tiers.resume]\nenabled = true\n[tiers.next_up]\nenabled = false\n",
			TierOrder{core.TierResume}},
		{"legacy all true is the full order",
			"[tiers.resume]\nenabled = true\n[tiers.next_up]\nenabled = true\n[tiers.recently_added]\nenabled = true\n",
			full},
		// Legacy dials, deny-list reading: with only false values stated, only the
		// stated tiers drop out. Note this is a deliberate DEPARTURE from the
		// pre-order loader, which decoded an unstated `enabled` to false once
		// [tiers] existed and so warmed nothing here (see deriveLegacyOrder).
		{"legacy single false is a deny-list", "[tiers.next_up]\nenabled = false\n",
			TierOrder{core.TierResume, core.TierRecentlyAdded}},
		{"legacy all false warms nothing",
			"[tiers.resume]\nenabled = false\n[tiers.next_up]\nenabled = false\n[tiers.recently_added]\nenabled = false\n",
			TierOrder{}},
		// No legacy enabled key defined at all: the derivation must not run, or it
		// would filter against all-false dials and silently warm nothing.
		{"new-shape override only", "[tiers.override]\nbob = [\"resume\"]\n", full},
		{"new-shape max_items only", "[tiers.resume]\nmax_items = 5\n", full},
		{"no [tiers] block at all", ``, full},
		// An explicit order always wins, including the empty "warm nothing" order.
		{"explicit empty order is legal", "[tiers]\norder = []\n", TierOrder{}},
		{"explicit order is verbatim", "[tiers]\norder = [\"nextup\", \"resume\"]\n",
			TierOrder{core.TierNextUp, core.TierResume}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := writeAndLoad(t, tc.body)
			if !reflect.DeepEqual(c.Tiers.Order, tc.want) {
				t.Fatalf("order = %v, want %v", c.Tiers.Order, tc.want)
			}
		})
	}
}

func TestTierOrderExplicitAndOverride(t *testing.T) {
	c := writeAndLoad(t, `
[tiers]
order = ["next_up", "resume"]
[tiers.override]
bob = ["resume"]
`)
	if want := (TierOrder{core.TierNextUp, core.TierResume}); !reflect.DeepEqual(c.Tiers.Order, want) {
		t.Fatalf("order = %v, want %v", c.Tiers.Order, want)
	}
	if want := (TierOrder{core.TierResume}); !reflect.DeepEqual(c.Tiers.Override["bob"], want) {
		t.Fatalf("override[bob] = %v, want %v", c.Tiers.Override["bob"], want)
	}
	if _, ok := c.Tiers.Override["alice"]; ok {
		t.Fatal("alice must have no override entry (inheritance is by absence)")
	}
}

func TestTierOrderRejectsBadInput(t *testing.T) {
	for name, body := range map[string]string{
		"unknown tier":   "[tiers]\norder = [\"bogus\"]\n",
		"duplicate tier": "[tiers]\norder = [\"resume\", \"resume\"]\n",
		"bad override":   "[tiers.override]\nbob = [\"bogus\"]\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := loadErr(t, body); err == nil {
				t.Fatal("want error, got nil")
			}
		})
	}
}

func TestSecretPathOverride(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	body := "secret_path = \"/tmp/x/secrets.toml\"\n" + sample
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	c, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if c.SecretPath != "/tmp/x/secrets.toml" {
		t.Errorf("SecretPath = %q, want /tmp/x/secrets.toml", c.SecretPath)
	}
}
