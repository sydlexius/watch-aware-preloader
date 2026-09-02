// Package core holds the domain types shared across the preloader units.
package core

import "time"

// Tier is the preload priority class. The integer values are NOT a priority
// order: priority is configuration data resolved at runtime (see
// config.TierOrder and scorer.RankOpts). Never compare two Tier values with
// < or > to mean "higher priority"; look up their position in the resolved
// order instead.
type Tier int

// Preload signal tiers. Declaration order is the DEFAULT global order only
// (applied by config.applyDefaults); it carries no meaning at comparison time.
const (
	TierResume        Tier = iota // recent incompletes, not currently playing
	TierNextUp                    // next episode of an active series
	TierRecentlyAdded             // recently added, unwatched
	TierBingeAhead                // episode after next-up (reserved; Phase 3)
	TierBestEffort                // filesystem-recency fill
)

// String returns the lowercase tier label used in structured logs.
func (t Tier) String() string {
	switch t {
	case TierResume:
		return "resume"
	case TierNextUp:
		return "next-up"
	case TierRecentlyAdded:
		return "recently-added"
	case TierBingeAhead:
		return "binge-ahead"
	case TierBestEffort:
		return "best-effort"
	default:
		return "unknown"
	}
}

// MediaItem is a normalized media file surfaced by the media server.
type MediaItem struct {
	ID           string
	Name         string
	ServerPath   string        // path as the media server reports it
	BitrateBps   int64         // average bits per second; 0 if unknown
	SizeBytes    int64         // file size in bytes
	Runtime      time.Duration // total playback duration
	ResumeOffset time.Duration // playback position for resume items; 0 otherwise
	UserID       string        // the user account that surfaced this item
}

// User is a media-server user account.
//
// It lives here rather than in a vendor package because the pipeline's Provider
// interface names it, and an interface that names a vendor type can only ever
// have one implementation. Emby and Jellyfin both report an id and a display
// name, so the shape is genuinely shared rather than an Emby detail promoted
// upward (#3).
//
// The JSON tags are load-bearing: both servers return PascalCase fields, so an
// adapter can decode straight into this type. An adapter whose wire shape
// differs should decode into its own type and convert, rather than widening
// these tags.
type User struct {
	ID   string `json:"Id"`
	Name string `json:"Name"`
}

// Library is a media-server library and the filesystem locations backing it.
//
// Locations are paths as the SERVER reports them, so a consumer comparing them
// against item paths must normalize both into a common namespace first (see
// libscope.ToHost). Type is the server's collection type ("movies", "tvshows",
// and so on) and is empty when the server does not classify the library.
type Library struct {
	ID        string   `json:"ItemId"`
	Name      string   `json:"Name"`
	Type      string   `json:"CollectionType"`
	Locations []string `json:"Locations"`
}

// PreloadTarget is a scored, ordered item ready to preload.
type PreloadTarget struct {
	Item MediaItem
	Tier Tier
}
