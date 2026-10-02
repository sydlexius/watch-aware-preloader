package secrets

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeSecrets(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "secrets.toml")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestAPIKeyEnvOverrideWins(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "env-key")
	// Nonexistent path: env must win without touching the file.
	got, err := APIKey(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != "env-key" {
		t.Errorf("got %q, want env-key", got)
	}
}

func TestAPIKeyWhitespaceEnvIsIgnored(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "   ")
	// Whitespace-only env must be treated as unset and fall through to the file.
	p := writeSecrets(t, "[server]\napi_key = \"file-key\"\n")
	got, err := APIKey(p)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != "file-key" {
		t.Errorf("got %q, want file-key (whitespace env must be ignored)", got)
	}
}

func TestAPIKeyWhitespaceFileKeyIsError(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "")
	p := writeSecrets(t, "[server]\napi_key = \"   \"\n")
	if _, err := APIKey(p); err == nil {
		t.Error("expected error for whitespace-only api_key in the secrets file")
	}
}

func TestAPIKeyFromFile(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "") // neutralize ambient override
	p := writeSecrets(t, "[server]\napi_key = \"file-key\"\n")
	got, err := APIKey(p)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != "file-key" {
		t.Errorf("got %q, want file-key", got)
	}
}

func TestAPIKeyMissingFileIsFriendlyError(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "")
	_, err := APIKey(filepath.Join(t.TempDir(), "absent.toml"))
	if err == nil {
		t.Fatal("expected error for missing file with no env var")
	}
	if !strings.Contains(err.Error(), "no media server API key found") {
		t.Errorf("error = %q, want it to mention 'no media server API key found'", err)
	}
}

func TestAPIKeyEmptyKeyIsError(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "")
	p := writeSecrets(t, "[server]\napi_key = \"\"\n")
	if _, err := APIKey(p); err == nil {
		t.Error("expected error for empty api_key")
	}
}

func TestAPIKeyUsersTableParses(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "")
	p := writeSecrets(t, "[server]\napi_key = \"k\"\n\n[users]\n\"3\" = \"tok3\"\n\"7\" = \"tok7\"\n")
	got, err := APIKey(p)
	if err != nil {
		t.Fatalf("APIKey with [users] table: %v", err)
	}
	if got != "k" {
		t.Errorf("got %q, want k", got)
	}
}

func TestAPIKeyEnvOverridesFileKey(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "env-key")
	p := writeSecrets(t, "[server]\napi_key = \"file-key\"\n")
	got, err := APIKey(p)
	if err != nil {
		t.Fatalf("APIKey: %v", err)
	}
	if got != "env-key" {
		t.Errorf("got %q, want env-key (env must override the file)", got)
	}
}

func TestAPIKeyInvalidTOMLIsGenericError(t *testing.T) {
	t.Setenv("EMBY_API_KEY", "")
	p := writeSecrets(t, "this is not valid toml =\n")
	_, err := APIKey(p)
	if err == nil {
		t.Fatal("expected error for invalid TOML")
	}
	if strings.Contains(err.Error(), "not valid TOML") == false {
		t.Errorf("error = %q, want generic 'not valid TOML' message", err)
	}
}
