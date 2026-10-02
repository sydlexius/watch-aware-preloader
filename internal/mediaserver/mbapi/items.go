package mbapi

import (
	"context"
	"net/url"
	"time"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// ticksPerSecond is the Emby/Jellyfin tick unit: 100-nanosecond intervals.
const ticksPerSecond = 10_000_000

// TicksToDuration converts Emby/Jellyfin 100-nanosecond ticks to a Duration.
func TicksToDuration(t int64) time.Duration {
	return time.Duration(t) * (time.Second / ticksPerSecond)
}

type mediaSource struct {
	Path    string `json:"Path"`
	Bitrate int64  `json:"Bitrate"`
	Size    int64  `json:"Size"`
}

// Item is the subset of a BaseItemDto the preloader reads. Both servers emit
// the same field names for it.
type Item struct {
	ID           string `json:"Id"`
	Name         string `json:"Name"`
	RunTimeTicks int64  `json:"RunTimeTicks"`
	UserData     struct {
		PlaybackPositionTicks int64 `json:"PlaybackPositionTicks"`
	} `json:"UserData"`
	MediaSources []mediaSource `json:"MediaSources"`
}

// ToCore converts the item to the provider-neutral type, attributing it to
// userID. The first media source supplies the path, bitrate and size.
func (it Item) ToCore(userID string) core.MediaItem {
	mi := core.MediaItem{
		ID:           it.ID,
		Name:         it.Name,
		Runtime:      TicksToDuration(it.RunTimeTicks),
		ResumeOffset: TicksToDuration(it.UserData.PlaybackPositionTicks),
		UserID:       userID,
	}
	if len(it.MediaSources) > 0 {
		mi.ServerPath = it.MediaSources[0].Path
		mi.BitrateBps = it.MediaSources[0].Bitrate
		mi.SizeBytes = it.MediaSources[0].Size
	}
	return mi
}

func toCore(items []Item, userID string) []core.MediaItem {
	out := make([]core.MediaItem, 0, len(items))
	for _, it := range items {
		out = append(out, it.ToCore(userID))
	}
	return out
}

// Items fetches an endpoint that answers with an {"Items": [...]} envelope.
func (c *Client) Items(ctx context.Context, path string, q url.Values, userID string) ([]core.MediaItem, error) {
	var resp struct {
		Items []Item `json:"Items"`
	}
	if err := c.Get(ctx, path, q, &resp); err != nil {
		return nil, err
	}
	return toCore(resp.Items, userID), nil
}

// ItemArray fetches an endpoint that answers with a bare JSON array of items,
// as the Latest endpoints do.
func (c *Client) ItemArray(ctx context.Context, path string, q url.Values, userID string) ([]core.MediaItem, error) {
	var items []Item
	if err := c.Get(ctx, path, q, &items); err != nil {
		return nil, err
	}
	return toCore(items, userID), nil
}

// MediaFields is the base query every item fetch needs: without Path and
// MediaSources an item cannot be mapped to a file or sized.
func MediaFields() url.Values {
	return url.Values{"Fields": {"Path,MediaSources"}}
}

// LatestFields is the query for the RecentlyAdded (Latest) tier. Without
// GroupItems=false the endpoint returns MusicAlbum/Series containers (no Path,
// no MediaSources); IncludeItemTypes keeps it to warmable video leaves.
func LatestFields() url.Values {
	q := MediaFields()
	q.Set("GroupItems", "false")
	q.Set("IncludeItemTypes", "Movie,Episode")
	return q
}

// Users lists the server's user accounts.
func (c *Client) Users(ctx context.Context) ([]core.User, error) {
	var users []core.User
	if err := c.Get(ctx, "/Users", nil, &users); err != nil {
		return nil, err
	}
	return users, nil
}

// Libraries lists the server's media libraries (VirtualFolders).
func (c *Client) Libraries(ctx context.Context) ([]core.Library, error) {
	var libs []core.Library
	if err := c.Get(ctx, "/Library/VirtualFolders", nil, &libs); err != nil {
		return nil, err
	}
	return libs, nil
}

// NowPlayingIDs returns the set of item IDs in active playback sessions.
func (c *Client) NowPlayingIDs(ctx context.Context) (map[string]bool, error) {
	var sessions []struct {
		NowPlayingItem *struct {
			ID string `json:"Id"`
		} `json:"NowPlayingItem"`
	}
	if err := c.Get(ctx, "/Sessions", nil, &sessions); err != nil {
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
