# YATTA — Architecture & Project Structure

## Overview

YATTA is a single Go module producing one statically-linked binary. The interface is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) application intended to remain running. Persistence is SQLite through a pure-Go driver, addressed with hand-written SQL. There is no ORM, no code generation in the domain, and no dependency injection framework.

**The domain package imports nothing but the standard library.** No interface types, no database types, no HTTP types. This is enforced by the import list and visible at the top of every file. It is also what keeps the deferred alternative delivery surface described in `YATTA.md` cheap rather than speculative: no work is done for it now, but nothing beneath the interface assumes a terminal either.

---

## Design Philosophy

1. **Idiomatic Go first; ceremony only where it pays.** Structs with exported fields. Constructors only where an invariant genuinely needs protecting. Pointer-nil as the natural "this concept does not apply here" marker rather than a type-level split.

2. **Nil is the discriminator.** `TimeEntry.Upload == nil` means the entry is on a local task and has no remote concern. `Task.Remote == nil` means the task is local. This keeps one type and one table per concept, and avoids a polymorphic foreign key that SQLite could not enforce.

3. **Derive what can be derived.** `End()` is a method over `Start` and `Duration`, never a field. Divergence from the remote is derived from the upload phase and record link, never stored as a separate flag.

4. **Push logic into pure functions; keep I/O at the edges.** Rounding, aggregation, and upload planning are pure functions from values to values. They read no database, call no network, and consult no clock they were not handed. Everything intricate about this product lives in those functions, which means everything intricate about this product is directly table-testable. This is the primary compensation for Go's weaker type system relative to the domain's variants, and it is load-bearing rather than incidental: the hardest logic in the product must not also be the hardest to test.

5. **The message loop is the state machine.** There is no separate state-management layer. Bubble Tea's `Model`/`Update`/`View` *is* the application's state, and I/O is expressed as `tea.Cmd` returning a `tea.Msg`.

---

## Package Structure

```
yatta/
├── cmd/
│   └── yatta/
│       └── main.go            # wiring: open store, build adapter, run program
├── internal/
│   ├── core/                  # domain types + pure logic. stdlib only.
│   │   ├── entry.go
│   │   ├── task.go
│   │   ├── record.go
│   │   ├── policy.go
│   │   ├── round.go           # rounding + aggregation
│   │   └── plan.go            # upload planning
│   ├── store/                 # SQLite. Concrete type, hand-written SQL.
│   │   ├── store.go
│   │   ├── entries.go
│   │   ├── tasks.go
│   │   ├── records.go
│   │   └── migrations/*.sql   # embedded
│   ├── remote/                # Adapter interface + implementations
│   │   ├── adapter.go
│   │   ├── jira/
│   │   ├── redmine/
│   │   └── toggl/
│   ├── secret/                # OS keyring wrapper
│   └── ui/                    # Bubble Tea models
│       ├── app.go             # root model, mode switching, status bar
│       ├── entries.go         # list + filter + resume
│       ├── picker.go          # task tree picker
│       ├── editor.go          # entry create/edit form
│       ├── upload.go          # upload confirm + progress
│       └── attention.go       # failed uploads and departed tasks
└── go.mod
```

`internal/` is doing real work here: nothing outside this module can import any of it, so the package boundaries are enforced rather than advisory.

### Dependency Rules

| Package | May import | May not import |
|---|---|---|
| `core` | standard library only | everything else, including `store` and `remote` |
| `store` | `core`, database driver | `ui`, `remote` |
| `remote` | `core`, `net/http` | `ui`, `store` |
| `secret` | keyring library | `core`, `ui`, `store` |
| `ui` | `core`, `store`, `remote` | — |
| `cmd/yatta` | everything | — |

`core` not importing `remote` is the load-bearing rule. `core.PlanUpload` decides *what* operations to perform; it never learns which system performs them.

---

## Domain Types (`internal/core`)

### Time

Instants are `time.Time` held in UTC. Durations are `time.Duration`. The local timezone is applied only when rendering and when computing the aggregation day boundary; it is never persisted. See `YATTA-DATA.md` for the storage encoding.

### Tasks

One type, with the remote-only fields in an optional sub-struct. `Remote == nil` is exactly "this is a local task."

