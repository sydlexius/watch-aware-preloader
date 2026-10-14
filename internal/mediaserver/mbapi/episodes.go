package mbapi

import (
	"context"
	"fmt"
	"strings"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

// EpisodeAfter returns the episode that follows itemID in its series, for the
// binge look-ahead tier (#139). ok is false when there is nothing to warm: the
// item is the series' last episode, the server's answer does not contain the
// item, the following episode is a special (season 0), or it has no media file.
//
// It makes ONE request: /Shows/{seriesID}/Episodes starting at itemID, limited
// to two results, so the answer is normally [itemID, the next episode]. Both
// servers document StartItemId as "skip through the list until a given item is
// found" over the series' episodes in series order. The answer is still not
// trusted to lead with itemID: the item is located by ID and the element after
// it is taken, and when the item is absent (a server that ignored StartItemId
// and returned the series' first two episodes, say) the answer is nothing
// rather than a guess.
//
// SeasonId is deliberately NOT sent, so a season finale's successor is the next
// season's premiere: a viewer finishing a season usually starts the next one.
// IsMissing=false asks the server to keep virtual "missing" episodes (metadata
// with no file, so nothing to warm) out of the list, but Emby does not document
// it on this endpoint, so a successor without a media file path is also
// refused here rather than emitted as a placeholder (#170).
//
// userKey is the vendor's spelling of the user query parameter (Emby UserId,
// Jellyfin userId); the server needs the user to resolve per-user data.
func (c *Client) EpisodeAfter(ctx context.Context, userKey, userID, seriesID, itemID string) (core.MediaItem, bool, error) {
	// seriesID comes from the server and becomes a path segment. JoinPath keeps
	// it on the configured host regardless, but a separator or dot-segment would
	// still steer the request to a different route on that host, so refuse it.
	if seriesID == "" || seriesID == "." || seriesID == ".." || strings.ContainsAny(seriesID, `/\?#`) {
		return core.MediaItem{}, false, fmt.Errorf("%s: invalid series id %q", c.vendor, seriesID)
	}
	q := MediaFields()
	q.Set(userKey, userID)
	q.Set("StartItemId", itemID)
	q.Set("Limit", "2")
	q.Set("IsMissing", "false")

	var resp struct {
		Items []Item `json:"Items"`
	}
	if err := c.Get(ctx, "/Shows/"+seriesID+"/Episodes", q, &resp); err != nil {
		return core.MediaItem{}, false, err
	}
	for i, it := range resp.Items {
		if it.ID != itemID {
			continue
		}
		if i+1 >= len(resp.Items) {
			return core.MediaItem{}, false, nil // itemID is the last episode
		}
		next := resp.Items[i+1]
		if next.ID == "" || (next.ParentIndexNumber != nil && *next.ParentIndexNumber == 0) {
			return core.MediaItem{}, false, nil // unidentifiable, or a special
		}
		if len(next.MediaSources) == 0 || next.MediaSources[0].Path == "" {
			return core.MediaItem{}, false, nil // a placeholder with no file
		}
		return next.ToCore(userID), true, nil
	}
	return core.MediaItem{}, false, nil
}
