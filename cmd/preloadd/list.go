package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"

	"github.com/doxazo-net/watch-aware-preloader/internal/config"
	"github.com/doxazo-net/watch-aware-preloader/internal/core"
	"github.com/doxazo-net/watch-aware-preloader/internal/mediaserver/emby"
	"github.com/doxazo-net/watch-aware-preloader/internal/secrets"
)

// dispatchSubcommand runs a read-only diagnostic subcommand (list-users,
// list-libraries, detect-pathmaps) when args name one, and reports whether it
// handled the invocation. A handled subcommand writes JSON to stdout and exits
// non-zero on failure; the caller then returns without entering the run modes.
func dispatchSubcommand(args []string) bool {
	if len(args) < 2 {
		return false
	}
	switch args[1] {
	case "list-users", "list-libraries":
		runListSubcommand(args[1], configPathFromArgs(args[2:]))
		return true
	case "detect-pathmaps":
		runDetectSubcommand(configPathFromArgs(args[2:]))
		return true
	}
	return false
}

// runListSubcommand queries the configured Emby server and emits list-users /
// list-libraries JSON. Read-only; the API key is never written to output.
func runListSubcommand(name, cfgPath string) {
	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
	client, err := newEmbyClient(cfgPath)
	if err != nil {
		log.Error(name+" failed: client init", "err", err)
		os.Exit(1)
	}
	ctx := context.Background()
	switch name {
	case "list-users":
		users, uerr := client.Users(ctx)
		if uerr == nil {
			uerr = writeUsersJSON(users, os.Stdout)
		}
		err = uerr
	case "list-libraries":
		libs, lerr := client.Libraries(ctx)
		if lerr == nil {
			lerr = writeLibrariesJSON(libs, os.Stdout)
		}
		err = lerr
	}
	if err != nil {
		log.Error(name+" failed", "err", err)
		os.Exit(1)
	}
}

// newEmbyClient builds a read-only Emby client from the config at cfgPath and
// its secret store. Used by the list-users / list-libraries diagnostic
// subcommands (the server-query backend the settings UI renders). The API key
// is loaded into the client but never written to output.
func newEmbyClient(cfgPath string) (*emby.Client, error) {
	cfg, err := config.Load(cfgPath)
	if err != nil {
		return nil, err
	}
	apiKey, err := secrets.APIKey(cfg.SecretPath)
	if err != nil {
		return nil, err
	}
	return emby.New(cfg.Server.URL, apiKey, nil)
}

type userJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type libraryJSON struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
}

// writeUsersJSON emits the users as `[{"id","name"}]`, always a JSON array
// (never null when empty) so a strict UI consumer can iterate it directly.
func writeUsersJSON(users []core.User, w io.Writer) error {
	out := make([]userJSON, 0, len(users))
	for _, u := range users {
		out = append(out, userJSON{ID: u.ID, Name: u.Name})
	}
	return writeJSON(out, w)
}

// writeLibrariesJSON emits the libraries as `[{"id","name","type"}]`, always a
// JSON array (never null when empty).
func writeLibrariesJSON(libs []core.Library, w io.Writer) error {
	out := make([]libraryJSON, 0, len(libs))
	for _, l := range libs {
		out = append(out, libraryJSON{ID: l.ID, Name: l.Name, Type: l.Type})
	}
	return writeJSON(out, w)
}

func writeJSON(v any, w io.Writer) error {
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}
