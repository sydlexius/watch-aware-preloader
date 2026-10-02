// Package secrets loads preloadd credentials from a secret-only store (a TOML
// file or the EMBY_API_KEY env var), kept separate from config.toml so
// credentials never live in the main configuration file.
package secrets

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/BurntSushi/toml"
)

// Store is the on-disk secrets file. Users is reserved for issue #18 (per-user
// access tokens keyed by Emby UserID) and is parsed but not consumed yet.
type Store struct {
	Server struct {
		APIKey string `toml:"api_key"`
	} `toml:"server"`
	Users map[string]string `toml:"users"`
}

// APIKey resolves the media server's (Emby or Jellyfin) admin API key. The
// EMBY_API_KEY env var wins when set (dev/CI/headless); otherwise the key is read
// from secretPath's [server].api_key.
// A missing file (with no env var) or an empty key yields a friendly error naming
// both sources.
func APIKey(secretPath string) (string, error) {
	// TrimSpace both sources so an accidental whitespace-only value (e.g. a
	// stray space in the env var or file) is treated as unset rather than
	// silently passed to the server, which would reject it.
	if k := strings.TrimSpace(os.Getenv("EMBY_API_KEY")); k != "" {
		return k, nil
	}
	var s Store
	if _, err := toml.DecodeFile(secretPath, &s); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", notFoundErr(secretPath)
		}
		var perr toml.ParseError
		if errors.As(err, &perr) {
			// Do not wrap: a TOML parse error can echo the offending source
			// line, which may contain the api_key value.
			return "", fmt.Errorf("secrets file %s is not valid TOML", secretPath)
		}
		return "", fmt.Errorf("reading secrets file %s: %w", secretPath, err)
	}
	key := strings.TrimSpace(s.Server.APIKey)
	if key == "" {
		return "", notFoundErr(secretPath)
	}
	return key, nil
}

func notFoundErr(secretPath string) error {
	return fmt.Errorf("no media server API key found; set [server].api_key in %s or the EMBY_API_KEY env var", secretPath)
}
