// Command yatta is a terminal time tracker.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/remote"
	"github.com/gsprdev/yatta/internal/secret"
	"github.com/gsprdev/yatta/internal/store"
	"github.com/gsprdev/yatta/internal/ui"
)

func main() {
	dbPath := flag.String("db", "", "database file (default: yatta.db in the user data directory)")
	flag.Parse()
	if err := run(*dbPath); err != nil {
		fmt.Fprintln(os.Stderr, "yatta:", err)
		os.Exit(1)
	}
}

func run(dbPath string) error {
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
	return nil, fmt.Errorf("the %s integration is not built yet", cfg.Integration)
}
