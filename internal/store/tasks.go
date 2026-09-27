package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/gsprdev/yatta/internal/core"
)

const taskColumns = `id, name, parent_id, sort_order, archived_at, integration, native_id,
	label, node_type, extra, departed_at`

func scanTask(row scanner) (core.Task, error) {
	var (
		t                                     core.Task
		parent, integ, native, label, nt, ext sql.NullString
		archived, departed                    sql.NullInt64
	)
	if err := row.Scan(&t.ID, &t.Name, &parent, &t.Sort, &archived, &integ, &native,
		&label, &nt, &ext, &departed); err != nil {
		return t, err
	}
	t.ParentID = parent.String
	t.Archived = timePtr(archived)
	if integ.Valid {
		t.Remote = &core.RemoteTask{
			Integration: integ.String,
			NativeID:    native.String,
			Label:       label.String,
			NodeType:    nt.String,
			Departed:    timePtr(departed),
		}
		if ext.Valid {
			t.Remote.Extra = json.RawMessage(ext.String)
		}
	}
	return t, nil
}

// Tasks returns every task, local and remote, including archived and departed
// ones, ordered for display within each parent.
func (s *Store) Tasks() ([]core.Task, error) {
	rows, err := s.db.Query("SELECT " + taskColumns + " FROM tasks ORDER BY sort_order, name, id")
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []core.Task
	for rows.Next() {
		t, err := scanTask(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

func (s *Store) Task(id string) (core.Task, error) {
	t, err := scanTask(s.db.QueryRow("SELECT "+taskColumns+" FROM tasks WHERE id = ?", id))
	if errors.Is(err, sql.ErrNoRows) {
		return t, ErrNotFound
	}
	return t, err
}

// SaveLocalTask creates a local task when its ID is empty, otherwise updates
// its name, parent, ordering, and archived state. A local task's parent must
// be local, and a task cannot be moved beneath itself.
func (s *Store) SaveLocalTask(t core.Task) (core.Task, error) {
	if t.Remote != nil {
		return t, fmt.Errorf("remote tasks are managed by reconciliation, not edited")
	}
	if t.Name == "" {
		return t, fmt.Errorf("task name is required")
	}
	err := s.tx(func(tx *sql.Tx) error {
		for p := t.ParentID; p != ""; {
			if p == t.ID {
				return fmt.Errorf("a task cannot be moved beneath itself")
			}
			var parent sql.NullString
			var integ sql.NullString
			err := tx.QueryRow("SELECT parent_id, integration FROM tasks WHERE id = ?", p).Scan(&parent, &integ)
			if errors.Is(err, sql.ErrNoRows) {
				return fmt.Errorf("parent task %s: %w", p, ErrNotFound)
			}
			if err != nil {
				return err
			}
			if integ.Valid {
				return fmt.Errorf("a local task cannot be placed under a remote task")
			}
			p = parent.String
		}
		if t.ID == "" {
			t.ID = newID()
			_, err := tx.Exec(`INSERT INTO tasks (id, name, parent_id, sort_order, archived_at, created_at)
				VALUES (?, ?, ?, ?, ?, ?)`,
				t.ID, t.Name, nullStr(t.ParentID), t.Sort, nullTime(t.Archived), unix(s.now()))
			return err
		}
		res, err := tx.Exec(`UPDATE tasks SET name = ?, parent_id = ?, sort_order = ?, archived_at = ?
			WHERE id = ? AND integration IS NULL`,
			t.Name, nullStr(t.ParentID), t.Sort, nullTime(t.Archived), t.ID)
		if err != nil {
			return err
		}
		if n, _ := res.RowsAffected(); n != 1 {
			return fmt.Errorf("local task %s: %w", t.ID, ErrNotFound)
		}
		return nil
	})
	if err != nil {
		return t, err
	}
	return s.Task(t.ID)
}

// ReconcileRemoteTasks brings the stored hierarchy for one integration in line
// with a fresh fetch. Fetched tasks are upserted on their native ID, and a
// departed task that reappears is restored. Tasks missing from the fetch are
// hard-deleted when nothing refers to them, and otherwise marked departed so
// entries keep their reference. Pass a nil fetch to retire an integration.
func (s *Store) ReconcileRemoteTasks(integration string, fetched []core.FetchedTask) error {
	now := unix(s.now())
	return s.tx(func(tx *sql.Tx) error {
		existing := map[string]string{} // native ID -> local ID
		rows, err := tx.Query("SELECT native_id, id FROM tasks WHERE integration = ?", integration)
		if err != nil {
			return err
		}
		for rows.Next() {
			var native, id string
			if err := rows.Scan(&native, &id); err != nil {
				rows.Close()
				return err
			}
			existing[native] = id
		}
		rows.Close()
		if err := rows.Err(); err != nil {
			return err
		}

		ids := map[string]string{} // native ID -> local ID, for this fetch
		for i, f := range fetched {
			if f.NativeID == "" {
				return fmt.Errorf("fetched task %q has no native ID", f.Name)
			}
			var extra sql.NullString
			if len(f.Extra) > 0 {
				extra = sql.NullString{String: string(f.Extra), Valid: true}
			}
			id, ok := existing[f.NativeID]
			if ok {
				_, err = tx.Exec(`UPDATE tasks SET name = ?, sort_order = ?, label = ?, node_type = ?, extra = ?,
					departed_at = NULL, fetched_at = ? WHERE id = ?`,
					f.Name, i, nullStr(f.Label), nullStr(f.NodeType), extra, now, id)
			} else {
				id = newID()
				_, err = tx.Exec(`INSERT INTO tasks (id, name, sort_order, integration, native_id, label,
					node_type, extra, fetched_at, created_at) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
					id, f.Name, i, integration, f.NativeID, nullStr(f.Label), nullStr(f.NodeType), extra, now, now)
			}
			if err != nil {
				return err
			}
			ids[f.NativeID] = id
		}
		// Parents are set once every fetched task has an ID. A parent missing
		// from the fetch makes the child a root.
		for _, f := range fetched {
			_, err := tx.Exec("UPDATE tasks SET parent_id = ? WHERE id = ?",
				nullStr(ids[f.ParentNativeID]), ids[f.NativeID])
			if err != nil {
				return err
			}
		}

		// Absent tasks: delete those nothing refers to, leaves first, until no
		// more can go; the rest depart.
		var absent []string
		for native, id := range existing {
			if _, ok := ids[native]; !ok {
				absent = append(absent, id)
			}
		}
		for deleted := true; deleted && len(absent) > 0; {
			deleted = false
			remaining := absent[:0]
			for _, id := range absent {
				res, err := tx.Exec(`DELETE FROM tasks WHERE id = ?
					AND NOT EXISTS (SELECT 1 FROM time_entries WHERE task_id = ?)
					AND NOT EXISTS (SELECT 1 FROM remote_records WHERE target_task_id = ?)
					AND NOT EXISTS (SELECT 1 FROM active_timer WHERE task_id = ?)
					AND NOT EXISTS (SELECT 1 FROM tasks WHERE parent_id = ?)`, id, id, id, id, id)
				if err != nil {
					return err
				}
				if n, _ := res.RowsAffected(); n == 1 {
					deleted = true
				} else {
					remaining = append(remaining, id)
				}
			}
			absent = remaining
		}
		for _, id := range absent {
			if _, err := tx.Exec("UPDATE tasks SET departed_at = COALESCE(departed_at, ?) WHERE id = ?", now, id); err != nil {
				return err
			}
		}
		return nil
	})
}
