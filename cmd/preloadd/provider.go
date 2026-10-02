package main

import (
	"fmt"

	"github.com/doxazo-net/watch-aware-preloader/internal/app"
	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/emby"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/jellyfin"
)

// newProvider builds the media-server adapter server.type names. The choice is
// made here, not in internal/app, so the pipeline never imports a vendor
// package (TestAppDoesNotImportAVendorPackage).
func newProvider(server config.ServerConfig, apiKey string) (app.Provider, error) {
	switch server.Type {
	case config.ServerEmby:
		return emby.New(server.URL, apiKey, nil)
	case config.ServerJellyfin:
		return jellyfin.New(server.URL, apiKey, nil)
	default:
		return nil, fmt.Errorf("unsupported server.type %q", server.Type)
	}
}
