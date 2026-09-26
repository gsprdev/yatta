# YATTA — Working Agreement

YATTA is a local-first, single-user terminal time tracker in Go, with optional upload to one remote system (Jira, Redmine, or Toggl). Single module, single static binary.

## Design documents — read before non-trivial work

| Document | Authority over |
|---|---|
| `docs/YATTA.md` | **Driver.** Product goals, principles, scope, terminology. Nothing below may contradict it. |
| `docs/YATTA-ARCH.md` | Package layout, domain types, pure logic contracts, UI structure. Subordinate to the driver. |
| `docs/YATTA-DATA.md` | SQLite schema, constraints, migrations. Subordinate to the driver. |

If code and a document disagree, the document is presumed right and the code is a bug — unless implementation proved the document wrong, in which case **say so and update the document in the same change**. Never leave the two in conflict.

## Do not re-litigate

Each document ends with **Resolved Decisions**. Those questions are closed with recorded reasoning. Do not reopen one because a different approach looks cleaner in the moment; if implementation surfaces genuinely new information, raise it explicitly rather than quietly building the alternative.

Each document also ends with **Open Questions**. Those are the live agenda. Do not silently pick an answer to one in passing — if a task requires an answer, name the question, state the choice and why, and record it in Resolved Decisions.

## Invariants that must not be violated

- **Local records are never modified by the upload process.** Rounding and aggregation apply to a derived copy. This is the product's central promise.
- **No cloud sync, hosted backend, or multi-machine replication.** Data lives on one machine. This rules out whole categories of feature; do not add a network listener, a sync path, or a remote datastore.
- **Existing time entries are never fetched from a remote system.** Remote reads are for task hierarchies only.
- **`internal/core` imports only the standard library.** No UI, database, or HTTP types. Enforce it in review.
- **Rounding, aggregation, and upload planning stay pure.** `core.Group` and `core.PlanUpload` take values and return values — no database, no network, no ambient clock or timezone. All I/O lives at the edges.
- **`core.PlanUpload` receives every remote-task entry in the bound, in any phase**, including already-uploaded ones. Passing only pending entries undercounts aggregate records. There is a test for this; do not "optimize" it away.
- **A remote record's state is shared by every entry linked to it.** Aggregated siblings always agree because the store materializes their phase from the record.
- **No whole-upload abort.** One failed operation produces failed entries inside an otherwise successful upload. Per-operation results, always.
- **Credentials live in the OS keyring, never in SQLite.** The database stores a lookup key only.
- **A remote task that disappears upstream is soft-deleted, never dropped.** Entries keep their reference and are surfaced for reassociation.

## Conventions

- Go, idiomatic and plain. Nil is the discriminator for "this concept does not apply" (`TimeEntry.Upload`, `Task.Remote`). Do not reintroduce interface-based sum types to model these.
- Hand-written SQL, no ORM. `modernc.org/sqlite` — keep `CGO_ENABLED=0` buildable.
- Interfaces are declared at the consumer, not exported from the store. The exception is `remote.Adapter`, which is selected at runtime.
- `core` is tested with table-driven tests and no mocks. The store is tested against a real SQLite file in a temp dir. Adapters are tested against `httptest.Server`.
- Build: `CGO_ENABLED=0 go build ./cmd/yatta`.

## Terminology — use these words

- **Upload**, not "submit" or "sync". Upload is one-directional and deliberate.
- **Aggregation** means several local entries projected into one remote record at upload time. It never means merging, editing, or deleting local entries.
- **Departed** describes a remote task absent from the latest fetch. **Archived** describes a local task the user has retired. They are different conditions with different handling.
