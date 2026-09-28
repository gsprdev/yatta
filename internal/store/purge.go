package store

import (
	"database/sql"
	"time"
)

// PurgeCounts reports the never-uploaded entries a purge before the given
// instant would remove, so the interface can warn: failed entries loudly,
// pending and unassigned ones plainly. Entries on local tasks are not counted.
func (s *Store) PurgeCounts(before time.Time) (failed, pending, unassigned int, err error) {
	err = s.db.QueryRow(`SELECT
			COALESCE(SUM(t.integration IS NOT NULL AND e.upload_error IS NOT NULL), 0),
			COALESCE(SUM(t.integration IS NOT NULL AND e.upload_error IS NULL), 0),
			COALESCE(SUM(e.task_id IS NULL), 0)
		FROM time_entries e LEFT JOIN tasks t ON t.id = e.task_id
		WHERE e.remote_record_id IS NULL AND e.start < ?`, unix(before)).Scan(&failed, &pending, &unassigned)
	return
}

// Purge deletes every entry starting before the given instant, in any state,
// and the remote records no entry refers to any more. Pass the start of the
// user's chosen local date.
func (s *Store) Purge(before time.Time) error {
	return s.tx(func(tx *sql.Tx) error {
		if _, err := tx.Exec("DELETE FROM time_entries WHERE start < ?", unix(before)); err != nil {
			return err
		}
		_, err := tx.Exec(`DELETE FROM remote_records WHERE id NOT IN
			(SELECT remote_record_id FROM time_entries WHERE remote_record_id IS NOT NULL)`)
		return err
	})
}
