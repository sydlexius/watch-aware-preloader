// Package emby is a minimal read-only client for the Emby API.
package emby

import (
	"fmt"
	"net/http"

	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/mbapi"
)

// Client talks to a single Emby server with an API key.
type Client struct {
	api *mbapi.Client
}

// New validates the base URL at the trust boundary and returns a Client.
func New(baseURL, apiKey string, httpClient *http.Client) (*Client, error) {
	if apiKey == "" {
		return nil, fmt.Errorf("api key is required")
	}
	// Emby authenticates an API key through X-Emby-Token. mbapi refuses to
	// follow redirects, so the header is never replayed to another host.
	auth := func(req *http.Request) { req.Header.Set("X-Emby-Token", apiKey) }
	api, err := mbapi.New("emby", baseURL, auth, httpClient)
	if err != nil {
		return nil, err
	}
	return &Client{api: api}, nil
}
