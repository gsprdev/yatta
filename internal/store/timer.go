package store

import (
	"database/sql"
	"errors"
	"time"

	"github.com/gsprdev/yatta/internal/core"
)

// Timer returns the running timer, or nil if none is running.
func (s *Store) Timer() (*core.ActiveTimer, error) {
	var start int64
	var task sql.NullString
	err := s.db.QueryRow("SELECT start, task_id FROM active_timer WHERE id = 1").Scan(&start, &task)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &core.ActiveTimer{Start: fromUnix(start), TaskID: task.String}, nil
}

// StartTimer starts a timer at now against taskID ("" for no task yet). A
// timer already running is stopped at now first; its entry, if any, is
// returned.
func (s *Store) StartTimer(taskID string, now time.Time) (*core.TimeEntry, error) {
	var stopped string
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		if stopped, err = s.stopTimer(tx, now); err != nil {
			return err
		}
		_, err = tx.Exec("INSERT INTO active_timer (id, start, task_id) VALUES (1, ?, ?)", unix(now), nullStr(taskID))
		return err
	})
	return s.entryOrNil(stopped, err)
}

// SetTimerTask assigns a task to the running timer.
func (s *Store) SetTimerTask(taskID string) error {
	res, err := s.db.Exec("UPDATE active_timer SET task_id = ? WHERE id = 1", nullStr(taskID))
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return ErrNotFound
	}
	return nil
}

// StopTimer stops the running timer at now and returns the entry it produced.
// It returns nil when no timer was running, or when it ran for under a second.
func (s *Store) StopTimer(now time.Time) (*core.TimeEntry, error) {
	var stopped string
	err := s.tx(func(tx *sql.Tx) error {
		var err error
		stopped, err = s.stopTimer(tx, now)
		return err
	})
	return s.entryOrNil(stopped, err)
}

func (s *Store) stopTimer(tx *sql.Tx, now time.Time) (entryID string, err error) {
	var start int64
	var task sql.NullString
	err = tx.QueryRow("SELECT start, task_id FROM active_timer WHERE id = 1").Scan(&start, &task)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if _, err := tx.Exec("DELETE FROM active_timer WHERE id = 1"); err != nil {
		return "", err
	}
	dur := unix(now) - start
	if dur <= 0 {
		return "", nil
	}
	entryID = newID()
	ts := unix(s.now())
	_, err = tx.Exec(`INSERT INTO time_entries (id, start, duration_s, task_id, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?)`, entryID, start, dur, task, ts, ts)
	return entryID, err
}

func (s *Store) entryOrNil(id string, err error) (*core.TimeEntry, error) {
	if err != nil || id == "" {
		return nil, err
	}
	e, err := s.Entry(id)
	if err != nil {
		return nil, err
	}
	return &e, nil
}
