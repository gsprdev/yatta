package core

import "time"

// TimeEntry is the authoritative local record of a block of time.
type TimeEntry struct {
	ID        string
	Start     time.Time // UTC
	Duration  time.Duration
	TaskID    string // "" => unassigned; allowed, never uploaded
	Note      string
	Upload    *UploadState // nil => entry is on a local task, or unassigned
	CreatedAt time.Time
	UpdatedAt time.Time
}

func (e TimeEntry) End() time.Time { return e.Start.Add(e.Duration) }

// Locked reports that the entry has been uploaded or excluded and can no
// longer be changed or discarded.
func (e TimeEntry) Locked() bool { return e.Upload != nil && e.Upload.Locked() }

type Phase string

const (
	Pending  Phase = "pending"  // not yet uploaded; editable
	Failed   Phase = "failed"   // last upload attempt rejected; editable, retried next upload
	Uploaded Phase = "uploaded" // linked to a record that was sent; locked
	Excluded Phase = "excluded" // linked to a sentinel: below minimum, nothing sent; locked
)

// UploadState is the upload condition of an entry on a remote task.
type UploadState struct {
	Phase    Phase
	Err      string // non-empty only when Phase == Failed
	RecordID string // "" until uploaded or excluded
}

func (u UploadState) Locked() bool { return u.RecordID != "" }

// ActiveTimer is the running timer. It survives restarts.
type ActiveTimer struct {
	Start  time.Time
	TaskID string // "" => started before a task was chosen
}
