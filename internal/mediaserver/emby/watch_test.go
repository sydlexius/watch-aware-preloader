package emby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestLibraries(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/Library/VirtualFolders" {
			t.Errorf("path = %q, want /Library/VirtualFolders", r.URL.Path)
		}
		_, _ = w.Write([]byte(`[
			{"Name":"Movies","ItemId":"111","CollectionType":"movies","Locations":["/share/Movies","/share/Videos"]},
			{"Name":"Music","ItemId":"222","CollectionType":null,"Locations":[]}
		]`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	libs, err := c.Libraries(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(libs) != 2 {
		t.Fatalf("got %d libraries, want 2", len(libs))
	}
	if libs[0].ID != "111" || libs[0].Name != "Movies" || libs[0].Type != "movies" {
		t.Errorf("libs[0] = %+v, want {111 Movies movies}", libs[0])
	}
	if len(libs[0].Locations) != 2 || libs[0].Locations[0] != "/share/Movies" {
		t.Errorf("libs[0].Locations = %v, want [/share/Movies /share/Videos]", libs[0].Locations)
	}
	if libs[1].Type != "" {
		t.Errorf("null CollectionType should decode to empty string, got %q", libs[1].Type)
	}
}

func TestRecentlyAddedQueryParams(t *testing.T) {
	// Latest must request flattened, video-only leaf items, else it returns
	// MusicAlbum/Series containers with no warmable media.
	qCh := make(chan url.Values, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		qCh <- r.URL.Query()
		_, _ = w.Write([]byte(`[]`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.RecentlyAdded(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	q := <-qCh
	if q.Get("GroupItems") != "false" {
		t.Errorf("GroupItems = %q, want false", q.Get("GroupItems"))
	}
	if q.Get("IncludeItemTypes") != "Movie,Episode" {
		t.Errorf("IncludeItemTypes = %q, want Movie,Episode", q.Get("IncludeItemTypes"))
	}
	if f := q.Get("Fields"); !strings.Contains(f, "Path") || !strings.Contains(f, "MediaSources") {
		t.Errorf("Fields = %q, want to contain Path,MediaSources", f)
	}
}

func TestResumeQueryParams(t *testing.T) {
	// Emby's /Items/Resume returns nothing unless MediaTypes=Video is set:
	// without it the endpoint yields zero items on a real server, silently
	// disabling the resume tier. It must also request Path,MediaSources so the
	// resume offset and warmable media info come back.
	type req struct {
		path string
		q    url.Values
	}
	reqCh := make(chan req, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCh <- req{path: r.URL.Path, q: r.URL.Query()}
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	got := <-reqCh
	if got.path != "/Users/u1/Items/Resume" {
		t.Errorf("path = %q, want /Users/u1/Items/Resume", got.path)
	}
	q := got.q
	if q.Get("MediaTypes") != "Video" {
		t.Errorf("MediaTypes = %q, want Video", q.Get("MediaTypes"))
	}
	if f := q.Get("Fields"); !strings.Contains(f, "Path") || !strings.Contains(f, "MediaSources") {
		t.Errorf("Fields = %q, want to contain Path,MediaSources", f)
	}
}

func TestRecentlyAddedMapsLeafItems(t *testing.T) {
	c := serveFixture(t, "latest.json")
	items, err := c.RecentlyAdded(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	for _, it := range items {
		if it.ServerPath == "" || it.BitrateBps == 0 || it.SizeBytes == 0 {
			t.Errorf("leaf item missing media info: %+v", it)
		}
	}
}

func serveFixture(t *testing.T, file string) *Client {
	t.Helper()
	body, err := os.ReadFile("testdata/" + file)
	if err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	t.Cleanup(srv.Close)
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestResumeMapsFields(t *testing.T) {
	c := serveFixture(t, "resume.json")
	items, err := c.Resume(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.ID != "item1" || it.ServerPath != "/share/TV_Shows/Slow Horses/s05e01.mkv" {
		t.Errorf("bad id/path: %+v", it)
	}
	if it.BitrateBps != 25000000 || it.SizeBytes != 8471453856 {
		t.Errorf("bad bitrate/size: %+v", it)
	}
	// RunTimeTicks 27063290000 / 1e7 = 2706.329s
	if it.Runtime != time.Duration(27063290000*100) {
		t.Errorf("runtime = %v", it.Runtime)
	}
	// PlaybackPositionTicks 6000000000 / 1e7 = 600s
	if it.ResumeOffset != 600*time.Second {
		t.Errorf("resume offset = %v, want 10m", it.ResumeOffset)
	}
	if it.UserID != "u1" {
		t.Errorf("user id = %q, want u1", it.UserID)
	}
}

func TestNextUpMapsFields(t *testing.T) {
	// NextUp shares the {Items:[]} envelope decode with Resume, so this locks the
	// leaf-item field mapping (path, bitrate, size, tick-converted runtime, user).
	c := serveFixture(t, "nextup.json")
	items, err := c.NextUp(context.Background(), "u1")
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 1 {
		t.Fatalf("got %d items, want 1", len(items))
	}
	it := items[0]
	if it.ID != "next1" || it.ServerPath != "/share/TV_Shows/Example Series/s02e03.mkv" {
		t.Errorf("bad id/path: %+v", it)
	}
	if it.BitrateBps != 12000000 || it.SizeBytes != 4200000000 {
		t.Errorf("bad bitrate/size: %+v", it)
	}
	// RunTimeTicks 18000000000 / 1e7 = 1800s
	if it.Runtime != 1800*time.Second {
		t.Errorf("runtime = %v, want 30m", it.Runtime)
	}
	if it.UserID != "u1" {
		t.Errorf("user id = %q, want u1", it.UserID)
	}
	// The binge look-ahead tier keys its follow-up request off this.
	if it.SeriesID != "series1" {
		t.Errorf("series id = %q, want series1", it.SeriesID)
	}
}

func TestEpisodeAfterRoute(t *testing.T) {
	// The binge look-ahead request: Emby's /Shows/{Id}/Episodes, user-scoped by a
	// UserId query param and authenticated with X-Emby-Token like every call.
	type req struct {
		path, token string
		q           url.Values
	}
	reqCh := make(chan req, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCh <- req{path: r.URL.Path, token: r.Header.Get("X-Emby-Token"), q: r.URL.Query()}
		_, _ = w.Write([]byte(`{"Items":[{"Id":"e3","ParentIndexNumber":1},` +
			`{"Id":"e4","SeriesId":"ser1","ParentIndexNumber":1,"MediaSources":[{"Path":"/share/TV/e4.mkv"}]}]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	it, ok, err := c.EpisodeAfter(context.Background(), "u1", "ser1", "e3")
	if err != nil || !ok || it.ID != "e4" || it.ServerPath != "/share/TV/e4.mkv" {
		t.Fatalf("got (%+v, %v, %v), want e4", it, ok, err)
	}
	got := <-reqCh
	if got.path != "/Shows/ser1/Episodes" || got.token != "k" {
		t.Errorf("path/token = %q/%q, want /Shows/ser1/Episodes and k", got.path, got.token)
	}
	if got.q.Get("UserId") != "u1" || got.q.Get("StartItemId") != "e3" || got.q.Get("Limit") != "2" {
		t.Errorf("query = %v, want UserId=u1 StartItemId=e3 Limit=2", got.q)
	}
}

func TestNextUpQueryParams(t *testing.T) {
	// NextUp is user-scoped via a UserId query param (not a path segment) and must
	// request Path,MediaSources so the warmable media info comes back.
	type req struct {
		path string
		q    url.Values
	}
	reqCh := make(chan req, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqCh <- req{path: r.URL.Path, q: r.URL.Query()}
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.NextUp(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	got := <-reqCh
	if got.path != "/Shows/NextUp" {
		t.Errorf("path = %q, want /Shows/NextUp", got.path)
	}
	q := got.q
	if q.Get("UserId") != "u1" {
		t.Errorf("UserId = %q, want u1", q.Get("UserId"))
	}
	// Exact comma-delimited tokens, not a substring check: these tests lock the
	// request contract, so "Path" must not be satisfied by a drifted "FilePath".
	fields := strings.Split(q.Get("Fields"), ",")
	if !slices.Contains(fields, "Path") || !slices.Contains(fields, "MediaSources") {
		t.Errorf("Fields = %q, want exact tokens Path and MediaSources", q.Get("Fields"))
	}
}

func TestNowPlayingIDs(t *testing.T) {
	// /Sessions is a bare array; only sessions with a non-empty NowPlayingItem.Id
	// count. Sessions with the item absent, null, or an empty Id are skipped.
	c := serveFixture(t, "sessions.json")
	ids, err := c.NowPlayingIDs(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(ids) != 1 {
		t.Fatalf("got %d ids, want 1: %v", len(ids), ids)
	}
	if !ids["playing1"] {
		t.Errorf("ids missing playing1: %v", ids)
	}
	if _, ok := ids[""]; ok {
		t.Errorf("empty-id session should be skipped (no empty key), got %v", ids)
	}
}
