// Package config loads and validates the preloadd TOML configuration.
package config

import (
	"fmt"
	"time"

	"github.com/BurntSushi/toml"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// ServerConfig holds the media-server connection parameters.
type ServerConfig struct {
	Type string `toml:"type"` // "emby" (Phase 1)
	URL  string `toml:"url"`
}

// UsersConfig specifies which users to preload for.
type UsersConfig struct {
	// Enabled entries are a user ID or a display name. List ORDER is the user
	// rank: earlier = higher priority, breaking ties within a slot. Empty => all
	// users at equal rank.
	Enabled []string `toml:"enabled"`
}

// LibrariesConfig scopes preloading to specific media libraries.
type LibrariesConfig struct {
	Enabled []string `toml:"enabled"` // library IDs; empty => all libraries
}

// TierDial controls one preload signal tier. MaxItems caps how many items the
// tier contributes per user (0 = no cap).
type TierDial struct {
	Enabled  bool `toml:"enabled"`
	MaxItems int  `toml:"max_items"`
}

// TierOrder is an explicit priority order over signal tiers. Position in the
// slice is priority; a tier absent from the slice is disabled for that scope.
// An empty (non-nil) order means "warm nothing", which is legal.
type TierOrder []core.Tier

// tierNames maps the TOML/cfg spelling of a tier to its Tier value. It is the
// single source of truth for tier spelling across config, rc.preloadd render,
// and presave validation.
var tierNames = map[string]core.Tier{
	"resume":         core.TierResume,
	"next_up":        core.TierNextUp,
	"recently_added": core.TierRecentlyAdded,
}

// ParseTierName resolves a tier's config spelling. It also accepts the flat
// .cfg spellings ("nextup", "recent") that rc.preloadd emits, so a
// hand-edited config using either form loads.
func ParseTierName(s string) (core.Tier, bool) {
	switch s {
	case "nextup":
		return core.TierNextUp, true
	case "recent":
		return core.TierRecentlyAdded, true
	}
	t, ok := tierNames[s]
	return t, ok
}

// DefaultTierOrder is the order used when no [tiers] order is configured. It
// matches the pre-order hardcoded behavior.
func DefaultTierOrder() TierOrder {
	return TierOrder{core.TierResume, core.TierNextUp, core.TierRecentlyAdded}
}

// UnmarshalTOML decodes a list of tier names into a TierOrder. Decoding rejects
// unknown names here rather than in Validate so the error names the offending
// value while the TOML position is still known.
func (o *TierOrder) UnmarshalTOML(v any) error {
	raw, ok := v.([]any)
	if !ok {
		return fmt.Errorf("tier order must be a list of tier names, got %T", v)
	}
	out := make(TierOrder, 0, len(raw))
	for _, e := range raw {
		s, ok := e.(string)
		if !ok {
			return fmt.Errorf("tier order entries must be strings, got %T", e)
		}
		t, ok := ParseTierName(s)
		if !ok {
			return fmt.Errorf("unknown tier %q in order (want resume, next_up, or recently_added)", s)
		}
		out = append(out, t)
	}
	*o = out
	return nil
}

// TiersConfig holds the per-signal dials. The zero value (no [tiers] block)
// resolves to the full default order with no cap, preserving the pre-dials
// behavior; applyDefaults fills the order in.
type TiersConfig struct {
	// Dials keep their existing max_items semantics and TOML shape. Enabled is
	// superseded by Order (a tier is enabled iff it appears in the resolved
	// order) and is retained only as the legacy migration input.
	Resume        TierDial `toml:"resume"`
	NextUp        TierDial `toml:"next_up"`
	RecentlyAdded TierDial `toml:"recently_added"`

	// Order is the household-wide priority order.
	Order TierOrder `toml:"order"`
	// Override maps a user (ID or display name, as configured) to that user's
	// own order. Inheritance is by ABSENCE: a user with no entry follows Order.
	// Never populate this with copies of Order.
	Override map[string]TierOrder `toml:"override"`
}

// Dial returns the max-items dial for a tier. Unknown tiers get the zero dial
// (no cap), which is the correct default for the reserved Phase 3 tiers.
func (t TiersConfig) Dial(tier core.Tier) TierDial {
	switch tier {
	case core.TierResume:
		return t.Resume
	case core.TierNextUp:
		return t.NextUp
	case core.TierRecentlyAdded:
		return t.RecentlyAdded
	default:
		return TierDial{}
	}
}

// PreloadConfig controls the preload budget and read-ahead sizes.
type PreloadConfig struct {
	RAMPercent    int   `toml:"ram_percent"`
	TargetSeconds int   `toml:"target_seconds"`
	MinHeadMB     int64 `toml:"min_head_mb"`
	MaxHeadMB     int64 `toml:"max_head_mb"`
	TailMB        int64 `toml:"tail_mb"`
}

// PathRule maps a media-server path prefix to the equivalent host path.
type PathRule struct {
	From string `toml:"from"`
	To   string `toml:"to"`
}

// ScheduleConfig controls polling and sweep intervals.
type ScheduleConfig struct {
	SweepSeconds       int `toml:"sweep_seconds"`
	SessionPollSeconds int `toml:"session_poll_seconds"`
}

// ResidencyConfig controls the read-timing probe used to detect page-cache
// residency on filesystems where mincore cannot (e.g. Unraid fuse.shfs).
type ResidencyConfig struct {
	ProbeBytes     int64         `toml:"probe_bytes"`     // fixed probe sample size
	ProbeThreshold time.Duration `toml:"probe_threshold"` // cached iff a probe read returns faster than this
	// ProbeTimeout is a generous deadline for a single probe read. Unset/0 ->
	// 30s default. A negative value disables the guard (unbounded, pre-#17
	// behavior). A positive value must be >= 15s (above the array spin-up
	// window) so it never aborts a legitimate cold read.
	ProbeTimeout time.Duration `toml:"probe_timeout"`
}

// Config is the full preloadd configuration.
type Config struct {
	Server     ServerConfig    `toml:"server"`
	Users      UsersConfig     `toml:"users"`
	Libraries  LibrariesConfig `toml:"libraries"`
	Tiers      TiersConfig     `toml:"tiers"`
	Preload    PreloadConfig   `toml:"preload"`
	PathMap    []PathRule      `toml:"path_map"`
	Schedule   ScheduleConfig  `toml:"schedule"`
	Residency  ResidencyConfig `toml:"residency"`
	StatusPath string          `toml:"status_path"` // where the engine writes status.json
	SecretPath string          `toml:"secret_path"` // where the engine reads the secrets file
}

// Load decodes a TOML config file, applies defaults, and validates it.
func Load(path string) (*Config, error) {
	var c Config
	md, err := toml.DecodeFile(path, &c)
	if err != nil {
		return nil, fmt.Errorf("decoding config: %w", err)
	}
	c.applyDefaults(md.IsDefined("tiers", "order"), legacyEnabledFlags(md, c.Tiers))
	for _, key := range md.Undecoded() {
		if key.String() == "server.api_key" {
			return nil, fmt.Errorf("server.api_key must not be in config.toml; move it to the secrets file (%s) or the EMBY_API_KEY env var", c.SecretPath)
		}
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// legacyEnabledFlags reports each tier whose legacy `enabled` flag is actually
// present in the TOML, mapped to its decoded value. Only a tier listed here has
// an operator-stated opinion; a tier absent from the map has none. Presence is
// read from metadata, not the decoded struct, so an explicit `enabled = false`
// is distinguishable from an absent key.
func legacyEnabledFlags(md toml.MetaData, t TiersConfig) map[core.Tier]bool {
	flags := make(map[core.Tier]bool, len(tierNames))
	for name, tier := range tierNames {
		if md.IsDefined("tiers", name, "enabled") {
			flags[tier] = t.Dial(tier).Enabled
		}
	}
	return flags
}

// applyDefaults fills unset fields. orderDefined reports whether [tiers] order
// is present in the TOML; legacyEnabled carries only the tiers whose legacy
// `enabled` flag is present (see legacyEnabledFlags).
func (c *Config) applyDefaults(orderDefined bool, legacyEnabled map[core.Tier]bool) {
	if c.Preload.RAMPercent == 0 {
		c.Preload.RAMPercent = 50
	}
	if c.Preload.TargetSeconds == 0 {
		c.Preload.TargetSeconds = 20
	}
	if c.Preload.MinHeadMB == 0 {
		c.Preload.MinHeadMB = 8
	}
	if c.Preload.MaxHeadMB == 0 {
		c.Preload.MaxHeadMB = 250
	}
	if c.Preload.TailMB == 0 {
		// Flat tail warmed for every non-resume tier, and for resume targets
		// whenever the container parser cannot locate the cue index (non-MKV or
		// parse failure). MKV resume targets warm the exact cue region instead
		// (see internal/container), so raising this default mainly affects the
		// non-resume and fallback paths.
		c.Preload.TailMB = 16
	}
	if c.Schedule.SweepSeconds == 0 {
		c.Schedule.SweepSeconds = 60
	}
	if c.Schedule.SessionPollSeconds == 0 {
		c.Schedule.SessionPollSeconds = 5
	}
	if c.Residency.ProbeBytes == 0 {
		c.Residency.ProbeBytes = 1 << 20
	}
	if c.Residency.ProbeThreshold == 0 {
		c.Residency.ProbeThreshold = 150 * time.Millisecond
	}
	if c.Residency.ProbeTimeout == 0 {
		c.Residency.ProbeTimeout = 30 * time.Second
	}
	if c.StatusPath == "" {
		c.StatusPath = "/var/local/preloadd/status.json"
	}
	if c.SecretPath == "" {
		c.SecretPath = "/boot/config/plugins/watch-aware-preloader/secrets.toml"
	}
	// Order resolution, in precedence order:
	//   1. an explicit [tiers] order -> honored as-is (an empty order is legal)
	//   2. no order, but at least one legacy per-tier enabled flag -> derived
	//      from those flags (see deriveLegacyOrder)
	//   3. neither -> the full default order
	// orderDefined comes from TOML metadata, not the decoded value, so an
	// explicit `order = []` is distinguishable from an absent key.
	//
	// The legacy branch is gated on a legacy `enabled` flag being DEFINED, not on
	// [tiers] existing: a block holding only new-shape keys (order, override) or
	// only max_items dials would otherwise filter against all-false dials and
	// resolve to an empty order, silently warming nothing.
	if !orderDefined {
		c.Tiers.Order = deriveLegacyOrder(legacyEnabled)
	}
}

// deriveLegacyOrder resolves a pre-order config's `[tiers.<tier>] enabled` dials
// into an order. legacyEnabled holds ONLY the tiers whose flag is present, so an
// absent tier carries no operator opinion (see legacyEnabledFlags).
//
// The reading depends on which way the operator stated their intent:
//   - Any explicit `enabled = true` makes the explicitly-true set an ALLOW-LIST:
//     the order is exactly those tiers. An operator who opts one tier IN is not
//     asking for the others, and reading it permissively would pull unrequested
//     media into a finite page-cache budget.
//   - Otherwise (only `enabled = false` stated) it is a DENY-LIST: the default
//     order minus the stated tiers. This DEPARTS from the pre-order loader on
//     purpose. That loader defaulted every tier to on only when [tiers] was
//     absent entirely (`applyDefaults(md.IsDefined("tiers"))`); once the block
//     existed it used the decoded struct verbatim, so an unstated `enabled`
//     decoded to the zero value false. A config saying only
//     `[tiers.recently_added] enabled = false` therefore used to warm NOTHING,
//     because two unrelated keys happened to be absent. Warming the other two is
//     what an operator writing that stanza means, so the deny-list reading is
//     the intended behavior change, not a preserved one.
//   - No flag at all: the full default order.
//
// A config stating every flag (what rc.preloadd always renders) resolves the
// same under either reading, so the plugin-rendered shape is unaffected and the
// departure above is reachable only from a hand-edited config.toml.
func deriveLegacyOrder(legacyEnabled map[core.Tier]bool) TierOrder {
	if len(legacyEnabled) == 0 {
		return DefaultTierOrder()
	}
	allowList := false
	for _, enabled := range legacyEnabled {
		if enabled {
			allowList = true
			break
		}
	}
	order := TierOrder{}
	for _, t := range DefaultTierOrder() {
		enabled, stated := legacyEnabled[t]
		if (allowList && stated && enabled) || (!allowList && !stated) {
			order = append(order, t)
		}
	}
	return order
}

// Validate checks required fields and ranges.
func (c *Config) Validate() error {
	if c.Server.Type != "emby" {
		return fmt.Errorf("server.type must be \"emby\" in Phase 1, got %q", c.Server.Type)
	}
	if c.Server.URL == "" {
		return fmt.Errorf("server.url is required")
	}
	if c.Preload.RAMPercent < 1 || c.Preload.RAMPercent > 100 {
		return fmt.Errorf("preload.ram_percent must be 1-100, got %d", c.Preload.RAMPercent)
	}
	if c.Preload.TargetSeconds < 1 {
		return fmt.Errorf("preload.target_seconds must be >= 1")
	}
	if c.Preload.MinHeadMB < 0 || c.Preload.MaxHeadMB < 0 || c.Preload.TailMB < 0 {
		return fmt.Errorf("preload min_head_mb, max_head_mb, and tail_mb must be >= 0")
	}
	if c.Preload.MaxHeadMB > 0 && c.Preload.MinHeadMB > c.Preload.MaxHeadMB {
		return fmt.Errorf("preload.min_head_mb (%d) must be <= preload.max_head_mb (%d)",
			c.Preload.MinHeadMB, c.Preload.MaxHeadMB)
	}
	if c.Schedule.SweepSeconds < 1 {
		return fmt.Errorf("schedule.sweep_seconds must be >= 1")
	}
	if c.Schedule.SessionPollSeconds < 1 {
		return fmt.Errorf("schedule.session_poll_seconds must be >= 1")
	}
	if c.Residency.ProbeBytes <= 0 {
		return fmt.Errorf("residency.probe_bytes must be > 0, got %d", c.Residency.ProbeBytes)
	}
	if c.Residency.ProbeThreshold <= 0 {
		return fmt.Errorf("residency.probe_threshold must be > 0, got %v", c.Residency.ProbeThreshold)
	}
	if c.Residency.ProbeTimeout > 0 && c.Residency.ProbeTimeout < 15*time.Second {
		return fmt.Errorf("residency.probe_timeout must be >= 15s (above the array spin-up window) or negative to disable, got %v", c.Residency.ProbeTimeout)
	}
	if err := validateTierOrder(c.Tiers.Order); err != nil {
		return fmt.Errorf("tiers.order: %w", err)
	}
	for user, o := range c.Tiers.Override {
		if err := validateTierOrder(o); err != nil {
			return fmt.Errorf("tiers.override.%s: %w", user, err)
		}
	}
	return nil
}

// validateTierOrder rejects duplicates. Unknown names are already rejected at
// decode time by TierOrder.UnmarshalTOML.
func validateTierOrder(o TierOrder) error {
	seen := make(map[core.Tier]bool, len(o))
	for _, t := range o {
		if seen[t] {
			return fmt.Errorf("duplicate tier %q", t)
		}
		seen[t] = true
	}
	return nil
}
