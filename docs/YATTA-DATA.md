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
| `node_type` | TEXT | integration-specific label (`'epic'`, `'issue'`, …); NULL for local |
| `extra` | TEXT | JSON blob of integration-specific fields; NULL for local |
| `departed_at` | INTEGER | nullable; remote tasks only. non-null ⇒ absent from the latest fetch |
| `fetched_at` | INTEGER | nullable; remote tasks only |
| `created_at` | INTEGER NOT NULL | Unix seconds |

**Constraints:**

```sql
CHECK (integration IS NULL OR native_id IS NOT NULL)
CHECK (integration IS NOT NULL OR (native_id IS NULL AND node_type IS NULL
       AND extra IS NULL AND departed_at IS NULL AND fetched_at IS NULL))
CHECK (integration IS NULL OR archived_at IS NULL)
```

**Why one table.** Local and remote tasks share the structure that matters to every consumer — name, parent, ordering — and differ only in provenance. Putting them together makes `time_entries.task_id` a single enforced foreign key rather than a polymorphic reference that SQLite cannot check, which matters because entry-to-task is the most important relationship in the database: a dangling reference would otherwise surface as a missing task at render time rather than as a failed insert. It also makes reassociating an entry from a departed remote task to a local task an ordinary column update rather than a change of referent table, which is exactly the flow `YATTA.md` requires after an upstream deletion.

The cost is a wider table with columns that apply to only one kind of row, and one rule that becomes application-enforced:

- **`parent_id` never crosses the namespace boundary.** A local task's parent is local; a remote task's parent is remote and shares its integration. This is the "distinct namespaces" rule from `YATTA.md`, and it is covered by a store test rather than by the schema.

**Reconciliation on fetch:**

- Rows present in the fetch are upserted on `(integration, native_id)`; a previously departed row that reappears has `departed_at` cleared.
- Rows absent from the fetch but still referenced — by a `time_entries` row, a `remote_records` row, an `orphaned_remote_records` row, or `active_timer` — are soft-deleted with `departed_at = now`. They stay resolvable for historical display and are excluded from the picker.
- Rows absent from the fetch and unreferenced are hard-deleted.

Orphaned records count as referents: an orphan's `target_task_id` supplies the delete context for an upstream call that has not happened yet, so hard-deleting that task would break a deletion still owed to the remote system.

---

### `time_entries`

The authoritative local record. Never modified by the upload process.

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID |
| `start` | INTEGER NOT NULL | Unix seconds, UTC |
| `duration_s` | INTEGER NOT NULL | block length in seconds |
| `task_id` | TEXT NOT NULL | `REFERENCES tasks(id)` |
| `note` | TEXT | nullable |
| `remote_record_id` | TEXT | nullable; `REFERENCES remote_records(id)`. NULL for entries on local tasks, and for remote-task entries not yet part of a successful upload. **Several rows may share one value** — that is how aggregation is represented. |
| `unlinked_phase` | TEXT | nullable; `'pending'` \| `'failed'`. Meaningful **only** while `remote_record_id IS NULL` |
| `unlinked_error` | TEXT | nullable; set when `unlinked_phase = 'failed'` |
| `created_at` | INTEGER NOT NULL | Unix seconds |
| `updated_at` | INTEGER NOT NULL | Unix seconds |

The `unlinked_` prefix states in the column name when these apply, so a reader does not have to remember the rule. Once an entry is linked to a record, its phase and error come from that record, which is what keeps aggregated siblings in agreement.

**Constraints:**

```sql
CHECK (duration_s > 0)
CHECK (remote_record_id IS NULL OR (unlinked_phase IS NULL AND unlinked_error IS NULL))
CHECK (unlinked_phase IS NULL OR unlinked_phase IN ('pending','failed'))
```

The second constraint makes the mutual exclusion structural: a linked entry cannot carry a stale phase, because it cannot carry one at all.

Application-enforced:

- An entry whose task is local has `remote_record_id`, `unlinked_phase`, and `unlinked_error` all NULL. This requires a cross-table check, so it is a store invariant with a test rather than a `CHECK`.

**Reconstructing `core.TimeEntry.Upload`:**

| Task | `remote_record_id` | Source of phase | Resulting `UploadState` |
|---|---|---|---|
| local | NULL | — | `nil` |
| remote | NULL | `unlinked_phase` NULL or `'pending'` | `{Pending, "", ""}` |
| remote | NULL | `unlinked_phase = 'failed'` | `{Failed, unlinked_error, ""}` |
| remote | set | `remote_records.phase = 'uploaded'` | `{Uploaded, "", recordID}` |
| remote | set | `remote_records.phase = 'pending'` | `{Pending, "", recordID}` — diverged |
| remote | set | `remote_records.phase = 'failed'` | `{Failed, record.error, recordID}` |

Every row sharing a `remote_record_id` reads its phase from the same `remote_records` row, which is the mechanism behind "the group shares one fate."

---

### `remote_records`

What was actually sent — the post-rounding, post-aggregation projection. A row may be referenced by several `time_entries` rows.