```go
type Task struct {
    ID       string
    Name     string
    ParentID string      // "" for roots
    Sort     int         // sibling ordering
    Archived *time.Time  // local tasks only; non-nil => hidden from the picker
    Remote   *RemoteTask // nil => local task
}

type RemoteTask struct {
    Integration string          // "jira" | "redmine" | "toggl"
    NativeID    string          // id as known to the remote system
    NodeType    string          // "epic", "issue", "project", ...
    Extra       json.RawMessage // integration-specific fields
    Departed    *time.Time      // non-nil => absent from the latest fetch
}

func (t Task) IsRemote() bool   { return t.Remote != nil }
func (t Task) Selectable() bool {
    return t.Archived == nil && (t.Remote == nil || t.Remote.Departed == nil)
}
```

Integration-specific native keys live in `Extra` as raw JSON and are unmarshalled by the owning adapter into its own unexported struct. This puts the typing where the typing is used: an adapter building a request payload is the only code that needs a structured native key, and `Integration` tells it the blob is its own to interpret. Giving each integration a distinct domain type would impose per-integration handling on every other layer to serve one call site.

`Selectable` encodes the two independent reasons a task can exist but not be choosable. The distinction matters: archived is a local user decision, departed is an upstream fact requiring attention. Both hide from the picker; only departure raises an indicator.

### Time Entries

```go
type TimeEntry struct {
    ID        string
    Start     time.Time     // UTC
    Duration  time.Duration
    TaskID    string
    Note      string
    Upload    *UploadState  // nil => entry is on a local task
    CreatedAt time.Time
    UpdatedAt time.Time
}

func (e TimeEntry) End() time.Time { return e.Start.Add(e.Duration) }
```

`Upload` is nil for entries on local tasks and always non-nil for entries on remote tasks. This is an invariant the store maintains rather than one the compiler enforces — the deliberate trade named in Design Philosophy #1. It is asserted on the store's read path and covered by tests.

### Upload State

```go
type Phase string

const (
    Pending  Phase = "pending"
    Uploaded Phase = "uploaded"
    Failed   Phase = "failed"
)

type UploadState struct {
    Phase    Phase
    Err      string // non-empty only when Phase == Failed
    RecordID string // "" until this entry has been part of a successful upload
}

// Diverged reports that the remote holds a stale projection of this entry:
// it was uploaded successfully at least once, and has since changed.
func (u UploadState) Diverged() bool { return u.Phase == Pending && u.RecordID != "" }
```

The two fields together carry five meaningful states:

| `Phase` | `RecordID` | Meaning |
|---|---|---|
| `Pending` | `""` | Never uploaded. Awaiting its first upload. |
| `Failed` | `""` | First upload attempt failed. Nothing exists upstream. |
| `Uploaded` | set | Remote matches this entry as of the record's `UploadedAt`. |
| `Pending` | set | **Diverged.** Uploaded before, changed since. Remote holds a stale projection; the UI shows both. |
| `Failed` | set | Re-upload failed. The remote still holds the last good projection. |

**Where the state actually lives.** Once `RecordID` is set, the phase is a property of the *record*, shared by every entry linked to it, so that aggregated siblings always agree — the "group shares one fate" rule in `YATTA.md`. `TimeEntry.Upload` is therefore materialized by the store on read: linked entries take their phase and error from the record, unlinked entries from their own row. Neither `core` nor `ui` needs to know this; both see a consistent `UploadState` either way.

### Remote Records and Orphans

```go
type RemoteRecord struct {
    ID          string        // local UUID
    RemoteID    string        // opaque; only the owning adapter interprets it
    Integration string
    TaskID      string        // the task this record was uploaded under
    Start       time.Time     // post-rounding; earliest member's start under aggregation
    Duration    time.Duration // post-rounding; summed across members under aggregation
    Phase       Phase
    Err         string
    UploadedAt  time.Time
}

// An OrphanedRecord still exists upstream but no local entry targets it any
// more, because every member was deleted or reassociated away. It outlives the
// entries, so it is tracked separately rather than cascading away with them.
type OrphanedRecord struct {
    RemoteID    string
    Integration string
    TaskID      string // delete context, e.g. the Jira issue holding the worklog
    OrphanedAt  time.Time
}
```

