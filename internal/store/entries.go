package store

import (
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/gsprdev/yatta/internal/core"
)

const entryColumns = `
	e.id, e.start, e.duration_s, e.task_id, e.note, e.remote_record_id, e.upload_error,
	e.created_at, e.updated_at, t.integration, r.id IS NOT NULL AND r.remote_id IS NULL`

const entryFrom = `
	FROM time_entries e
	LEFT JOIN tasks t ON t.id = e.task_id
	LEFT JOIN remote_records r ON r.id = e.remote_record_id`

type scanner interface{ Scan(...any) error }

// scanEntry reads one row of entryColumns and materializes the entry's upload
// state from its task and record link.
func scanEntry(row scanner) (core.TimeEntry, error) {
	var (
		e                             core.TimeEntry
		start, dur, created, updated  int64
		task, note, record, uploadErr sql.NullString
		integration                   sql.NullString
		sentinel                      sql.NullBool
	)
	if err := row.Scan(&e.ID, &start, &dur, &task, &note, &record, &uploadErr,
		&created, &updated, &integration, &sentinel); err != nil {
		return e, err
	}
	e.Start = fromUnix(start)
	e.Duration = time.Duration(dur) * time.Second
	e.TaskID = task.String
	e.Note = note.String
	e.CreatedAt = fromUnix(created)
	e.UpdatedAt = fromUnix(updated)

	if !integration.Valid { // local task, or unassigned
		if record.Valid || uploadErr.Valid {
			return e, fmt.Errorf("store invariant violated: entry %s is not on a remote task but has upload state", e.ID)
		}
		return e, nil
	}
	switch {
	case record.Valid && sentinel.Bool:
		e.Upload = &core.UploadState{Phase: core.Excluded, RecordID: record.String}
	case record.Valid:
		e.Upload = &core.UploadState{Phase: core.Uploaded, RecordID: record.String}
	case uploadErr.Valid:
		e.Upload = &core.UploadState{Phase: core.Failed, Err: uploadErr.String}
	default:
		e.Upload = &core.UploadState{Phase: core.Pending}
	}
	return e, nil
}