| Column | Type | Notes |
|---|---|---|
| `id` | TEXT PK | UUID |
| `remote_id` | TEXT NOT NULL | opaque identifier from the remote system; interpreted only by the adapter |
| `integration` | TEXT NOT NULL | `'jira'` \| `'redmine'` \| `'toggl'` |
| `target_task_id` | TEXT NOT NULL | `REFERENCES tasks(id)`; the task this record was uploaded under |
| `uploaded_start` | INTEGER NOT NULL | Unix seconds, post-rounding — earliest member's rounded start |
| `uploaded_duration_s` | INTEGER NOT NULL | seconds, post-rounding — sum of the group's rounded durations |
| `phase` | TEXT NOT NULL | `'uploaded'` \| `'pending'` \| `'failed'` — a group-level fact |
| `error` | TEXT | nullable; set when `phase = 'failed'` |
| `uploaded_at` | INTEGER NOT NULL | Unix seconds of the last successful upload |

**Notes:**

- No foreign key points from here back to a single entry, since there may be several.
- Editing, adding, or removing a member sets `phase = 'pending'` **at the moment of the local change**, not deferred to the next upload attempt, so divergence is visible immediately in every linked member's derived status.
- Group membership is never stored as a list. Re-uploading a `pending` or `failed` record recomputes `uploaded_start` and `uploaded_duration_s` from whichever entries currently target this record's task and day, and upserts in place, keeping `id` and `remote_id`.
- **Not** cascade-deleted with its members. When the last entry linking to a record is deleted or reassociated away, the record moves to `orphaned_remote_records` so its upstream deletion still happens. While other members remain, it is recomputed and updated in place instead.

---

### `orphaned_remote_records`

Records that still exist upstream but that no local entry targets any more. These are deletions awaiting the next upload, decoupled from `time_entries` precisely because the entries may be gone.

| Column | Type | Notes |
|---|---|---|
| `remote_id` | TEXT PK | the upstream record to delete |
| `integration` | TEXT NOT NULL | |
| `target_task_id` | TEXT NOT NULL | `REFERENCES tasks(id)`; supplies delete context, e.g. the Jira issue |
| `orphaned_at` | INTEGER NOT NULL | Unix seconds |

A record is orphaned only when its **last** member leaves. On upload each row is deleted upstream via the adapter, then removed here; a failed deletion stays for the next attempt. Because `target_task_id` may reference a departed task, that reference must stay resolvable — which is why orphans count as referents during reconciliation.

---

### `active_timer`

Single-row table holding the running timer, so it survives a restart and an accidental terminal close does not lose an in-flight block.

| Column | Type | Notes |
|---|---|---|
| `id` | INTEGER PK | always `1`; `CHECK (id = 1)` |
| `start` | INTEGER NOT NULL | Unix seconds, UTC |
| `task_id` | TEXT NOT NULL | `REFERENCES tasks(id)` |

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
CREATE INDEX idx_entries_unlinked_phase ON time_entries(unlinked_phase)
                                        WHERE unlinked_phase IS NOT NULL;
CREATE INDEX idx_records_phase          ON remote_records(phase) WHERE phase <> 'uploaded';
CREATE INDEX idx_tasks_parent           ON tasks(parent_id);
CREATE INDEX idx_tasks_departed         ON tasks(departed_at) WHERE departed_at IS NOT NULL;
CREATE UNIQUE INDEX idx_tasks_native    ON tasks(integration, native_id)
                                        WHERE integration IS NOT NULL;
```

`idx_entries_start DESC` backs the resting view, which is the most-recent-first list. `idx_records_phase` and `idx_entries_unlinked_phase` back the pending and failed counts in the always-visible status bar, which are recomputed on every mutation. `idx_tasks_native` is what makes reconciliation an upsert.

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
- **`unlinked_phase` / `unlinked_error` on entries**, with a `CHECK` making their mutual exclusion with `remote_record_id` structural rather than conventional.
- **A remote record may be referenced by multiple entries**, expressed as a plain nullable FK rather than a join table, because an entry still targets at most one record. A join table would only be needed if an entry could feed several remotes at once, which `YATTA.md` rejects.
- **Upload state lives on `remote_records`** once a group has uploaded successfully at least once, since the state is then a group-level fact; on the unlinked entry row before that.
- **Stale remote task references are reconciled, not wiped.** Referenced departed rows are retained with `departed_at`; unreferenced ones are hard-deleted. No display-name snapshot on entries is needed, since the retained row keeps both the reference and the display intact.
- **Foreign keys and WAL enabled** at connection setup.

## Open Questions

- **Whether the local-task/upload-columns invariant deserves a trigger.** Currently a store invariant with a test, on the grounds that a single-writer local database does not need belt and braces. A trigger would make it structural at the cost of schema machinery, and would also catch corruption introduced by hand-editing the database — which the inspectability argument above makes marginally more likely.
- **Whether `extra` should become typed columns per integration.** Acceptable as a JSON blob while only one integration is active at a time and the set is small; worth revisiting if deserialization becomes hot or the three integrations' fields diverge enough that the blob obscures more than it saves.
- **Retention.** Nothing in the schema ever ages out. Uploaded records and their entries accumulate indefinitely. For a single user this is unlikely to matter for years, but the always-running resting view queries the entry list constantly, and it is worth deciding whether that list is bounded by a window (the index supports it) or whether the table is expected to stay small enough that it never matters.
