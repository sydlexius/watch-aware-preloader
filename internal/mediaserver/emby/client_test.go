package emby

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestNewRejectsBadURL(t *testing.T) {
	for _, bad := range []string{
		"", "ftp://x", "http://user:pw@host", "http://h/?q=1",
		"mailto:a@b",     // opaque, not a plain absolute URL
		"http://h:99999", // port out of range
		"http://h#frag",  // fragment
	} {
		if _, err := New(bad, "k", nil); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
}

func TestNewAcceptsPrivateLANHost(t *testing.T) {
	// The media server normally lives on the LAN; private hosts must be accepted.
	if _, err := New("http://tower:8096", "k", nil); err != nil {
		t.Errorf("private LAN base URL should be accepted, got %v", err)
	}
}

func TestGetBuildsRequestPathSafely(t *testing.T) {
	// Verify JoinPath produces the expected endpoint (no host escape, no double slash).
	pathCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		pathCh <- r.URL.Path
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
	if got := <-pathCh; got != "/Users/u1/Items/Resume" {
		t.Errorf("request path = %q, want /Users/u1/Items/Resume", got)
	}
}

func TestGetPathCannotEscapeHost(t *testing.T) {
	// A server-supplied path element with traversal / host-injection attempts must
	// still reach the configured host, never another (JoinPath cleans the path).
	hostCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hostCh <- r.Host
		_, _ = w.Write([]byte(`{"Items":[]}`))
	}))
	defer srv.Close()
	c, err := New(srv.URL, "k", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Resume(context.Background(), "../../@evil.com/x"); err != nil {
		t.Fatal(err)
	}
	if got := <-hostCh; got != srv.Listener.Addr().String() {
		t.Errorf("request reached host %q, want %q (path must not escape host)", got, srv.Listener.Addr().String())
	}
}

func TestGetSendsTokenAndDecodes(t *testing.T) {
	tokenCh := make(chan string, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		tokenCh <- r.Header.Get("X-Emby-Token")
		_, _ = w.Write([]byte(`{"Value":42}`))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	var out struct{ Value int }
	if err := c.api.Get(context.Background(), "/Test", nil, &out); err != nil {
		t.Fatal(err)
	}
	if gotToken := <-tokenCh; gotToken != "secret" {
		t.Errorf("X-Emby-Token = %q, want secret", gotToken)
	}
	if out.Value != 42 {
		t.Errorf("decoded Value = %d, want 42", out.Value)
	}
}

func TestGetErrorsOnNon200(t *testing.T) {
	// A non-200 status must surface as an error and never attempt to decode the
	// body into out.
	for _, code := range []int{http.StatusNotFound, http.StatusInternalServerError, http.StatusUnauthorized} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(code)
		}))
		c, err := New(srv.URL, "secret", srv.Client())
		if err != nil {
			srv.Close()
			t.Fatal(err)
		}
		var out struct{ Value int }
		if err := c.api.Get(context.Background(), "/x", nil, &out); err == nil {
			t.Errorf("status %d: expected error, got nil", code)
		}
		srv.Close()
	}
}

func TestGetNilOutSkipsDecode(t *testing.T) {
	// A 200 with out == nil is a fire-and-forget request: it must succeed
	// without touching the (here non-JSON) body.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte("not json"))
	}))
	defer srv.Close()

	c, err := New(srv.URL, "secret", srv.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.api.Get(context.Background(), "/x", nil, nil); err != nil {
		t.Errorf("nil out should skip decode and succeed, got %v", err)
	}
}

func TestGetDoesNotFollowRedirect(t *testing.T) {
	// X-Emby-Token would be re-sent on a cross-host redirect; the client must
	// refuse to follow, so the redirect target never sees the API key.
	leaked := make(chan string, 1)
	target := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		leaked <- r.Header.Get("X-Emby-Token")
	}))
	defer target.Close()
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer redirector.Close()

	c, err := New(redirector.URL, "secret", redirector.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := c.api.Get(context.Background(), "/x", nil, nil); err == nil {
		t.Error("expected error: redirect must not be followed")
	}
	select {
	case tok := <-leaked:
		t.Errorf("redirect was followed; token leaked to other host: %q", tok)
	default:
	}
}
