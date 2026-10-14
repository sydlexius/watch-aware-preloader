package mbapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// seen is one request the test server received.
type seen struct {
	url  *url.URL
	auth string
}

// episodeServer answers every request with body and records each request.
func episodeServer(t *testing.T, body string) (*Client, chan seen) {
	t.Helper()
	got := make(chan seen, 16)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- seen{url: r.URL, auth: r.Header.Get("X-Test-Auth")}
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	c, err := New("test", srv.URL, func(r *http.Request) { r.Header.Set("X-Test-Auth", "k") }, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	return c, got
}

// ep renders one episode DTO. season < 0 omits ParentIndexNumber.
func ep(id string, season int) string {
	s := `{"Id":"` + id + `","Name":"` + id + `","SeriesId":"s1","RunTimeTicks":18000000000,` +
		`"MediaSources":[{"Path":"/tv/` + id + `.mkv","Bitrate":8000000,"Size":1800000000}]`
	if season >= 0 {
		s += `,"ParentIndexNumber":` + string(rune('0'+season))
	}
	return s + `}`
}

func items(eps ...string) string {
	out := `{"Items":[`
	for i, e := range eps {
		if i > 0 {
			out += ","
		}
		out += e
	}
	return out + `]}`
}

func TestEpisodeAfter(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		item   string // the next-up item; "" means e3
		wantID string // "" means ok must be false
	}{
		{"next episode in the same season", items(ep("e3", 1), ep("e4", 1)), "", "e4"},
		{"finale crosses into the next season premiere", items(ep("s1e10", 1), ep("s2e01", 2)), "s1e10", "s2e01"},
		{"next-up not first is located by id", items(ep("e2", 1), ep("e3", 1), ep("e4", 1)), "", "e4"},
		{"next-up absent yields nothing rather than a guess", items(ep("e1", 1), ep("e2", 1)), "", ""},
		{"next-up is the last episode", items(ep("e3", 1)), "", ""},
		{"special (season 0) is skipped", items(ep("e3", 1), ep("sp1", 0)), "", ""},
		{"unknown season is not mistaken for a special", items(ep("e3", 1), ep("e4", -1)), "", "e4"},
		{"empty answer", items(), "", ""},
		{"successor with no media file yields nothing", items(ep("e3", 1), `{"Id":"e4","SeriesId":"s1","ParentIndexNumber":1}`), "", ""},
		{"successor with an empty path yields nothing", items(ep("e3", 1), `{"Id":"e4","SeriesId":"s1","MediaSources":[{"Path":""}]}`), "", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := episodeServer(t, tc.body)
			itemID := tc.item
			if itemID == "" {
				itemID = "e3"
			}
			got, ok, err := c.EpisodeAfter(context.Background(), "UserId", "u1", "s1", itemID)
			if err != nil {
				t.Fatal(err)
			}
			if tc.wantID == "" {
				if ok {
					t.Fatalf("got %+v, want nothing", got)
				}
				return
			}
			if !ok || got.ID != tc.wantID {
				t.Fatalf("got (%+v, %v), want %s", got, ok, tc.wantID)
			}
			if got.UserID != "u1" || got.SeriesID != "s1" || got.ServerPath != "/tv/"+tc.wantID+".mkv" ||
				got.BitrateBps != 8000000 || got.SizeBytes != 1800000000 {
				t.Errorf("decoded item = %+v, want path/bitrate/size/series/user filled", got)
			}
		})
	}
}

func TestEpisodeAfterRequest(t *testing.T) {
	c, ch := episodeServer(t, items())
	if _, _, err := c.EpisodeAfter(context.Background(), "userId", "u1", "ser9", "e3"); err != nil {
		t.Fatal(err)
	}
	got := <-ch
	if got.url.Path != "/Shows/ser9/Episodes" {
		t.Errorf("path = %q, want /Shows/ser9/Episodes", got.url.Path)
	}
	want := url.Values{
		"userId":      {"u1"},
		"StartItemId": {"e3"},
		"Limit":       {"2"},
		"IsMissing":   {"false"},
		"Fields":      {"Path,MediaSources"},
	}
	q := got.url.Query()
	for k, v := range want {
		if q.Get(k) != v[0] {
			t.Errorf("query %s = %q, want %q", k, q.Get(k), v[0])
		}
	}
	// The servers match query keys case-insensitively (Jellyfin's own spelling
	// is seasonId), so any casing of a season key is a restriction.
	for k := range q {
		if strings.EqualFold(k, "SeasonId") || strings.EqualFold(k, "Season") {
			t.Errorf("query %v restricts to one season; the finale must cross into the next", q)
		}
	}
	if got.auth != "k" {
		t.Error("request was not authenticated")
	}
}

func TestEpisodeAfterRejectsUnsafeSeriesID(t *testing.T) {
	c, ch := episodeServer(t, items())
	for _, id := range []string{"", ".", "..", "a/b", `a\b`, "a?b", "a#b"} {
		if _, _, err := c.EpisodeAfter(context.Background(), "UserId", "u1", id, "e3"); err == nil {
			t.Errorf("series id %q accepted", id)
		}
	}
	if len(ch) != 0 {
		t.Errorf("a rejected id still sent a request to %v", (<-ch).url)
	}
}

func TestEpisodeAfterPropagatesHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	t.Cleanup(srv.Close)
	c, err := New("test", srv.URL, func(*http.Request) {}, srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, ok, err := c.EpisodeAfter(context.Background(), "UserId", "u1", "s1", "e3"); err == nil || ok {
		t.Errorf("got (ok=%v, err=%v), want an error", ok, err)
	}
}