`TaskID` on the record serves two purposes: some integrations address a record by parent *and* id when deleting (a Jira worklog is addressed by issue and worklog id), and comparing an entry's current task against its record's task is what detects a reassociation.

### Active Timer

```go
type ActiveTimer struct {
    Start  time.Time
    TaskID string
}
```

Persisted, so that a running timer survives a restart and an accidental terminal close does not lose an in-flight block.

---

## Pure Logic (`internal/core`)

This is where the product's difficulty lives, and it is deliberately I/O-free.

### Rounding and Aggregation

```go
type Direction string  // "nearest" | "up" | "down"
type BelowMin  string  // "round_up" | "exclude"
type AggKey    string  // "none" | "task_day"

type Policy struct {
    Increment time.Duration // 0 => no rounding
    Direction Direction
    Minimum   time.Duration // 0 => no minimum
    BelowMin  BelowMin
    Aggregate AggKey
}

// A Unit is what one remote record will represent: one entry when aggregation
// is off, several when it is on.
type Unit struct {
    TaskID   string
    Entries  []TimeEntry
    Start    time.Time     // earliest member's start, post-rounding
    Duration time.Duration // post-rounding; summed under aggregation
}

// Group applies the policy to entries, returning the units to be uploaded and
// the entries excluded by a below-minimum rule. loc is the device timezone,
// used only for the task_day grouping boundary.
//
// Group must be given every entry belonging to a candidate group, including
// entries already uploaded — see PlanUpload.
func Group(entries []TimeEntry, p Policy, loc *time.Location) (units []Unit, excluded []TimeEntry)
```

Aggregation is folded into the grouping output rather than modeled as a separate pass: the non-aggregated case is simply every unit having exactly one member. One code path serves both configurations, and no caller needs to branch on whether aggregation is active.

`Group` performs no overlap or conflict validation, and no facility anywhere in `core` does. See Resolved Decisions.

### Upload Planning

```go
type OpKind string

const (
    OpCreate OpKind = "create"
    OpUpdate OpKind = "update"
    OpDelete OpKind = "delete"
)

type Op struct {
    Kind     OpKind
    Unit     Unit   // zero value for OpDelete
    RecordID string // "" for OpCreate
    RemoteID string // "" for OpCreate
    TaskID   string // delete context for OpDelete
}

// PlanUpload derives the complete set of operations from current state alone.
//
// entries must contain EVERY remote-task entry within the bound, regardless of
// phase — uploaded entries included. Groups are formed from current membership,
// so a group's recomputed duration is only correct if the whole group is
// present. Passing only the non-uploaded entries would recompute an existing
// record from its new members alone and silently undercount it.
//
// through bounds the upload to entries starting on or before that instant. It
// is an inclusive local date normalized to end-of-day, which guarantees it
// never bisects an aggregation group. The zero value means no bound.
//
// PlanUpload never consults a log of edits — the net-intent semantics in
// YATTA.md fall out of deriving everything from the present.
func PlanUpload(
    entries []TimeEntry,
    records map[string]RemoteRecord,
    orphans []OrphanedRecord,
    p Policy,
    loc *time.Location,
    through time.Time,
) []Op
```

The rules it encodes:

- No member of a unit carries a `RecordID` → **create**; on success every member links to the new record.
- A member links to a record whose `TaskID` matches the unit's → **update** that record in place with the recomputed start and duration, and link any newly added members to it.
- A member's linked record has a `TaskID` that no longer matches → that member is simply absent from this unit, because grouping is always by current task. If it was the record's last member the record is already an orphan (the store orphans on the last member leaving); otherwise the record is recomputed for its survivors in the same pass, which falls out of regrouping rather than needing its own branch.
- Each orphan → **delete**.
- **A unit whose members are all `Uploaded` and whose recomputed start and duration equal the linked record's produces no operation.** This is what keeps "pass every entry" from turning every upload into a full re-upload: fully-settled groups are present so grouping is correct, then drop out.

Because the whole thing is a function over values, every reassociation and membership-change edge case is an ordinary table entry in a test file rather than a scenario requiring a mocked adapter.

