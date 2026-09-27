# YATTA — Architecture & Project Structure

## Overview

YATTA is a single Go module producing one statically-linked binary. The interface is a [Bubble Tea](https://github.com/charmbracelet/bubbletea) application intended to remain running. Persistence is SQLite through a pure-Go driver, addressed with hand-written SQL. There is no ORM, no code generation in the domain, and no dependency injection framework.

**The domain package imports nothing but the standard library.** No interface types, no database types, no HTTP types. This is enforced by the import list and visible at the top of every file. It is also what keeps the deferred alternative delivery surface described in `YATTA.md` cheap rather than speculative: no work is done for it now, but nothing beneath the interface assumes a terminal either.

---

## Design Philosophy

1. **Idiomatic Go first; ceremony only where it pays.** Structs with exported fields. Constructors only where an invariant genuinely needs protecting. Pointer-nil as the natural "this concept does not apply here" marker rather than a type-level split.

2. **Nil is the discriminator.** `TimeEntry.Upload == nil` means the entry is on a local task and has no remote concern. `Task.Remote == nil` means the task is local. This keeps one type and one table per concept, and avoids a polymorphic foreign key that SQLite could not enforce.

3. **Derive what can be derived.** `End()` is a method over `Start` and `Duration`, never a field. Whether an entry is locked is derived from its record link, never stored as a separate flag.

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

// FetchedTask is one node of a remote hierarchy as an adapter returns it,
// before it has a local ID. The store's reconciliation maps native IDs to
// local UUIDs, including the parent link.
type FetchedTask struct {
    NativeID       string
    ParentNativeID string // "" for roots
    Name           string
    NodeType       string
    Extra          json.RawMessage
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
    Pending  Phase = "pending"  // not yet uploaded; editable
    Failed   Phase = "failed"   // last upload attempt rejected; editable, retried next upload
    Uploaded Phase = "uploaded" // linked to a record that was sent; locked
    Excluded Phase = "excluded" // linked to a sentinel: below minimum, nothing sent; locked
)

type UploadState struct {
    Phase    Phase
    Err      string // non-empty only when Phase == Failed
    RecordID string // "" until uploaded or excluded
}

// Locked reports that the entry has been uploaded or excluded and can no
// longer be changed.
func (u UploadState) Locked() bool { return u.RecordID != "" }
```

`Phase` follows from `RecordID` plus one fact on each side: an unlinked entry is `Failed` if it carries an error and `Pending` otherwise; a linked entry is `Excluded` if its record is a sentinel and `Uploaded` otherwise. The store materializes it on read. Upload is one-way, so there is no state in which an entry is linked and still owes the remote anything.

### Remote Records

```go
type RemoteRecord struct {
    ID          string        // local UUID
    RemoteID    string        // opaque; only the owning adapter interprets it. "" => excluded sentinel
    Integration string
    TaskID      string        // the task this record was uploaded under
    Start       time.Time     // earliest member's start
    Duration    time.Duration // members' summed duration, policy applied
    Note        string        // distinct member notes, concatenated
    CreatedAt   time.Time
}

func (r RemoteRecord) Excluded() bool { return r.RemoteID == "" }
```

A record is written once and never modified — YATTA does not update or delete remote records.

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
    Start    time.Time     // earliest member's start; not rounded
    Raw      time.Duration // sum of member durations
    Duration time.Duration // Raw with the policy applied
    Note     string        // distinct member notes, in start order
}

// Group forms units from entries and applies the policy to each unit's summed
// duration. Units falling below the minimum under the "exclude" rule are
// returned separately. loc is the device timezone, used only for the task_day
// grouping boundary.
func Group(entries []TimeEntry, p Policy, loc *time.Location) (upload, excluded []Unit)
```

Rounding and the minimum are applied once, to the unit's summed raw duration. Individual entries are never rounded.

Aggregation is folded into the grouping output rather than modeled as a separate pass: the non-aggregated case is simply every unit having exactly one member. One code path serves both configurations, and no caller needs to branch on whether aggregation is active.

`Group` performs no overlap or conflict validation, and no facility anywhere in `core` does. See Resolved Decisions.

### Upload Planning

```go
type Plan struct {
    Upload   []Unit // each becomes one Create call
    Excluded []Unit // each becomes one sentinel record; shown on the confirmation screen
}

// PlanUpload selects the unlocked remote-task entries starting on or before
// through and groups them. through is an inclusive local date normalized to
// end-of-day, which guarantees it never bisects an aggregation group. The zero
// value means no bound.
//
// Locked entries are never planned: upload is one-way, so an entry that has
// been uploaded or excluded is finished.
func PlanUpload(entries []TimeEntry, p Policy, loc *time.Location, through time.Time) Plan
```

With create as the only remote operation, planning has no branch table: every unit is either uploaded or excluded. Because it is a function over values, every rounding, minimum, and day-boundary case is an ordinary table entry in a test file.

### Execution

Execution is the thin part, and it lives in `ui` as a command, because it is I/O:

```go
func uploadCmd(st *store.Store, ad remote.Adapter, plan core.Plan) tea.Cmd
```

On confirmation, it first records the excluded units as sentinel records, then walks the upload units in order, calling the adapter's `Create` for each and persisting each result immediately: on success a record is written and every member linked to it in one transaction; on failure the error is written to each member's row, leaving them unlinked and editable. **There is no batch-level failure mode.** A rejected unit produces failed entries within an otherwise successful upload; it never aborts the run. This includes a remote-specific rejection such as an overlap constraint, which is an ordinary adapter error like any other.

Upload is modal: while it runs, the interface shows progress and accepts no edits, so what is sent is exactly what was confirmed.

---

## Store (`internal/store`)

A concrete `*store.Store` over [`modernc.org/sqlite`](https://pkg.go.dev/modernc.org/sqlite) — pure Go, so `CGO_ENABLED=0` and cross-compilation stay trivial. SQL is hand-written and lives beside the methods that run it.

```go
type Store struct{ db *sql.DB }

func Open(path string) (*Store, error)

func (s *Store) Entries(from, to time.Time) ([]core.TimeEntry, error)
func (s *Store) Entry(id string) (core.TimeEntry, error)
func (s *Store) SaveEntry(e core.TimeEntry) error // rejects a locked entry

// UnlockedRemoteEntries returns every pending or failed entry on a remote task
// starting on or before through: the input to PlanUpload.
func (s *Store) UnlockedRemoteEntries(through time.Time) ([]core.TimeEntry, error)

func (s *Store) RecordUpload(u core.Unit, integration, remoteID string) error // write record, link members
func (s *Store) RecordExcluded(u core.Unit, integration string) error         // write sentinel, link members
func (s *Store) RecordFailure(u core.Unit, err error) error                   // set error on members

func (s *Store) Purge(before time.Time) error

func (s *Store) Tasks() ([]core.Task, error)
func (s *Store) ReconcileRemoteTasks(integration string, fetched []core.FetchedTask) error
// ... timer, config, settings
```

**No interface is declared here.** Where a test or a caller needs to substitute the store, the consuming package declares the narrow interface it actually uses — typically one or two methods — which is the Go convention and produces smaller seams than a mirror of the full type.

**There is no observation mechanism.** Bubble Tea does not observe state; it receives messages. Every mutation returns a `tea.Cmd` that reloads the affected slice and delivers it as a message, so there is exactly one explicit place per mutation where the reload is requested.

Two invariants the store owns, since the type system does not:

- An entry whose task is remote always reads back with a non-nil `Upload`; an entry whose task is local always reads back with nil. Asserted on the read path.
- A linked entry is never modified. `SaveEntry` refuses it.

The schema is specified in `YATTA-DATA.md`.

---

## Remote Integration (`internal/remote`)

An interface is warranted here: the implementation is selected at runtime from configuration, which is what interfaces are for.

```go
type Adapter interface {
    Integration() string
    FetchTasks(ctx context.Context) ([]core.FetchedTask, error)
    Create(ctx context.Context, task core.Task, u core.Unit) (remoteID string, err error)
}
```

Create is the only write. `task` is the unit's target task, passed so the adapter can read its native ID and `Extra` without any store access. Adapters take a `core.Unit` and neither know nor care whether its duration summarizes one entry or five — aggregation is invisible below the planning layer. Credentials and base URL are injected at construction; an adapter never reads configuration or the keyring itself. Each adapter unmarshals `Task.Remote.Extra` into its own unexported struct.

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
    modeEditor              // creating or correcting an entry; read-only when locked
    modeUpload              // confirm scope, then blocking progress
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
| Merged local/remote view | The entry list's detail rendering, read-only. A linked entry shows its local values alongside the record's uploaded values; under aggregation the sibling members are shown together against the one remote value, and an excluded entry is marked as such |
| Task tree picker | `bubbles/list` over a depth-flattened tree with indent prefixes, filtering on the full ancestry path; non-`Selectable()` tasks omitted |
| Entry create / correct | `bubbles/textinput` for times and note, plus the picker for the task — reassociation is this same flow, not a separate one. A locked entry opens read-only |
| Rounding and aggregation policy | `huh` form, reached from settings |
| Upload confirmation and progress | custom; a summary of the units to upload and the below-minimum exclusions, then per-unit results streaming in while all other input is blocked |
| Failed uploads and departed tasks | `bubbles/list` in `modeAttention`, filtered by kind; selecting an item returns to `modeEntries` positioned on the affected entry |

Flattening the task tree into a filterable list rather than building a tree widget is deliberate: filtering across a full ancestry path is a better interaction for deep hierarchies than expanding and collapsing nodes, and it reuses the component already carrying the entry list.

Failed uploads and departed tasks share one mode with a filter rather than occupying two. They are consulted in the same moment, they share the same go-to action, and a departed task frequently *causes* the upload failure sitting next to it in the list.

Under aggregation, every member of a failed unit appears in the attention list, since the failure is written to each member. Departed tasks are listed only through the not-yet-uploaded entries on them; uploaded entries on a departed task are history and need no action.

### Messages and Commands

I/O never blocks `Update`. Every store read, every adapter call, and the timer tick are commands.

```go
type entriesLoadedMsg struct{ entries []core.TimeEntry; err error }
type tasksLoadedMsg   struct{ tasks   []core.Task;      err error }
type tickMsg          time.Time              // 1s, drives the running-timer display
type uploadPlannedMsg struct{ plan core.Plan }
type unitDoneMsg      struct{ unit core.Unit; err error } // one per uploaded unit
type uploadDoneMsg    struct{ succeeded, failed int }
```

`unitDoneMsg` arriving one per unit is what makes the no-batch-failure rule fall out of the architecture rather than needing enforcement: each result is persisted and rendered independently, and there is no place where a single error could abort the run even by accident.

`uploadPlannedMsg` carries the below-minimum exclusions alongside the ops so the confirmation screen can show them, satisfying the visibility requirement in `YATTA.md`.

The `tickMsg` cadence is one second, which is what an always-visible running timer requires and is inexpensive — Bubble Tea repaints only changed lines.

---

## Testing

The type system carries fewer guarantees than the domain has invariants, so tests carry the difference, and the architecture is arranged to make that a fair trade rather than a hopeful one.

- **`core` is covered by table-driven tests and carries no mocks**, because it has no I/O to mock. `Group` and `PlanUpload` between them hold every rule that is subtle: rounding the summed duration rather than each entry, rounding directions, below-minimum behavior, note concatenation, the day boundary near midnight in a non-UTC zone, the upload bound, and never planning a locked entry. Each is a table row.
- **The store is tested against a real SQLite database** in a temp file, not a fake. It is fast enough, and the invariants worth testing — locking of linked entries, nil-`Upload` discipline, reconciliation of departed tasks, purge — are exactly the ones a fake would paper over.
- **Adapters are tested against `httptest.Server`** with recorded payloads.
- **`exhaustive` in the lint gate** recovers exhaustiveness checking on switches over the string enums (`Phase`, `Direction`, `BelowMin`, `AggKey`), since the design has no interface-based unions for a sum-type checker to inspect.

---

## Build and Distribution

`CGO_ENABLED=0 go build ./cmd/yatta` produces one static binary per platform with no runtime dependency. Cross-compilation is `GOOS`/`GOARCH`. Migrations are embedded with `embed.FS`, so the binary is self-contained.

---

## Resolved Decisions

- **Flattened domain model.** One `TimeEntry` with a nillable `Upload`, one `Task` with a nillable `Remote` and a JSON `Extra`, and an upload state expressed as a phase plus a record link. The alternative — closed interfaces with unexported marker methods and a type switch at every use site — reproduces the shape of a sealed hierarchy without reproducing its exhaustiveness, and costs more code than the guarantee is worth at this size. The invariants the flattening gives up are held by the store and asserted by tests.
- **Upload planning is a pure function.** `PlanUpload` decides; a command executes. Grouping, rounding, and minimums are where the product's subtle rules live, and keeping them free of network calls and persistence is what makes them directly testable.
- **Create is the only remote operation.** Upload is one-way (`YATTA.md`), so the adapter interface has no update or delete, the planner has no record-reconciliation branches, and there are no orphaned records.
- **The adapter receives the target task.** `Create` is passed the `core.Task` so the adapter can address the remote task by native ID without store access; `FetchTasks` returns `FetchedTask`s keyed by native ID, and the store assigns local IDs.
- **The upload bound is an inclusive local date normalized to end-of-day.** An arbitrary timestamp bound could bisect a task+day aggregation group, uploading half a day and leaving the rest to recompute the same record later.
- **No interfaces in the domain for persistence.** The store is a concrete type; consumers declare narrow interfaces where they need seams.
- **Domain objects hold identifiers, not object references.** Parent-pointer object graphs over an arbitrary-depth tree fight both Go's lack of lazy loading and SQLite's adjacency-list storage, and departed-node reconciliation is where a graph representation would hurt most.
- **Integration-specific task typing lives in the adapter.** Native keys are `json.RawMessage` in `core` and typed structs inside the owning adapter, which is the only code that reads them.
- **Failed uploads and departed tasks share one attention view.** Same moment of consultation, same go-to action, and the two conditions are frequently causally linked.
- **Below-minimum exclusions are surfaced at upload confirmation and recorded as sentinel records.** Recording them locks the entries and takes them out of the pending count; surfacing them means nothing is excluded without the user seeing it.
- **Upload is modal and blocking.** No edits race the upload, so no check is needed that an entry changed between planning and persisting its result.
- **`net/http` rather than vendor SDKs** for all three integrations.
- **Pure-Go SQLite (`modernc.org/sqlite`)** to keep `CGO_ENABLED=0` and single-binary cross-compilation.
- **No overlap validation in `core`.** `Group` does not validate spans and `PlanUpload` has no abort path. A remote that genuinely cares enforces it inside its own adapter, where a rejection is an ordinary per-op failure.
- **Credentials in the OS keyring, never in SQLite.**

## Open Questions

- **Keyring library.** `zalando/go-keyring` is smaller and shells out to platform tools; `99designs/keyring` supports more backends including an encrypted-file fallback, which matters on a headless or minimal Linux workstation where no Secret Service daemon is running. Whether that fallback case is real for the target environment decides it. The wrapper is designed so this stays a one-file change.
- **Hand-written SQL versus `sqlc`.** Hand-written is assumed above and keeps the dependency count and build steps minimal. `sqlc` generates typed Go from the same SQL and would recover a slice of the compile-time safety the flattened domain gives up, at the cost of a code-generation step in the build. Given how much weight rests on tests rather than types, this deserves an explicit answer rather than a default.
- **Migration runner.** A hand-rolled stepper over `PRAGMA user_version` with embedded `.sql` files is roughly forty lines and adds no dependency; `goose` or `golang-migrate` are standard and add one. Leaning hand-rolled on the same minimal-dependency grounds as the rest.
- **Package name for the domain.** `internal/core` reads as `core.TimeEntry`, which is serviceable but generic. `internal/tracking` is an alternative. Cosmetic, but it appears in every file in the project.
- **Duplicate on crash.** If the process dies after the remote accepts a `Create` but before the local transaction commits, the entries stay pending and the next upload sends them again. Options: accept and document it; or write an in-flight marker before each call and, on restart, list those units for the user to check against the remote before retrying.
- **Pending entries on a departed task.** Plan them and let the `Create` fail, or leave them out of the plan and show them on the confirmation screen? Leaving them out avoids a call that is known to fail; both routes end in the attention list.
- **Note concatenation.** Separator, and what happens when the result exceeds a remote's field limit (Jira worklog comments, Toggl descriptions). Truncation belongs in the adapter, but whether truncation is acceptable is a product call.
- **Modes not yet designed.** Local task management (create, rename, nest, archive), settings and integration setup (base URL, credentials into the keyring), purge, and the trigger for fetching remote tasks (startup, keystroke, or both) have no place in the mode structure above.
- **Database location.** Presumably the platform's user data directory (`os.UserConfigDir` or an XDG data path), with an override flag.
