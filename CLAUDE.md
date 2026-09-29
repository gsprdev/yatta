# YATTA — Working Agreement

YATTA is a local-first, single-user terminal time tracker in Go, with optional upload to one remote system (Jira, Redmine, or Toggl). Single module, single static binary.

## Design documents — read before non-trivial work

| Document | Authority over |
|---|---|
| `docs/YATTA.md` | **Driver.** Product goals, principles, scope, terminology. Nothing below may contradict it. |
| `docs/YATTA-ARCH.md` | Package layout, domain types, pure logic contracts, UI structure. Subordinate to the driver. |
| `docs/YATTA-DATA.md` | SQLite schema, constraints, migrations. Subordinate to the driver. |

If code and a document disagree, the document is presumed right and the code is a bug — unless implementation proved the document wrong, in which case **say so and update the document in the same change**. Never leave the two in conflict.

## Resolved Decisions and Open Questions

Each document ends with **Resolved Decisions** and **Open Questions**. Resolved Decisions record the current thinking and its reasons; they are not locked. When implementation or discussion shows one is wrong, say so and change the document in the same change as the code.

Open Questions are the live agenda. If a task needs an answer to one, name the question and the choice you made, and move it to Resolved Decisions rather than answering it silently in code.

## Invariants that must not be violated

- **Local records are never modified by the upload process.** Rounding and aggregation apply to a derived copy. Upload only links an entry to the record it produced. This is the product's central promise.
- **Upload is one-way and final.** YATTA creates remote records and never updates or deletes them. An uploaded (or excluded) entry is locked; corrections happen in the remote system.
- **No cloud sync, hosted backend, or multi-machine replication.** Data lives on one machine. This rules out whole categories of feature; do not add a network listener, a sync path, or a remote datastore.
- **Existing time entries are never fetched from a remote system.** Remote reads are for task hierarchies only.
- **`internal/core` imports only the standard library.** No UI, database, or HTTP types. Enforce it in review.
- **Rounding, aggregation, and upload planning stay pure.** `core.Group` and `core.PlanUpload` take values and return values — no database, no network, no ambient clock or timezone. All I/O lives at the edges.
- **Rounding and minimums apply to the summed duration of a unit**, never to individual entries before summing.
- **No whole-upload abort.** One failed operation produces failed entries inside an otherwise successful upload. Per-operation results, always.
- **Credentials live in the OS keyring, never in SQLite.** The database stores a lookup key only.
- **A remote task that disappears upstream is soft-deleted, never dropped.** Entries keep their reference; those not yet uploaded are surfaced for reassociation.

## Conventions

- Go, idiomatic and plain. Nil is the discriminator for "this concept does not apply" (`TimeEntry.Upload`, `Task.Remote`). Do not reintroduce interface-based sum types to model these.
- Hand-written SQL, no ORM. `modernc.org/sqlite` — keep `CGO_ENABLED=0` buildable.
- Interfaces are declared at the consumer, not exported from the store. The exception is `remote.Adapter`, which is selected at runtime.
- `core` is tested with table-driven tests and no mocks. The store is tested against a real SQLite file in a temp dir. Adapters are tested against `httptest.Server`.
- Build: `CGO_ENABLED=0 go build ./cmd/yatta`.
- Lint: `golangci-lint run ./...` (config in `.golangci.yml`; also `mise run check`). It enforces the `internal/core` stdlib-only rule and exhaustive switches over the domain enums.

## Terminology — use these words

- **Upload**, not "submit" or "sync". Upload is one-directional and deliberate.
- **Aggregation** means several local entries projected into one remote record at upload time. It never means merging, editing, or deleting local entries.
- **Departed** describes a remote task absent from the latest fetch. **Archived** describes a local task the user has retired. They are different conditions with different handling.