### Execution

Execution is the thin part, and it lives in `ui` as a command, because it is I/O:

```go
func uploadCmd(st *store.Store, ad remote.Adapter, ops []core.Op) tea.Cmd
```

It walks the ops, calls the adapter, and persists each result: on success the record's phase becomes `Uploaded` and any consumed orphan is removed; on failure the record's phase becomes `Failed` with the error, or — for an entry not yet linked to any record — the failure is written to the entry's own row. **There is no batch-level failure mode.** A rejected op produces failed entries within an otherwise successful upload; it never aborts the run. This includes a remote-specific rejection such as an overlap constraint, which is an ordinary adapter error like any other.

---

## Store (`internal/store`)

A concrete `*store.Store` over [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — pure Go, so `CGO_ENABLED=0` and cross-compilation stay trivial. SQL is hand-written and lives beside the methods that run it.

```go
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error)

func (s *Store) Entries(from, to time.Time) ([]core.TimeEntry, error)
func (s *Store) Entry(id string) (core.TimeEntry, error)
func (s *Store) SaveEntry(e core.TimeEntry) error
func (s *Store) DeleteEntry(id string) error

// RemoteEntries returns every entry on a remote task starting on or before
// through, in any phase. Named for what it returns rather than for the upload
// that consumes it, because PlanUpload requires settled entries too.
func (s *Store) RemoteEntries(through time.Time) ([]core.TimeEntry, error)

func (s *Store) Tasks() ([]core.Task, error)
func (s *Store) ReconcileRemoteTasks(fetched []core.Task) error
func (s *Store) Records(ids []string) (map[string]core.RemoteRecord, error)
func (s *Store) Orphans() ([]core.OrphanedRecord, error)
// ... timer, config, settings
```

**No interface is declared here.** Where a test or a caller needs to substitute the store, the consuming package declares the narrow interface it actually uses — typically one or two methods — which is the Go convention and produces smaller seams than a mirror of the full type.

**There is no observation mechanism.** Bubble Tea does not observe state; it receives messages. Every mutation returns a `tea.Cmd` that reloads the affected slice and delivers it as a message, so there is exactly one explicit place per mutation where the reload is requested.

Two invariants the store owns, since the type system does not:

- An entry whose task is remote always reads back with a non-nil `Upload`; an entry whose task is local always reads back with nil. Asserted on the read path.
- A linked entry's phase and error are materialized from its record, never from its own row, so aggregated siblings cannot disagree.

The schema is specified in `YATTA-DATA.md`.

---

## Remote Integration (`internal/remote`)

An interface is warranted here: the implementation is selected at runtime from configuration, which is what interfaces are for.

```go
type Adapter interface {
    Integration() string
    FetchTasks(ctx context.Context) ([]core.Task, error)
    Create(ctx context.Context, u core.Unit) (remoteID string, err error)
    Update(ctx context.Context, remoteID string, u core.Unit) error
    Delete(ctx context.Context, remoteID string, taskID string) error
}
```

Adapters take a `core.Unit` and neither know nor care whether its duration summarizes one entry or five — aggregation is invisible below the planning layer. Credentials and base URL are injected at construction; an adapter never reads configuration or the keyring itself. Each adapter unmarshals `Task.Remote.Extra` into its own unexported struct.

All three are plain `net/http` clients with `encoding/json`. Jira, Redmine, and Toggl all expose ordinary REST, and a vendor SDK would import far more than it saves.

An adapter may reject a call for reasons specific to its remote — a system that disallows overlapping entries, for instance. That rejection is a returned error, handled by execution exactly like any other adapter failure. There is no shared overlap-detection or conflict-resolution facility, and any such logic belongs inside the adapter that needs it.

## Credentials (`internal/secret`)

A thin wrapper over the OS keyring: Keychain on macOS, Secret Service on Linux, Credential Manager on Windows. SQLite stores only a lookup key, never a secret.

Library choice is an open question below. The wrapper exists specifically so that it stays a one-file decision.

---

## Interface (`internal/ui`)

The mode structure and component inventory below are settled. Specific keybindings, column layouts, and visual treatment are settled during implementation against the running application rather than specified here.

### Root Model

One root model owning a mode, the shared data slices, and the sub-models, with a single `Update` switch over messages delegating to the focused sub-model.

```go
type mode int

const (
    modeEntries mode = iota // resting view
    modePicker              // choosing a task
    modeEditor              // creating or correcting an entry
    modeUpload              // confirm scope, then progress
    modeAttention           // failed uploads and departed tasks
)

type Model struct {
    mode      mode
    entries   entriesModel
    picker    pickerModel
    editor    editorModel
    upload    uploadModel
    attention attentionModel

    timer      *core.ActiveTimer
    todayTotal time.Duration
    counts     struct{ pending, failed, departed int }

    st *store.Store
    ad remote.Adapter // nil when no integration is configured
}
```

The resting view is `modeEntries` with a status bar rendered by the root model on every frame, carrying the running timer, today's total, and the three attention counts — the always-visible surface required by `YATTA.md`.

### Components

| Requirement | Component |
|---|---|
| Entry list, search, filter, resume-from-recent | `bubbles/list` — its built-in filtering is the search requirement, and the list is ordered most-recent-first so resume is a filter plus Enter |
| Merged local/remote view | The entry list's detail rendering. A linked entry shows its local values alongside the record's uploaded values; under aggregation the sibling members are shown together against the one remote value. `Diverged()` drives the emphasis |
| Task tree picker | `bubbles/list` over a depth-flattened tree with indent prefixes, filtering on the full ancestry path; non-`Selectable()` tasks omitted |
| Entry create / correct | `bubbles/textinput` for times and note, plus the picker for the task — reassociation is this same flow, not a separate one |
| Rounding and aggregation policy | `huh` form, reached from settings |
| Upload confirmation and progress | custom; a summary of planned ops and below-minimum exclusions, then per-op results streaming in |
| Failed uploads and departed tasks | `bubbles/list` in `modeAttention`, filtered by kind; selecting an item returns to `modeEntries` positioned on the affected entry |

Flattening the task tree into a filterable list rather than building a tree widget is deliberate: filtering across a full ancestry path is a better interaction for deep hierarchies than expanding and collapsing nodes, and it reuses the component already carrying the entry list.

Failed uploads and departed tasks share one mode with a filter rather than occupying two. They are consulted in the same moment, they share the same go-to action, and a departed task frequently *causes* the upload failure sitting next to it in the list.

Under aggregation, every entry sharing a failed record appears in the attention list, since the materialized phase gives all of them `Failed`. That falls out of the store's materialization rather than needing its own rule.

### Messages and Commands

I/O never blocks `Update`. Every store read, every adapter call, and the timer tick are commands.

```go
type entriesLoadedMsg struct{ entries []core.TimeEntry; err error }
type tasksLoadedMsg   struct{ tasks   []core.Task;      err error }
type tickMsg          time.Time              // 1s, drives the running-timer display
type uploadPlannedMsg struct{ ops []core.Op; excluded []core.TimeEntry }
type opDoneMsg        struct{ op core.Op; err error }   // one per operation
type uploadDoneMsg    struct{ succeeded, failed int }
```

`opDoneMsg` arriving one per operation is what makes the no-batch-failure rule fall out of the architecture rather than needing enforcement: each result is persisted and rendered independently, and there is no place where a single error could abort the run even by accident.

`uploadPlannedMsg` carries the below-minimum exclusions alongside the ops so the confirmation screen can show them, satisfying the visibility requirement in `YATTA.md`.

The `tickMsg` cadence is one second, which is what an always-visible running timer requires and is inexpensive — Bubble Tea repaints only changed lines.

---

## Testing

The type system carries fewer guarantees than the domain has invariants, so tests carry the difference, and the architecture is arranged to make that a fair trade rather than a hopeful one.

- **`core` is covered by table-driven tests and carries no mocks**, because it has no I/O to mock. `Group` and `PlanUpload` between them hold every rule that is subtle: rounding directions, below-minimum behavior, the day boundary near midnight in a non-UTC zone, task reassociation, membership changes to an already-uploaded group, adding a member to a settled group, and orphan handling. Each is a table row.
- **The store is tested against a real SQLite database** in a temp file, not a fake. It is fast enough, and the invariants worth testing — materialized phase, nil-`Upload` discipline, reconciliation of departed tasks — are exactly the ones a fake would paper over.
- **Adapters are tested against `httptest.Server`** with recorded payloads.
- **`gochecksumtype` in the lint gate**, wherever a closed union remains, recovers exhaustiveness checking on type switches.

---

## Build and Distribution

`CGO_ENABLED=0 go build ./cmd/yatta` produces one static binary per platform with no runtime dependency. Cross-compilation is `GOOS`/`GOARCH`. Migrations are embedded with `embed.FS`, so the binary is self-contained.

---

## Resolved Decisions

- **Flattened domain model.** One `TimeEntry` with a nillable `Upload`, one `Task` with a nillable `Remote` and a JSON `Extra`, and an upload state expressed as a phase plus a record link. The alternative — closed interfaces with unexported marker methods and a type switch at every use site — reproduces the shape of a sealed hierarchy without reproducing its exhaustiveness, and costs more code than the guarantee is worth at this size. The invariants the flattening gives up are held by the store and asserted by tests.
- **Upload planning is a pure function.** `PlanUpload` decides; a command executes. The create/update/drop-member/orphan branch table is the most error-prone logic in the product, and keeping it free of network calls and persistence is what makes it directly testable.
- **Planning sees settled entries, not just pending ones.** `PlanUpload` takes every remote-task entry within the bound regardless of phase, and drops fully-settled unchanged groups on the way out. Grouping by current membership is only correct if the whole group is present; a plan built from non-uploaded entries alone would recompute an existing record from its new members and undercount it.
- **The upload bound is an inclusive local date normalized to end-of-day.** An arbitrary timestamp bound could bisect a task+day aggregation group, uploading half a day and leaving the rest to recompute the same record later.
- **No interfaces in the domain for persistence.** The store is a concrete type; consumers declare narrow interfaces where they need seams.
- **Domain objects hold identifiers, not object references.** Parent-pointer object graphs over an arbitrary-depth tree fight both Go's lack of lazy loading and SQLite's adjacency-list storage, and departed-node reconciliation is where a graph representation would hurt most.
- **Integration-specific task typing lives in the adapter.** Native keys are `json.RawMessage` in `core` and typed structs inside the owning adapter, which is the only code that reads them.
- **Failed uploads and departed tasks share one attention view.** Same moment of consultation, same go-to action, and the two conditions are frequently causally linked.
- **Below-minimum exclusions are surfaced at upload confirmation.** An entry excluded by a minimum-duration rule is otherwise indistinguishable from one that was never attempted.
- **`net/http` rather than vendor SDKs** for all three integrations.
- **Pure-Go SQLite (`modernc.org/sqlite`)** to keep `CGO_ENABLED=0` and single-binary cross-compilation.
- **No overlap validation in `core`.** `Group` does not validate spans and `PlanUpload` has no abort path. A remote that genuinely cares enforces it inside its own adapter, where a rejection is an ordinary per-op failure.
- **Credentials in the OS keyring, never in SQLite.**

## Open Questions

- **Keyring library.** `zalando/go-keyring` is smaller and shells out to platform tools; `99designs/keyring` supports more backends including an encrypted-file fallback, which matters on a headless or minimal Linux workstation where no Secret Service daemon is running. Whether that fallback case is real for the target environment decides it. The wrapper is designed so this stays a one-file change.
- **Hand-written SQL versus `sqlc`.** Hand-written is assumed above and keeps the dependency count and build steps minimal. `sqlc` generates typed Go from the same SQL and would recover a slice of the compile-time safety the flattened domain gives up, at the cost of a code-generation step in the build. Given how much weight rests on tests rather than types, this deserves an explicit answer rather than a default.
- **Migration runner.** A hand-rolled stepper over `PRAGMA user_version` with embedded `.sql` files is roughly forty lines and adds no dependency; `goose` or `golang-migrate` are standard and add one. Leaning hand-rolled on the same minimal-dependency grounds as the rest.
- **Package name for the domain.** `internal/core` reads as `core.TimeEntry`, which is serviceable but generic. `internal/tracking` is an alternative. Cosmetic, but it appears in every file in the project.
