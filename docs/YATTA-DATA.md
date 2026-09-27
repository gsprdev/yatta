# YATTA — Data Model

## Overview

YATTA persists to SQLite via [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — a pure-Go driver, so the binary builds with `CGO_ENABLED=0` and cross-compiles without a C toolchain. SQL is hand-written; there is no ORM and no generated data layer.

The schema sits close to the domain types in `internal/core` — one `Task` type to one table, one `TimeEntry` type to one table — so the mapping code in `internal/store` stays thin. Where the schema diverges from the domain, it is because relational storage genuinely wants something different, and the divergence is noted.

Connection setup:

```sql
PRAGMA foreign_keys = ON;
PRAGMA journal_mode = WAL;
PRAGMA busy_timeout = 5000;
```

Foreign keys are enforced. WAL suits a long-running single-process application and keeps the database readable by external tools while YATTA is open, which matters for a local file the owner may reasonably want to inspect.

### Time Representation

- Instants are stored as **Unix seconds, UTC**. Durations are stored as **whole seconds**.
- `end` is derived in the domain and never stored.
- Local timezone is applied only at render time and when computing the aggregation day boundary. It is never persisted.
- All IDs are locally generated UUIDs.

Seconds rather than a finer unit, for two reasons. Nothing in the domain carries sub-second meaning: the smallest rounding increment is fifteen minutes and the timer displays to the second. And seconds is the epoch SQLite's own date functions expect, so `datetime(start, 'unixepoch', 'localtime')` works directly in an ad-hoc query against a local, single-user database. Conversion in Go is `time.Unix(sec, 0).UTC()` and `time.Duration(sec) * time.Second`.

---

## Schema

### `tasks`

One table holds both local and remote tasks. `integration IS NULL` means the task is local, mapping exactly to `core.Task.Remote == nil`.

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID, for local and remote tasks alike |
| `name` | TEXT NOT NULL | display name |
| `parent_id` | TEXT | nullable self-FK; null for roots |
| `sort_order` | INTEGER NOT NULL DEFAULT 0 | sibling ordering |
| `archived_at` | INTEGER | nullable; local tasks only. non-null ⇒ archived |
| `integration` | TEXT | nullable. NULL ⇒ local task. `'jira'` \| `'redmine'` \| `'toggl'` |
| `native_id` | TEXT | id as known to the remote system; NULL for local tasks |
| `label` | TEXT | nullable; human-facing identifier shown and searched in the picker (`'PROJ-123'`, `'#4521'`); NULL for local tasks and for remotes without one |
| `node_type` | TEXT | integration-specific label (`'epic'`, `'issue'`, …); NULL for local |
| `extra` | TEXT | JSON blob of integration-specific fields; NULL for local |
| `departed_at` | INTEGER | nullable; remote tasks only. non-null ⇒ absent from the latest fetch |
| `fetched_at` | INTEGER | nullable; remote tasks only |
| `created_at` | INTEGER NOT NULL | Unix seconds |

**Constraints:**

```sql
CHECK (integration IS NULL OR native_id IS NOT NULL)
CHECK (integration IS NOT NULL OR (native_id IS NULL AND label IS NULL AND node_type IS NULL
       AND extra IS NULL AND departed_at IS NULL AND fetched_at IS NULL))
CHECK (integration IS NULL OR archived_at IS NULL)
```

**Why one table.** Local and remote tasks share the structure that matters to every consumer — name, parent, ordering — and differ only in provenance. Putting them together makes `time_entries.task_id` a single enforced foreign key rather than a polymorphic reference that SQLite cannot check, which matters because entry-to-task is the most important relationship in the database: a dangling reference would otherwise surface as a missing task at render time rather than as a failed insert. It also makes reassociating an entry from a departed remote task to a local task an ordinary column update rather than a change of referent table, which is exactly the flow `YATTA.md` requires after an upstream deletion.

The cost is a wider table with columns that apply to only one kind of row, and one rule that becomes application-enforced:

- **`parent_id` never crosses the namespace boundary.** A local task's parent is local; a remote task's parent is remote and shares its integration. This is the "distinct namespaces" rule from `YATTA.md`, and it is covered by a store test rather than by the schema.

**Reconciliation on fetch:**

- Rows present in the fetch are upserted on `(integration, native_id)`; a previously departed row that reappears has `departed_at` cleared.
- Rows absent from the fetch but still referenced — by a `time_entries` row, a `remote_records` row, or `active_timer` — are soft-deleted with `departed_at = now`. They stay resolvable for historical display and are excluded from the picker.
- Rows absent from the fetch and unreferenced are hard-deleted. A departed row whose last reference is purged is therefore removed by the next reconciliation.

---

### `time_entries`

The authoritative local record. Editable, and discardable, until it is linked to a remote record; then locked. The upload process only ever sets the link.

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID |
| `start` | INTEGER NOT NULL | Unix seconds, UTC |
| `duration_s` | INTEGER NOT NULL | block length in seconds |
| `task_id` | TEXT | nullable; `REFERENCES tasks(id)`. NULL ⇒ unassigned: allowed, never uploaded, counted for attention |
| `note` | TEXT | nullable |
| `remote_record_id` | TEXT | nullable; `REFERENCES remote_records(id)`. NULL until the entry is uploaded or excluded; non-null ⇒ locked. **Several rows may share one value** — that is how aggregation is represented. |
| `upload_error` | TEXT | nullable; the error from the last failed upload attempt. Meaningful **only** while `remote_record_id IS NULL` |
| `created_at` | INTEGER NOT NULL | Unix seconds |
| `updated_at` | INTEGER NOT NULL | Unix seconds |

**Constraints:**

```sql
CHECK (duration_s > 0)
CHECK (remote_record_id IS NULL OR upload_error IS NULL)
```

The second constraint makes the mutual exclusion structural: a linked entry cannot carry a stale error, because it cannot carry one at all. Linking and clearing the error happen in the same statement.

Application-enforced:

- An entry whose task is local or unassigned has `remote_record_id` and `upload_error` both NULL. This requires a cross-table check, so it is a store invariant with a test rather than a `CHECK`.
- Discarding deletes an unlinked entry. The store refuses to discard a linked one; only purge deletes linked entries.

Trigger-enforced — locking is the product's promise about uploaded time, so it is structural, and it also holds against hand-edits of the database:

```sql
CREATE TRIGGER entries_locked BEFORE UPDATE ON time_entries
WHEN OLD.remote_record_id IS NOT NULL
BEGIN SELECT RAISE(ABORT, 'entry is locked: already uploaded'); END;
```

Linking is an update of an unlinked row, so the trigger does not block it.

**Reconstructing `core.TimeEntry.Upload`:**

| Task | `remote_record_id` | Other | Resulting `UploadState` |
|---|---|---|---|
| none | NULL | — | `nil` |
| local | NULL | — | `nil` |
| remote | NULL | `upload_error` NULL | `{Pending, "", ""}` |
| remote | NULL | `upload_error` set | `{Failed, upload_error, ""}` |
| remote | set | `remote_records.remote_id` set | `{Uploaded, "", recordID}` |
| remote | set | `remote_records.remote_id` NULL | `{Excluded, "", recordID}` |

---

### `remote_records`

What was actually sent — the post-aggregation, post-rounding projection — or, for an exclusion, the sentinel recording that nothing was sent. Written once, on success or exclusion, and never modified. A row may be referenced by several `time_entries` rows.

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID |
| `remote_id` | TEXT | opaque identifier from the remote system; interpreted only by the adapter. **NULL ⇒ excluded sentinel**: handled, nothing uploaded |
| `integration` | TEXT NOT NULL | `'jira'` \| `'redmine'` \| `'toggl'` |
| `target_task_id` | TEXT NOT NULL | `REFERENCES tasks(id)`; the task this record was uploaded under |
| `uploaded_start` | INTEGER NOT NULL | Unix seconds — earliest member's start |
| `uploaded_duration_s` | INTEGER NOT NULL | seconds, the members' summed duration with the policy applied; for a sentinel, the value that fell below the minimum |
| `note` | TEXT | nullable; distinct member notes, concatenated |
| `created_at` | INTEGER NOT NULL | Unix seconds of the upload or exclusion |

**Notes:**

- There is no phase or error column. A row exists only once its upload succeeded or its exclusion was confirmed; failures stay on the entries, which remain unlinked and editable.
- No foreign key points from here back to a single entry, since there may be several.
- Group membership is not stored as a list; it is the set of entries whose `remote_record_id` points here, fixed at upload.

---

### `active_timer`

Single-row table holding the running timer, so it survives a restart and an accidental terminal close does not lose an in-flight block.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | always `1`; `CHECK (id = 1)` |
| `start` | INTEGER NOT NULL | Unix seconds, UTC |
| `task_id` | TEXT | nullable; `REFERENCES tasks(id)`. A timer may run before its task is chosen |

On stop, the row is consumed: a `time_entries` row is created with `duration_s = now - start`, and the timer row is deleted.

---

### `integration_config`

At most one row.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | always `1`; `CHECK (id = 1)` |
| `integration` | TEXT NOT NULL | `'jira'` \| `'redmine'` \| `'toggl'` |
| `base_url` | TEXT | nullable; required for Jira/Redmine, unused by Toggl |
| `keyring_key` | TEXT NOT NULL | account key within the `yatta` keyring service |
| `last_fetch_at` | INTEGER | nullable, Unix seconds |

**Credentials are never stored in SQLite.** Tokens and passwords live in the OS keyring via `internal/secret`, addressed by service `yatta` and account `keyring_key`. Deleting this row disables the integration; its tasks are then reconciled against an empty fetch, so unreferenced remote tasks are removed and referenced ones soft-delete to `departed_at`, keeping existing entries' history intact and surfacing them for reassociation.

---

### `settings`

| Column | Type | Notes |
|---|---|---|
| `key` | TEXT PK | setting identifier |
| `value` | TEXT NOT NULL | JSON-encoded value |

| Key | Value | Description |
|---|---|---|
| `rounding_increment` | `"none"` \| `"quarter"` \| `"half"` \| `"hour"` | increment |
| `rounding_direction` | `"nearest"` \| `"up"` \| `"down"` | direction |
| `minimum_duration_s` | integer \| `null` | minimum entry or group duration |
| `below_minimum_behavior` | `"round_up"` \| `"exclude"` \| `null` | below-minimum behavior |
| `rounding_aggregate` | `"none"` \| `"task_day"` | aggregation grouping key |

---

## Indexes

```sql
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
```

`idx_entries_start DESC` backs the resting view, which is the most-recent-first list. `idx_entries_unlinked` backs the pending, failed, departed, and unassigned counts in the always-visible status bar, which are recomputed on every mutation. `idx_tasks_native` is what makes reconciliation an upsert.

---

## Purge

The user removes old local data by choosing a cutoff date. Purge deletes every `time_entries` row starting before that local date, in any state, then any `remote_records` no longer referenced by an entry, in one transaction. Tasks are not purged directly: a departed remote task left unreferenced is hard-deleted by the next reconciliation, and local tasks are managed by the user.

Before confirming, the store counts the unlinked entries in the range so the interface can warn: failed entries (`upload_error IS NOT NULL`) get the louder warning; pending and unassigned entries the plainer one.

---

## Migrations

Numbered `.sql` files embedded with `embed.FS`, applied by a stepper over `PRAGMA user_version`. No migration framework: the runner is short enough to read in one sitting and adds no dependency, which matters for a tool distributed as a single binary.

```
internal/store/migrations/
├── 0001_initial.sql
└── ...
```

Each file is applied in a transaction, with `user_version` bumped in the same transaction. Non-additive changes are written explicitly with data preservation; there is no automatic destructive migration.

---

## Resolved Decisions

- **One `tasks` table for local and remote tasks.** Makes `time_entries.task_id` an enforced foreign key, and makes local↔remote reassociation an ordinary update. Costs nullable columns and leaves namespace separation to the application layer.
- **Unix seconds for instants and durations.** The domain has no sub-second meaning, and seconds is the epoch SQLite's date functions expect, which keeps the database directly inspectable.
- **`upload_error` on entries**, with a `CHECK` making its mutual exclusion with `remote_record_id` structural rather than conventional. Pending and failed are the only states an unlinked entry has, and they differ only by whether an error is present.
- **A remote record may be referenced by multiple entries**, expressed as a plain nullable FK rather than a join table, because an entry still targets at most one record. A join table would only be needed if an entry could feed several remotes at once, which `YATTA.md` rejects.
- **Remote records are write-once.** Upload is one-way, so a record has no lifecycle: it is created on success or exclusion and never changed. There is no record phase and no table of records awaiting upstream deletion.
- **Exclusions are sentinel records** (`remote_id IS NULL`), so an excluded entry is linked, locked, and no longer pending, through the same mechanism as an uploaded one.
- **Stale remote task references are reconciled, not wiped.** Referenced departed rows are retained with `departed_at`; unreferenced ones are hard-deleted. No display-name snapshot on entries is needed, since the retained row keeps both the reference and the display intact.
- **Foreign keys and WAL enabled** at connection setup.
- **Unassigned is a NULL `task_id`**, not a default bucket task. A bucket would be a special local task that must never be renamed, archived, or given children; NULL is already how SQLite says "no reference", and it maps to `TaskID == ""` in the domain.
- **Locking is a trigger; the local-task rule stays a store invariant.** Locking protects uploaded time, including from hand-edits, and costs one trigger. The local-task rule needs a cross-table check on every write and guards nothing the remote has seen.
- **`label` is a column; other integration fields stay in `extra`.** The picker filters on the label constantly, so it is worth a column; the rest is read only by the owning adapter. Revisit `extra` if the three integrations' fields diverge enough that the blob obscures more than it saves.

## Open Questions

None at present.
