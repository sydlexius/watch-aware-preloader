package main

import (
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/emby"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/jellyfin"
)

func TestNewProviderPicksTheConfiguredAdapter(t *testing.T) {
	p, err := newProvider(config.ServerConfig{Type: config.ServerEmby, URL: "http://h:8096"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*emby.Client); !ok {
		t.Errorf("type emby built %T", p)
	}
	p, err = newProvider(config.ServerConfig{Type: config.ServerJellyfin, URL: "http://h:8096"}, "k")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.(*jellyfin.Client); !ok {
		t.Errorf("type jellyfin built %T", p)
	}
	if _, err := newProvider(config.ServerConfig{Type: "plex", URL: "http://h:8096"}, "k"); err == nil {
		t.Error("an unknown server.type should be an error")
	}
}
