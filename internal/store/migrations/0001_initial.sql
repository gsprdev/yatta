CREATE TABLE tasks (
    id          TEXT PRIMARY KEY,
    name        TEXT NOT NULL,
    parent_id   TEXT REFERENCES tasks(id),
    sort_order  INTEGER NOT NULL DEFAULT 0,
    archived_at INTEGER,
    integration TEXT CHECK (integration IS NULL OR integration IN ('jira','redmine','toggl')),
    native_id   TEXT,
    label       TEXT,
    node_type   TEXT,
    extra       TEXT,
    departed_at INTEGER,
    fetched_at  INTEGER,
    created_at  INTEGER NOT NULL,
    CHECK (integration IS NULL OR native_id IS NOT NULL),
    CHECK (integration IS NOT NULL OR (native_id IS NULL AND label IS NULL AND node_type IS NULL
           AND extra IS NULL AND departed_at IS NULL AND fetched_at IS NULL)),
    CHECK (integration IS NULL OR archived_at IS NULL)
);

CREATE TABLE remote_records (
    id                  TEXT PRIMARY KEY,
    remote_id           TEXT,
    integration         TEXT NOT NULL CHECK (integration IN ('jira','redmine','toggl')),
    target_task_id      TEXT NOT NULL REFERENCES tasks(id),
    uploaded_start      INTEGER NOT NULL,
    uploaded_duration_s INTEGER NOT NULL,
    note                TEXT,
    created_at          INTEGER NOT NULL
);

CREATE TABLE time_entries (
    id               TEXT PRIMARY KEY,
    start            INTEGER NOT NULL,
    duration_s       INTEGER NOT NULL,
    task_id          TEXT REFERENCES tasks(id),
    note             TEXT,
    remote_record_id TEXT REFERENCES remote_records(id),
    upload_error     TEXT,
    created_at       INTEGER NOT NULL,
    updated_at       INTEGER NOT NULL,
    CHECK (duration_s > 0),
    CHECK (remote_record_id IS NULL OR upload_error IS NULL)
);

CREATE TRIGGER entries_locked BEFORE UPDATE ON time_entries
WHEN OLD.remote_record_id IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'entry is locked: already uploaded'); END;

CREATE TABLE active_timer (
    id      INTEGER PRIMARY KEY CHECK (id = 1),
    start   INTEGER NOT NULL,
    task_id TEXT REFERENCES tasks(id)
);

CREATE TABLE integration_config (
    id            INTEGER PRIMARY KEY CHECK (id = 1),
    integration   TEXT NOT NULL CHECK (integration IN ('jira','redmine','toggl')),
    base_url      TEXT,
    keyring_key   TEXT NOT NULL,
    task_query    TEXT,
    last_fetch_at INTEGER
);

CREATE TABLE settings (
    key   TEXT PRIMARY KEY,
    value TEXT NOT NULL
);

CREATE INDEX idx_entries_start          ON time_entries(start DESC);
CREATE INDEX idx_entries_task           ON time_entries(task_id);
CREATE INDEX idx_entries_record         ON time_entries(remote_record_id)
                                        WHERE remote_record_id IS NOT NULL;
CREATE INDEX idx_entries_unlinked       ON time_entries(task_id)
                                        WHERE remote_record_id IS NULL;
CREATE INDEX idx_tasks_parent           ON tasks(parent_id);
CREATE INDEX idx_tasks_departed         ON tasks(departed_at) WHERE departed_at IS NOT NULL;
CREATE UNIQUE INDEX idx_tasks_native    ON tasks(integration, native_id)
                                        WHERE integration IS NOT NULL;
