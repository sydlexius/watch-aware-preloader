package emby

import (
	"context"
	"net/url"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// ticksPerSecond is the Emby/Jellyfin tick unit: 100-nanosecond intervals.
const ticksPerSecond = 10_000_000

// User is an Emby user account.
// User and Library are aliases for the provider-neutral types in core, which
// is where the pipeline's Provider interface names them (#3). They are aliases
// rather than distinct types so this package's API is unchanged and an
// adapter's decode target stays spelled the way its endpoints read.
type User = core.User

type mediaSource struct {
	Path    string `json:"Path"`
	Bitrate int64  `json:"Bitrate"`
	Size    int64  `json:"Size"`
}

type embyItem struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	RunTimeTicks int64  `json:"RunTimeTicks"`
	UserData     struct {
		PlaybackPositionTicks int64 `json:"PlaybackPositionTicks"`
	} `json:"UserData"`
	MediaSources []mediaSource `json:"MediaSources"`
}

type itemsResponse struct {
	Items []embyItem `json:"Items"`
}

// ticksToDuration converts Emby/Jellyfin 100-nanosecond ticks to a Duration.
func ticksToDuration(t int64) time.Duration {
	return time.Duration(t) * (time.Second / ticksPerSecond)
}

func (e embyItem) toCore(userID string) core.MediaItem {
	mi := core.MediaItem{
		ID:           e.ID,
		Name:         e.Name,
		Runtime:      ticksToDuration(e.RunTimeTicks),
		ResumeOffset: ticksToDuration(e.UserData.PlaybackPositionTicks),
		UserID:       userID,
	}
	if len(e.MediaSources) > 0 {
		mi.ServerPath = e.MediaSources[0].Path
		mi.BitrateBps = e.MediaSources[0].Bitrate
		mi.SizeBytes = e.MediaSources[0].Size
	}
	return mi
}

func (c *Client) itemsTo(ctx context.Context, path string, q url.Values, userID string) ([]core.MediaItem, error) {
	var resp itemsResponse
	if err := c.get(ctx, path, q, &resp); err != nil {
		return nil, err
	}
	out := make([]core.MediaItem, 0, len(resp.Items))
	for _, it := range resp.Items {
		out = append(out, it.toCore(userID))
	}
	return out, nil
}

func mediaFields() url.Values {
	return url.Values{"Fields": {"Path,MediaSources"}}
}

// latestFields is the query for the RecentlyAdded (Latest) tier. Without
// GroupItems=false the endpoint returns MusicAlbum/Series containers (no Path,
// no MediaSources); IncludeItemTypes keeps it to warmable video leaves.
func latestFields() url.Values {
	return url.Values{
		"Fields":           {"Path,MediaSources"},
		"GroupItems":       {"false"},
		"IncludeItemTypes": {"Movie,Episode"},
	}
}

// Users lists Emby user accounts.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	var users []User
	if err := c.get(ctx, "/Users", nil, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// Library is a media library (VirtualFolder) the server exposes. ID is the
// stable ItemId; Type is the Emby CollectionType (movies/tvshows/music/...),
// empty when the server reports it as null. Locations are the library's source
// folders as the server reports them (used to decide which library an item
// belongs to, for the library-scope filter).
type Library = core.Library

// Libraries lists the server's media libraries (VirtualFolders).
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	var libs []Library
	if err := c.get(ctx, "/Library/VirtualFolders", nil, &libs); err != nil {
		return nil, err
	}
	return libs, nil
}

// Resume returns the user's in-progress items with their resume offsets.
func (c *Client) Resume(ctx context.Context, userID string) ([]core.MediaItem, error) {
	// Emby's /Items/Resume returns zero items unless MediaTypes=Video is set,
	// which silently disabled the resume tier on real servers (verified against
	// a live Emby: the same call returns 0 without this filter and the full
	// in-progress list with it). RecentlyAdded avoids this via IncludeItemTypes.
	q := mediaFields()
	q.Set("MediaTypes", "Video")
	return c.itemsTo(ctx, "/Users/"+userID+"/Items/Resume", q, userID)
}

// NextUp returns the next episode of each series the user is watching.
func (c *Client) NextUp(ctx context.Context, userID string) ([]core.MediaItem, error) {
	q := mediaFields()
	q.Set("UserId", userID)
	return c.itemsTo(ctx, "/Shows/NextUp", q, userID)
}

// RecentlyAdded returns recently added items for the user.
func (c *Client) RecentlyAdded(ctx context.Context, userID string) ([]core.MediaItem, error) {
	// /Items/Latest returns a bare array, not an {Items:[]} envelope.
	var items []embyItem
	if err := c.get(ctx, "/Users/"+userID+"/Items/Latest", latestFields(), &items); err != nil {
		return nil, err
	}
	out := make([]core.MediaItem, 0, len(items))
	for _, it := range items {
		out = append(out, it.toCore(userID))
	}
	return out, nil
}

// NowPlayingIDs returns the set of item IDs in active playback sessions.
func (c *Client) NowPlayingIDs(ctx context.Context) (map[string]bool, error) {
	var sessions []struct {
		NowPlayingItem *struct {
			ID string `json:"Id"`
		} `json:"NowPlayingItem"`
	}
	if err := c.get(ctx, "/Sessions", nil, &sessions); err != nil {
		return nil, err
	}
	ids := map[string]bool{}
	for _, s := range sessions {
		if s.NowPlayingItem != nil && s.NowPlayingItem.ID != "" {
			ids[s.NowPlayingItem.ID] = true
		}
	}
	return ids, nil
}