func (s *Store) queryEntries(where string, args ...any) ([]core.TimeEntry, error) {
	rows, err := s.db.Query("SELECT "+entryColumns+entryFrom+" "+where, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.TimeEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// Entries returns entries starting in [from, to), most recent first. A zero
// from or to leaves that side unbounded.
func (s *Store) Entries(from, to time.Time) ([]core.TimeEntry, error) {
	lo, hi := int64(-1<<62), int64(1<<62)
	if !from.IsZero() {
		lo = unix(from)
	}
	if !to.IsZero() {
		hi = unix(to)
	}
	return s.queryEntries("WHERE e.start >= ? AND e.start < ? ORDER BY e.start DESC, e.id", lo, hi)
}

func (s *Store) Entry(id string) (core.TimeEntry, error) {
	e, err := scanEntry(s.db.QueryRow("SELECT "+entryColumns+entryFrom+" WHERE e.id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return e, ErrNotFound
	}
	return e, err
}

// UnlockedRemoteEntries returns every pending or failed entry on a remote task
// starting on or before through (zero means no bound): the input to
// core.PlanUpload.
func (s *Store) UnlockedRemoteEntries(through time.Time) ([]core.TimeEntry, error) {
	hi := int64(1 << 62)
	if !through.IsZero() {
		hi = unix(through)
	}
	return s.queryEntries(`WHERE e.remote_record_id IS NULL AND t.integration IS NOT NULL
		AND e.start <= ? ORDER BY e.start, e.id`, hi)
}

// SaveEntry creates the entry when its ID is empty, otherwise updates its
// start, duration, task, and note. It returns the entry as stored. A locked
// entry cannot be saved. Moving a failed entry off a remote task clears its
// upload error, since only remote-task entries carry upload state.
func (s *Store) SaveEntry(e core.TimeEntry) (core.TimeEntry, error) {
	secs := seconds(e.Duration)
	if secs <= 0 {
		return e, fmt.Errorf("entry duration must be at least one second")
	}
	now := unix(s.now())
	if e.ID == "" {
		e.ID = newID()
		_, err := s.db.Exec(`INSERT INTO time_entries (id, start, duration_s, task_id, note, created_at, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			e.ID, unix(e.Start), secs, nullStr(e.TaskID), nullStr(e.Note), now, now)
		if err != nil {
			return e, err
		}
		return s.Entry(e.ID)
	}
	res, err := s.db.Exec(`UPDATE time_entries SET start = ?, duration_s = ?, task_id = ?, note = ?, updated_at = ?,
			upload_error = CASE WHEN (SELECT integration FROM tasks WHERE id = ?) IS NULL THEN NULL ELSE upload_error END
		WHERE id = ? AND remote_record_id IS NULL`,
		unix(e.Start), secs, nullStr(e.TaskID), nullStr(e.Note), now, e.TaskID, e.ID)
	if err != nil {
		return e, err
	}
	if err := s.checkUnlockedWrite(res, e.ID); err != nil {
		return e, err
	}
	return s.Entry(e.ID)
}

// DiscardEntry deletes an entry that has not been uploaded.
func (s *Store) DiscardEntry(id string) error {
	res, err := s.db.Exec("DELETE FROM time_entries WHERE id = ? AND remote_record_id IS NULL", id)
	if err != nil {
		return err
	}
	return s.checkUnlockedWrite(res, id)
}

// checkUnlockedWrite turns a write that touched no row into ErrLocked or
// ErrNotFound.
func (s *Store) checkUnlockedWrite(res sql.Result, id string) error {
	n, err := res.RowsAffected()
	if err != nil || n == 1 {
		return err
	}
	var exists bool
	if err := s.db.QueryRow("SELECT EXISTS (SELECT 1 FROM time_entries WHERE id = ?)", id).Scan(&exists); err != nil {
		return err
	}
	if exists {
		return ErrLocked
	}
	return ErrNotFound
}

// Record returns one remote record, for the merged local/remote view.
func (s *Store) Record(id string) (core.RemoteRecord, error) {
	var (
		r               core.RemoteRecord
		remoteID, note  sql.NullString
		start, dur, cre int64
	)
	err := s.db.QueryRow(`SELECT id, remote_id, integration, target_task_id, uploaded_start,
		uploaded_duration_s, note, created_at FROM remote_records WHERE id = ?`, id).
		Scan(&r.ID, &remoteID, &r.Integration, &r.TaskID, &start, &dur, &note, &cre)
	if errors.Is(err, sql.ErrNoRows) {
		return r, ErrNotFound
	}
	r.RemoteID = remoteID.String
	r.Note = note.String
	r.Start = fromUnix(start)
	r.Duration = time.Duration(dur) * time.Second
	r.CreatedAt = fromUnix(cre)
	return r, err
}

// RecordUpload writes the record for a successfully uploaded unit and links
// every member to it, locking them.
func (s *Store) RecordUpload(u core.Unit, integration, remoteID string) error {
	if remoteID == "" {
		return fmt.Errorf("RecordUpload needs a remote ID; use RecordExcluded for exclusions")
	}
	return s.writeRecord(u, integration, remoteID)
}

// RecordExcluded writes a sentinel record for a unit excluded by the policy and
// links every member to it, locking them.
func (s *Store) RecordExcluded(u core.Unit, integration string) error {
	return s.writeRecord(u, integration, "")
}

func (s *Store) writeRecord(u core.Unit, integration, remoteID string) error {
	if len(u.Entries) == 0 {
		return fmt.Errorf("unit has no entries")
	}
	return s.tx(func(tx *sql.Tx) error {
		id := newID()
		_, err := tx.Exec(`INSERT INTO remote_records (id, remote_id, integration, target_task_id,
			uploaded_start, uploaded_duration_s, note, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			id, nullStr(remoteID), integration, u.TaskID, unix(u.Start), seconds(u.Duration),
			nullStr(u.Note), unix(s.now()))
		if err != nil {
			return err
		}
		args := []any{id}
		for _, e := range u.Entries {
			args = append(args, e.ID)
		}
		res, err := tx.Exec(`UPDATE time_entries SET remote_record_id = ?, upload_error = NULL
			WHERE remote_record_id IS NULL AND id IN (`+placeholders(len(u.Entries))+`)`, args...)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); int(n) != len(u.Entries) {
			return fmt.Errorf("linked %d of %d entries: some are missing or already locked", n, len(u.Entries))
		}
		return nil
	})
}

// RecordFailure stores the upload error on every member of a unit. The
// entries stay unlinked and editable.
func (s *Store) RecordFailure(u core.Unit, uploadErr error) error {
	msg := "upload failed"
	if uploadErr != nil && uploadErr.Error() != "" {
		msg = uploadErr.Error()
	}
	args := []any{msg}
	for _, e := range u.Entries {
		args = append(args, e.ID)
	}
	_, err := s.db.Exec(`UPDATE time_entries SET upload_error = ?
		WHERE remote_record_id IS NULL AND id IN (`+placeholders(len(u.Entries))+`)`, args...)
	return err
}

// Counts are the attention counts shown in the status bar.
type Counts struct {
	Pending    int // unlinked remote-task entries with no error
	Failed     int // unlinked remote-task entries whose last upload failed
	Departed   int // unlinked entries whose remote task has departed
	Unassigned int // entries with no task
}

func (s *Store) Counts() (Counts, error) {
	var c Counts
	err := s.db.QueryRow(`SELECT
			COALESCE(SUM(t.integration IS NOT NULL AND e.upload_error IS NULL), 0),
			COALESCE(SUM(t.integration IS NOT NULL AND e.upload_error IS NOT NULL), 0),
			COALESCE(SUM(t.departed_at IS NOT NULL), 0),
			COALESCE(SUM(e.task_id IS NULL), 0)
		FROM time_entries e LEFT JOIN tasks t ON t.id = e.task_id
		WHERE e.remote_record_id IS NULL`).Scan(&c.Pending, &c.Failed, &c.Departed, &c.Unassigned)
	return c, err
}
