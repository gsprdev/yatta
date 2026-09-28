// Package secret stores credentials in the OS keyring: Keychain on macOS,
// Secret Service on Linux, Credential Manager on Windows. The database stores
// only the key.
package secret

import (
	"errors"
	"fmt"

	"github.com/zalando/go-keyring"
)

const service = "yatta"

// ErrNotFound means no credential is stored under the key.
var ErrNotFound = errors.New("no credential stored in the keyring")

// Keyring is the OS keyring under the "yatta" service.
type Keyring struct{}

func (Keyring) Set(key, value string) error {
	if err := keyring.Set(service, key, value); err != nil {
		return fmt.Errorf("OS keyring unavailable or refused the write (on Linux, a Secret Service such as gnome-keyring must be running): %w", err)
	}
	return nil
}

func (Keyring) Get(key string) (string, error) {
	v, err := keyring.Get(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return "", ErrNotFound
	}
	return v, err
}

func (Keyring) Delete(key string) error {
	err := keyring.Delete(service, key)
	if errors.Is(err, keyring.ErrNotFound) {
		return nil
	}
	return err
}
