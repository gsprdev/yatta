package core

import "time"

// RemoteRecord is what an upload produced: the projection actually sent, or
// for an exclusion, a sentinel recording that nothing was sent. Written once
// and never modified.
type RemoteRecord struct {
	ID          string // local UUID
	RemoteID    string // opaque; only the owning adapter interprets it. "" => excluded sentinel
	Integration string
	TaskID      string        // the task this record was uploaded under
	Start       time.Time     // earliest member's start
	Duration    time.Duration // members' summed duration, policy applied
	Note        string        // member notes combined
	CreatedAt   time.Time
}

func (r RemoteRecord) Excluded() bool { return r.RemoteID == "" }
