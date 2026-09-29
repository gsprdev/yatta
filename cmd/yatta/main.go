// Command yatta is a terminal time tracker.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/remote"
	"github.com/gsprdev/yatta/internal/remote/jira"
	"github.com/gsprdev/yatta/internal/remote/redmine"
	"github.com/gsprdev/yatta/internal/remote/toggl"
	"github.com/gsprdev/yatta/internal/secret"
	"github.com/gsprdev/yatta/internal/store"
	"github.com/gsprdev/yatta/internal/ui"
)

func main() {
	dbPath := flag.String("db", "", "database file (default: yatta.db in the user data directory)")
	debug := flag.Bool("debug", false, "append every request to the remote system, and its response, to debug.log beside the database (credentials are left out)")
	flag.Parse()
	if err := run(*dbPath, *debug); err != nil {
		fmt.Fprintln(os.Stderr, "yatta:", err)
		os.Exit(1)
	}
}

func run(dbPath string, debug bool) error {
	if dbPath == "" {
		dir, err := dataDir()
		if err != nil {
			return err
		}
		dbPath = filepath.Join(dir, "yatta.db")
	}
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o700); err != nil {
		return err
	}
	if debug {
		path := filepath.Join(filepath.Dir(dbPath), "debug.log")
		f, err := os.OpenFile(path, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
		if err != nil {
			return err
		}
		defer f.Close()
		// The adapters use the default client.
		http.DefaultClient.Transport = remote.Trace(f, http.DefaultTransport)
		defer fmt.Fprintln(os.Stderr, "yatta: remote requests were logged to", path)
	}
	st, err := store.Open(dbPath)
	if err != nil {
		return err
	}
	defer st.Close()

	keys := secret.Keyring{}
	connect := func(cfg *store.IntegrationConfig) (remote.Adapter, error) {
		raw, err := keys.Get(cfg.KeyringKey)
		if err != nil {
			return nil, fmt.Errorf("credential for %s: %w", cfg.Integration, err)
		}
		var cred ui.Credential
		if err := json.Unmarshal([]byte(raw), &cred); err != nil {
			return nil, fmt.Errorf("credential for %s is unreadable: %w", cfg.Integration, err)
		}
		return newAdapter(cfg, cred)
	}

	_, err = tea.NewProgram(ui.New(st, connect, keys, time.Local), tea.WithAltScreen()).Run()
	return err
}

// dataDir is the per-user data directory: $XDG_DATA_HOME/yatta (default
// ~/.local/share/yatta) on Linux and other Unix, Application Support on
// macOS, %AppData% on Windows.
func dataDir() (string, error) {
	switch runtime.GOOS {
	case "darwin", "windows":
		dir, err := os.UserConfigDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(dir, "yatta"), nil
	}
	if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
		return filepath.Join(dir, "yatta"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "yatta"), nil
}

func newAdapter(cfg *store.IntegrationConfig, cred ui.Credential) (remote.Adapter, error) {
	switch cfg.Integration {
	case "jira":
		// What kind of Jira the site is decides the route and authentication,
		// so it is asked each time rather than stored.
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		site, err := jira.Probe(ctx, cfg.BaseURL)
		if err != nil {
			return nil, fmt.Errorf("could not reach %s: %w", cfg.BaseURL, err)
		}
		return jira.New(site, cred.User, cred.Token, cfg.TaskQuery), nil
	case "redmine":
		return redmine.New(cfg.BaseURL, cred.Token, cfg.TaskQuery, time.Local)
	case "toggl":
		return toggl.New("", cred.Token), nil
	}
	return nil, fmt.Errorf("unknown integration %q", cfg.Integration)
}
