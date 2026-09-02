package main

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/doxazo-net/watch-aware-preloader/internal/core"
)

func TestWriteUsersJSON(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUsersJSON([]core.User{{ID: "u1", Name: "Alice"}, {ID: "u2", Name: "Bob"}}, &buf); err != nil {
		t.Fatal(err)
	}
	var got []struct{ ID, Name string }
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0].ID != "u1" || got[0].Name != "Alice" || got[1].ID != "u2" {
		t.Errorf("unexpected users: %+v", got)
	}
}

func TestWriteUsersJSONEmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := writeUsersJSON(nil, &buf); err != nil {
		t.Fatal(err)
	}
	// Must be [] not null, so a strict UI consumer can iterate it.
	if got := bytes.TrimSpace(buf.Bytes()); string(got) != "[]" {
		t.Errorf("empty users = %q, want []", got)
	}
}

func TestWriteLibrariesJSON(t *testing.T) {
	var buf bytes.Buffer
	libs := []core.Library{{ID: "111", Name: "Movies", Type: "movies"}, {ID: "222", Name: "Music", Type: ""}}
	if err := writeLibrariesJSON(libs, &buf); err != nil {
		t.Fatal(err)
	}
	var got []struct{ ID, Name, Type string }
	if err := json.Unmarshal(buf.Bytes(), &got); err != nil {
		t.Fatalf("invalid JSON: %v\n%s", err, buf.String())
	}
	if len(got) != 2 || got[0].ID != "111" || got[0].Name != "Movies" || got[0].Type != "movies" {
		t.Errorf("unexpected libraries: %+v", got)
	}
	if got[1].Type != "" {
		t.Errorf("empty type should round-trip empty, got %q", got[1].Type)
	}
}

func TestWriteLibrariesJSONEmptyIsArray(t *testing.T) {
	var buf bytes.Buffer
	if err := writeLibrariesJSON(nil, &buf); err != nil {
		t.Fatal(err)
	}
	if got := bytes.TrimSpace(buf.Bytes()); string(got) != "[]" {
		t.Errorf("empty libraries = %q, want []", got)
	}
}
