// Package mbapi is the HTTP core shared by the Emby and Jellyfin adapters.
//
// Both servers descend from MediaBrowser and still speak the same wire format:
// the same item DTOs, the same tick unit, the same /Users, /Library and
// /Sessions shapes. What differs is how a request authenticates and which
// route serves a given query, so those stay in the vendor packages and
// everything else - in particular the base-URL trust boundary and the refusal
// to follow redirects - lives here once, rather than as two copies that can
// drift apart.
package mbapi

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// Authenticator attaches the vendor's credential to an outgoing request.
type Authenticator func(req *http.Request)

// Client talks to a single MediaBrowser-family server.
type Client struct {
	vendor     string
	base       *url.URL
	auth       Authenticator
	httpClient *http.Client
}

// ValidateBaseURL enforces the media-server trust boundary. The configured base
// URL must be a plain absolute http/https URL with a host and nothing that could
// divert a request elsewhere: no opaque form, embedded credentials, query, or
// fragment. It returns the normalized base (scheme/host/path only, trailing
// slash trimmed) so request URLs can be built with url.URL.JoinPath rather than
// string concatenation, which keeps a server-supplied path element from
// escaping the configured host.
//
// Private/LAN hosts are intentionally allowed: this tool's purpose is to talk to
// a media server that normally lives on the local network, so blocking RFC1918
// addresses would break the documented setup rather than harden it.
func ValidateBaseURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("parsing base URL: %w", err)
	}
	if u.Opaque != "" {
		return nil, fmt.Errorf("base URL must be a plain absolute URL, not opaque")
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return nil, fmt.Errorf("base URL scheme must be http or https, got %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return nil, fmt.Errorf("base URL has no host")
	}
	if u.User != nil || u.RawQuery != "" || u.Fragment != "" {
		return nil, fmt.Errorf("base URL must not contain credentials, query, or fragment")
	}
	if p := u.Port(); p != "" {
		if n, err := strconv.Atoi(p); err != nil || n < 1 || n > 65535 {
			return nil, fmt.Errorf("base URL has invalid port %q", p)
		}
	}
	return &url.URL{Scheme: u.Scheme, Host: u.Host, Path: strings.TrimRight(u.Path, "/")}, nil
}

// New validates the base URL at the trust boundary and returns a Client. vendor
// names the server in error messages; auth attaches the credential to each
// request and must not be nil.
func New(vendor, baseURL string, auth Authenticator, httpClient *http.Client) (*Client, error) {
	base, err := ValidateBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if auth == nil {
		return nil, fmt.Errorf("an authenticator is required")
	}
	if httpClient == nil {
		httpClient = &http.Client{Timeout: 15 * time.Second}
	}
	// Refuse to follow redirects. net/http re-sends custom headers such as
	// X-Emby-Token on a cross-host 30x hop, which would leak the API key to
	// whatever host the redirect names. Authorization is stripped on a
	// cross-host hop, but refusing every redirect keeps the guarantee
	// independent of which header a vendor uses. Copy the client so the
	// caller's value is not mutated.
	hc := *httpClient
	hc.CheckRedirect = func(_ *http.Request, _ []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &Client{vendor: vendor, base: base, auth: auth, httpClient: &hc}, nil
}

// Get issues an authenticated GET for path (joined to the validated base) and
// decodes a 200 response into out. A nil out skips decoding.
func (c *Client) Get(ctx context.Context, path string, query url.Values, out any) error {
	// JoinPath joins and cleans the path against the validated base, so a path
	// element can never redirect the request to another host.
	u := c.base.JoinPath(path)
	if len(query) > 0 {
		u.RawQuery = query.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), http.NoBody)
	if err != nil {
		return err
	}
	c.auth(req)
	req.Header.Set("Accept", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close() //nolint:errcheck // reason: close error on response body is not actionable

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%s GET %s: status %d", c.vendor, path, resp.StatusCode)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}
