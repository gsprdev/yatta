// Package remote defines the interface to a remote time system and holds its
// implementations.
package remote

import (
	"context"

	"github.com/gsprdev/yatta/internal/core"
)

// Adapter talks to one remote system. Create is the only write: upload is
// one-way, so records are never updated or deleted.
type Adapter interface {
	Integration() string
	// Verify confirms the credentials by asking the remote who they belong
	// to, and describes the account and API in use for the user to check.
	Verify(ctx context.Context) (string, error)
	FetchTasks(ctx context.Context) ([]core.FetchedTask, error)
	// Create uploads one unit against task and returns the remote system's
	// identifier for the new record.
	Create(ctx context.Context, task core.Task, u core.Unit) (remoteID string, err error)
}
