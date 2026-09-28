package core

import (
	"encoding/json"
	"time"
)

// Task is a node in either the local task hierarchy or a remote one.
// Remote == nil means the task is local.
type Task struct {
	ID       string
	Name     string
	ParentID string      // "" for roots
	Sort     int         // sibling ordering
	Archived *time.Time  // local tasks only; non-nil => hidden from the picker
	Remote   *RemoteTask // nil => local task
}

// RemoteTask holds the fields that only a task fetched from a remote system has.
type RemoteTask struct {
	Integration string          // "jira" | "redmine" | "toggl"
	NativeID    string          // id as known to the remote system
	Label       string          // human-facing identifier: "PROJ-123", "#4521"; "" if none
	NodeType    string          // "epic", "issue", "project", ...
	Extra       json.RawMessage // integration-specific fields, read only by the owning adapter
	Departed    *time.Time      // non-nil => absent from the latest fetch
}

func (t Task) IsRemote() bool { return t.Remote != nil }

// Selectable reports whether the task may be chosen for an entry. Archived
// (a local user decision) and departed (an upstream fact) both hide a task.
func (t Task) Selectable() bool {
	return t.Archived == nil && (t.Remote == nil || t.Remote.Departed == nil)
}

// FetchedTask is one node of a remote hierarchy as an adapter returns it,
// before it has a local ID. The store's reconciliation maps native IDs to
// local UUIDs, including the parent link.
type FetchedTask struct {
	NativeID       string
	ParentNativeID string // "" for roots
	Name           string
	Label          string
	NodeType       string
	Extra          json.RawMessage
}
