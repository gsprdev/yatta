package store

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/gsprdev/yatta/internal/core"
)

var increments = map[string]time.Duration{
	"none": 0, "quarter": 15 * time.Minute, "half": 30 * time.Minute, "hour": time.Hour,
}

// Policy returns the stored upload policy; unset keys take their defaults.
func (s *Store) Policy() (core.Policy, error) {
	var p core.Policy
	vals := map[string]json.RawMessage{}
	rows, err := s.db.Query("SELECT key, value FROM settings")
	if err != nil {
		return p, err
	}
	defer rows.Close()
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return p, err
		}
		vals[k] = json.RawMessage(v)
	}
	if err := rows.Err(); err != nil {
		return p, err
	}
	str := func(key string) (string, error) {
		var v *string
		if raw, ok := vals[key]; ok {
			if err := json.Unmarshal(raw, &v); err != nil {
				return "", fmt.Errorf("setting %s: %w", key, err)
			}
		}
		if v == nil {
			return "", nil
		}
		return *v, nil
	}

	inc, err := str("rounding_increment")
	if err != nil {
		return p, err
	}
	d, ok := increments[inc]
	if inc != "" && !ok {
		return p, fmt.Errorf("setting rounding_increment: unknown value %q", inc)
	}
	p.Increment = d
	dir, err := str("rounding_direction")
	if err != nil {
		return p, err
	}
	p.Direction = core.Direction(dir)
	below, err := str("below_minimum_behavior")
	if err != nil {
		return p, err
	}
	p.BelowMin = core.BelowMin(below)
	agg, err := str("rounding_aggregate")
	if err != nil {
		return p, err
	}
	p.Aggregate = core.AggKey(agg)
	if raw, ok := vals["minimum_duration_s"]; ok {
		var secs *int64
		if err := json.Unmarshal(raw, &secs); err != nil {
			return p, fmt.Errorf("setting minimum_duration_s: %w", err)
		}
		if secs != nil {
			p.Minimum = time.Duration(*secs) * time.Second
		}
	}
	return p, p.Validate()
}

// SetPolicy validates and stores the upload policy.
func (s *Store) SetPolicy(p core.Policy) error {
	if err := p.Validate(); err != nil {
		return err
	}
	inc := ""
	for name, d := range increments {
		if d == p.Increment {
			inc = name
		}
	}
	if inc == "" {
		return fmt.Errorf("rounding increment must be none, 15, 30, or 60 minutes")
	}
	var minimum, below any
	if p.Minimum > 0 {
		minimum, below = seconds(p.Minimum), string(p.BelowMin)
	}
	vals := map[string]any{
		"rounding_increment":     inc,
		"rounding_direction":     orDefault(string(p.Direction), string(core.Nearest)),
		"minimum_duration_s":     minimum,
		"below_minimum_behavior": below,
		"rounding_aggregate":     orDefault(string(p.Aggregate), string(core.AggNone)),
	}
	return s.tx(func(tx *sql.Tx) error {
		for k, v := range vals {
			b, err := json.Marshal(v)
			if err != nil {
				return err
			}
			if _, err := tx.Exec(`INSERT INTO settings (key, value) VALUES (?, ?)
				ON CONFLICT (key) DO UPDATE SET value = excluded.value`, k, string(b)); err != nil {
				return err
			}
		}
		return nil
	})
}

func orDefault(v, def string) string {
	if v == "" {
		return def
	}
	return v
}

// IntegrationConfig is the one configured remote integration. The credential
// itself lives in the OS keyring under KeyringKey.
type IntegrationConfig struct {
	Integration string // "jira" | "redmine" | "toggl"
	BaseURL     string // required for Jira and Redmine
	KeyringKey  string
	LastFetchAt *time.Time
}

// Integration returns the configured integration, or nil if none is.
func (s *Store) Integration() (*IntegrationConfig, error) {
	var (
		c         IntegrationConfig
		base      sql.NullString
		lastFetch sql.NullInt64
	)
	err := s.db.QueryRow(`SELECT integration, base_url, keyring_key, last_fetch_at
		FROM integration_config WHERE id = 1`).Scan(&c.Integration, &base, &c.KeyringKey, &lastFetch)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.BaseURL = base.String
	c.LastFetchAt = timePtr(lastFetch)
	return &c, nil
}

// SetIntegration stores the integration configuration. Switching to a
// different integration first retires the previous one's tasks, as does
// ClearIntegration.
func (s *Store) SetIntegration(c IntegrationConfig) error {
	if err := s.retireOtherIntegration(c.Integration); err != nil {
		return err
	}
	_, err := s.db.Exec(`INSERT INTO integration_config (id, integration, base_url, keyring_key, last_fetch_at)
		VALUES (1, ?, ?, ?, ?)
		ON CONFLICT (id) DO UPDATE SET integration = excluded.integration, base_url = excluded.base_url,
			keyring_key = excluded.keyring_key, last_fetch_at = excluded.last_fetch_at`,
		c.Integration, nullStr(c.BaseURL), c.KeyringKey, nullTime(c.LastFetchAt))
	return err
}

// ClearIntegration removes the integration. Its tasks are reconciled against
// an empty fetch: unreferenced ones are deleted, referenced ones depart.
func (s *Store) ClearIntegration() error {
	if err := s.retireOtherIntegration(""); err != nil {
		return err
	}
	_, err := s.db.Exec("DELETE FROM integration_config")
	return err
}

func (s *Store) retireOtherIntegration(keep string) error {
	cur, err := s.Integration()
	if err != nil || cur == nil || cur.Integration == keep {
		return err
	}
	return s.ReconcileRemoteTasks(cur.Integration, nil)
}

// MarkFetched records when the remote task hierarchy was last fetched.
func (s *Store) MarkFetched(at time.Time) error {
	_, err := s.db.Exec("UPDATE integration_config SET last_fetch_at = ? WHERE id = 1", unix(at))
	return err
}
