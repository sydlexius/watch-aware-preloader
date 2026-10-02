package jellyfin

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"
)

// recorded captures one request so a test can assert on it after the call.
type recorded struct {
	path   string
	query  url.Values
	auth   string
	legacy string
}

// serve answers every request with body and records what it received.
func serve(t *testing.T, body string) (*Client, chan recorded) {
	t.Helper()
	ch := make(chan recorded, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ch <- recorded{
			path:   r.URL.Path,
			query:  r.URL.Query(),
			auth:   r.Header.Get("Authorization"),
			legacy: r.Header.Get("X-Emby-Token"),
		}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "abc123", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, ch
}

func TestNewRejectsBadInput(t *testing.T) {
	if _, err := New("ftp://h", "k", nil); err == nil {
		t.Error("non-http base URL should be rejected")
	}
	if _, err := New("http://h:8096", "", nil); err == nil {
		t.Error("empty API key should be rejected")
	}
}

func TestSendsMediaBrowserAuthorization(t *testing.T) {
	// A stock Jellyfin ignores X-Emby-Token (EnableLegacyAuthorization is off
	// by default), so the key must travel in the Authorization header.
	c, ch := serve(t, `[]`)
	if _, err := c.Users(context.Background()); err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if want := `MediaBrowser Token="abc123"`; got.auth != want {
		t.Errorf("Authorization = %q, want %q", got.auth, want)
	}
	if got.legacy != "" {
		t.Errorf("X-Emby-Token = %q, want it unset", got.legacy)
	}
}

func TestAuthHeaderEscapesTheKey(t *testing.T) {
	// The server splits parameters on quotes and commas and URL-decodes each
	// value; an unescaped quote or comma would truncate the token.
	got := authHeader(`a"b,c d`)
	if want := `MediaBrowser Token="a%22b%2Cc+d"`; got != want {
		t.Errorf("authHeader = %q, want %q", got, want)
	}
	v, err := url.QueryUnescape("a%22b%2Cc+d")
	if err != nil || v != `a"b,c d` {
		t.Errorf("escaped token does not round-trip: %q, %v", v, err)
	}
}

func TestPerUserRoutes(t *testing.T) {
	for _, tc := range []struct {
		name      string
		body      string
		call      func(*Client) error
		wantPath  string
		wantQuery map[string]string
	}{
		{
			name: "resume",
			body: `{"Items":[]}`,
			call: func(c *Client) error {
				_, err := c.Resume(context.Background(), "u1")
				return err
			},
			wantPath:  "/UserItems/Resume",
			wantQuery: map[string]string{"userId": "u1", "Fields": "Path,MediaSources", "MediaTypes": "Video"},
		},
		{
			name: "nextup",
			body: `{"Items":[]}`,
			call: func(c *Client) error {
				_, err := c.NextUp(context.Background(), "u1")
				return err
			},
			wantPath:  "/Shows/NextUp",
			wantQuery: map[string]string{"userId": "u1", "Fields": "Path,MediaSources"},
		},
		{
			name: "latest",
			body: `[]`,
			call: func(c *Client) error {
				_, err := c.RecentlyAdded(context.Background(), "u1")
				return err
			},
			wantPath: "/Items/Latest",
			wantQuery: map[string]string{
				"userId": "u1", "Fields": "Path,MediaSources",
				"GroupItems": "false", "IncludeItemTypes": "Movie,Episode",
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, ch := serve(t, tc.body)
			if err := tc.call(c); err != nil {
				t.Fatal(err)
			}
			got := <-ch
			if got.path != tc.wantPath {
				t.Errorf("path = %q, want %q", got.path, tc.wantPath)
			}
			for k, want := range tc.wantQuery {
				if v := got.query.Get(k); v != want {
					t.Errorf("query %s = %q, want %q", k, v, want)
				}
			}
		})
	}
}

// resumeFixture is a trimmed /UserItems/Resume response in Jellyfin's shape:
// PascalCase keys, dashless GUID ids, and the extra fields a real response
// carries that the adapter must ignore.
const resumeFixture = `{
  "Items": [{
    "Name": "Pilot",
    "ServerId": "4a7f1c2e9b8d4e6fa1b2c3d4e5f60718",
    "Id": "d1f0e2a3b4c5d6e7f8091a2b3c4d5e6f",
    "Type": "Episode",
    "RunTimeTicks": 26400000000,
    "MediaSources": [{
      "Protocol": "File",
      "Id": "d1f0e2a3b4c5d6e7f8091a2b3c4d5e6f",
      "Path": "/media/tv/Show/Season 01/Show - S01E01.mkv",
      "Container": "mkv",
      "Size": 1650000000,
      "Bitrate": 5000000
    }],
    "UserData": {"PlaybackPositionTicks": 6000000000, "Played": false}
  }],
  "TotalRecordCount": 1,
  "StartIndex": 0
}`

func TestResumeDecodesJellyfinItem(t *testing.T) {
	c, ch := serve(t, resumeFixture)
	items, err := c.Resume(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.ID != "d1f0e2a3b4c5d6e7f8091a2b3c4d5e6f" || it.Name != "Pilot" || it.UserID != "u1" {
		t.Errorf("identity fields wrong: %+v", it)
	}
	if it.ServerPath != "/media/tv/Show/Season 01/Show - S01E01.mkv" {
		t.Errorf("ServerPath = %q", it.ServerPath)
	}
	if it.BitrateBps != 5_000_000 || it.SizeBytes != 1_650_000_000 {
		t.Errorf("bitrate/size = %d/%d", it.BitrateBps, it.SizeBytes)
	}
	if it.Runtime != 44*time.Minute || it.ResumeOffset != 10*time.Minute {
		t.Errorf("runtime/resume = %v/%v, want 44m/10m", it.Runtime, it.ResumeOffset)
	}
}

func TestRecentlyAddedDecodesBareArray(t *testing.T) {
	c, ch := serve(t, `[{"Id":"m1","Name":"Film","MediaSources":[{"Path":"/media/movies/Film.mkv"}]}]`)
	items, err := c.RecentlyAdded(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	if len(items) != 1 || items[0].ID != "m1" || items[0].ServerPath != "/media/movies/Film.mkv" {
		t.Errorf("items = %+v", items)
	}
}

func TestLibrariesDecodeVirtualFolders(t *testing.T) {
	// Jellyfin's VirtualFolderInfo: same ItemId/Locations/CollectionType keys
	// core.Library decodes, plus LibraryOptions and refresh state to ignore.
	c, ch := serve(t, `[{
	  "Name": "Shows",
	  "Locations": ["/media/tv"],
	  "CollectionType": "tvshows",
	  "LibraryOptions": {"Enabled": true},
	  "ItemId": "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5",
	  "PrimaryImageItemId": "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5",
	  "RefreshStatus": "Idle"
	}]`)
	libs, err := c.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.path != "/Library/VirtualFolders" {
		t.Errorf("path = %q", got.path)
	}
	if len(libs) != 1 {
		t.Fatalf("got %d libraries, want 1", len(libs))
	}
	l := libs[0]
	if l.ID != "a0b1c2d3e4f5a6b7c8d9e0f1a2b3c4d5" || l.Name != "Shows" || l.Type != "tvshows" ||
		len(l.Locations) != 1 || l.Locations[0] != "/media/tv" {
		t.Errorf("library = %+v", l)
	}
}

func TestUsersAndNowPlaying(t *testing.T) {
	c, ch := serve(t, `[{"Name":"alice","Id":"11112222333344445555666677778888","HasPassword":true}]`)
	users, err := c.Users(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	<-ch
	if len(users) != 1 || users[0].ID != "11112222333344445555666677778888" || users[0].Name != "alice" {
		t.Errorf("users = %+v", users)
	}

	c, ch = serve(t, `[{"Id":"s1","NowPlayingItem":{"Id":"x1","Name":"Film"}},{"Id":"s2"}]`)
	ids, err := c.NowPlayingIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got := <-ch; got.path != "/Sessions" {
		t.Errorf("path = %q", got.path)
	}
	if len(ids) != 1 || !ids["x1"] {
		t.Errorf("now playing = %v, want {x1}", ids)
	}
}

func TestDoesNotFollowRedirect(t *testing.T) {
	// The redirect-refusal floor comes from mbapi; assert it holds for this
	// adapter's header too, so the key is never replayed to another host.
	hit := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		hit <- r.Header.Get("Authorization")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL+"/Users", http.StatusFound)
	}))
	defer redirector.Close()

	c, err := New(redirector.URL, "abc123", redirector.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Users(context.Background()); err == nil {
		t.Error("a 302 should surface as an error, not be followed")
	}
	select {
	case h := <-hit:
		t.Errorf("redirect was followed; target received Authorization %q", h)
	default:
	}
}
