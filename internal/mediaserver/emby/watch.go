package emby

import (
	"context"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/mbapi"
)

// User is an Emby user account.
// User and Library are aliases for the provider-neutral types in core, which
// is where the pipeline's Provider interface names them (#3). They are aliases
// rather than distinct types so this package's API is unchanged and an
// adapter's decode target stays spelled the way its endpoints read.
type User = core.User

// Library is a media library (VirtualFolder) the server exposes. ID is the
// stable ItemId; Type is the Emby CollectionType (movies/tvshows/music/...),
// empty when the server reports it as null. Locations are the library's source
// folders as the server reports them (used to decide which library an item
// belongs to, for the library-scope filter).
type Library = core.Library

// Users lists Emby user accounts.
func (c *Client) Users(ctx context.Context) ([]User, error) {
	return c.api.Users(ctx)
}

// Libraries lists the server's media libraries (VirtualFolders).
func (c *Client) Libraries(ctx context.Context) ([]Library, error) {
	return c.api.Libraries(ctx)
}

// Resume returns the user's in-progress items with their resume offsets.
func (c *Client) Resume(ctx context.Context, userID string) ([]core.MediaItem, error) {
	// Emby's /Items/Resume returns zero items unless MediaTypes=Video is set,
	// which silently disabled the resume tier on real servers (verified against
	// a live Emby: the same call returns 0 without this filter and the full
	// in-progress list with it). RecentlyAdded avoids this via IncludeItemTypes.
	q := mbapi.MediaFields()
	q.Set("MediaTypes", "Video")
	return c.api.Items(ctx, "/Users/"+userID+"/Items/Resume", q, userID)
}

// NextUp returns the next episode of each series the user is watching.
func (c *Client) NextUp(ctx context.Context, userID string) ([]core.MediaItem, error) {
	q := mbapi.MediaFields()
	q.Set("UserId", userID)
	return c.api.Items(ctx, "/Shows/NextUp", q, userID)
}

// RecentlyAdded returns recently added items for the user.
func (c *Client) RecentlyAdded(ctx context.Context, userID string) ([]core.MediaItem, error) {
	// /Items/Latest returns a bare array, not an {Items:[]} envelope.
	return c.api.ItemArray(ctx, "/Users/"+userID+"/Items/Latest", mbapi.LatestFields(), userID)
}

// NowPlayingIDs returns the set of item IDs in active playback sessions.
func (c *Client) NowPlayingIDs(ctx context.Context) (map[string]bool, error) {
	return c.api.NowPlayingIDs(ctx)
}
