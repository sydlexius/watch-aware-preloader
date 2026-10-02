// Package jellyfin is a minimal read-only client for the Jellyfin API.
//
// Jellyfin forked from Emby and still shares its wire format, so the HTTP core
// and item decoding come from mbapi. This package owns only what differs: how
// a request authenticates and which routes serve the per-user queries.
package jellyfin

import (
	"context"
	"fmt"
	"net/http"
	"net/url"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/mbapi"
)

// Client talks to a single Jellyfin server with an API key.
type Client struct {
	api *mbapi.Client
}

// authHeader builds the Authorization value Jellyfin accepts for an API key.
//
// Jellyfin reads X-Emby-Token only when the server's EnableLegacyAuthorization
// option is on, and that option is off by default, so the Emby adapter's header
// would be rejected by a stock server. The "MediaBrowser" scheme with a Token
// parameter is the form Jellyfin always accepts. The server URL-decodes each
// parameter value, so the key is escaped here: a quote or comma in it would
// otherwise end the value early.
func authHeader(apiKey string) string {
	return `MediaBrowser Token="` + url.QueryEscape(apiKey) + `"`
}

// New validates the base URL at the trust boundary and returns a Client.
func New(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("api key is required")
	}
	value := authHeader(apiKey)
	auth := func(req *http.Request) { req.Header.Set("Authorization", value) }
	api, err := mbapi.New("jellyfin", baseURL, auth, httpClient)
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}

// Users lists Jellyfin user accounts.
func (c *Client) Users(ctx context.Context) ([]core.User, error) {
	return c.api.Users(ctx)
}

// Libraries lists the server's media libraries (VirtualFolders).
func (c *Client) Libraries(ctx context.Context) ([]core.Library, error) {
	return c.api.Libraries(ctx)
}

// The per-user queries below use Jellyfin's userId query-parameter routes. The
// /Users/{userId}/Items/... routes Emby uses still exist on Jellyfin, but only
// as handlers marked obsolete and kept for backwards compatibility.

// Resume returns the user's in-progress items with their resume offsets.
// MediaTypes=Video keeps the tier to warmable video, matching the Emby adapter.
func (c *Client) Resume(ctx context.Context, userID string) ([]core.MediaItem, error) {
	q := mbapi.MediaFields()
	q.Set("userId", userID)
	q.Set("MediaTypes", "Video")
	return c.api.Items(ctx, "/UserItems/Resume", q, userID)
}

// NextUp returns the next episode of each series the user is watching.
func (c *Client) NextUp(ctx context.Context, userID string) ([]core.MediaItem, error) {
	q := mbapi.MediaFields()
	q.Set("userId", userID)
	return c.api.Items(ctx, "/Shows/NextUp", q, userID)
}

// RecentlyAdded returns recently added items for the user.
func (c *Client) RecentlyAdded(ctx context.Context, userID string) ([]core.MediaItem, error) {
	// /Items/Latest returns a bare array, not an {Items:[]} envelope.
	q := mbapi.LatestFields()
	q.Set("userId", userID)
	return c.api.ItemArray(ctx, "/Items/Latest", q, userID)
}

// NowPlayingIDs returns the set of item IDs in active playback sessions.
func (c *Client) NowPlayingIDs(ctx context.Context) (map[string]bool, error) {
	return c.api.NowPlayingIDs(ctx)
}
