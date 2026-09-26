# YATTA — Yet Another Time Tracking Application

## Vision

YATTA is a local-first, single-user time tracking client designed around the person doing the work — not the systems that consume their reports.

Most time reporting tools are built for the managers and analysts who read the data. As a result, the people actually entering time face significant friction: unfamiliar interfaces, poor UX, and tooling shaped by the destination system rather than the act of recording. Inaccurate reporting is often a direct consequence of poor tooling. YATTA inverts this priority.

The core insight: **the ideal local experience for honest time tracking is the same regardless of where that time ultimately gets reported.** Whether the destination is Jira, Redmine, NetSuite, Toggl, TimeCamp, Replicon, or nothing at all, the user's workflow is the same. YATTA owns that local experience and treats remote systems as optional, downstream concerns.

---

## Scope of This Iteration

**This iteration builds one thing well: a terminal time tracker for a technical individual contributor.** The reference user is a developer or similarly technical worker who already lives in a terminal, tracks their own time in detail, and reports it into a single issue tracker or time system.

That target is deliberately narrow, and the narrowness is the point. Designing for users and delivery surfaces that are not yet in evidence trades focus on the core recording-and-upload workflow for flexibility that may never be exercised. It also has no natural stopping point: every additional hypothetical audience reopens settled questions. Committing to one concrete user with known needs is what makes it possible to finish.

**Serving broader audiences remains a real goal, explicitly deferred.** Two paths are plausible and neither is committed:

1. **An additional delivery surface over the same core.** The domain, persistence, and integration layers make no assumptions about how the interface is drawn, so a second frontend — for example a local process serving a browser UI over loopback — could be added without redesigning what sits beneath it.
2. **A second major version designed from evidence.** This iteration will produce real usage: actual entries, actual upload histories, actual rounding and aggregation conventions met in practice, and concrete examples of where the model fits and where it strains. Requirements refined against that evidence are worth more than requirements inferred in advance, and may justify a clean redesign rather than an extension.

What this means for decisions made now: the layering below the interface stays free of interface assumptions, because that costs nothing and keeps path 1 open. But no work is done *for* either path in this iteration, and a feature justified only by a hypothetical future audience is out of scope.

Note that path 1 would require its own security assessment rather than inheriting this iteration's. A process listening on a socket — even loopback-only, even for the same user — is a different exposure from a process that only reads and writes local files, and the **Data isolation** principle below would need to be re-argued against it, not assumed to carry over.

---

## Design Principles

- **User-first.** The focus is on the person recording time, not the consumers of that data.
- **Terminal-native and always-present.** YATTA is a keyboard-driven terminal application, intended to be left running in a pane or window rather than launched per interaction. Its resting state is a live view of what is being tracked right now. Availability at a glance is a feature, not a side effect.
- **Data isolation.** Time entries carry client, employer, and project detail that must not be replicated casually. YATTA's data lives on exactly one already-trusted machine — the workstation that holds the same company context the entries describe. It is never synced, replicated, hosted, or mirrored to a second device. This principle is load-bearing: it rules out mobile clients and hosted backends outright, and it constrains every future feature.
- **Local and single-user.** YATTA runs locally. No account, no server required.
- **Portable across desktop platforms.** A terminal application runs on Linux, macOS, and Windows, and distributes as a single self-contained binary per platform.
- **Minimize friction.** Reducing friction is a win for everyone — easier entry leads to more accurate data.
- **Remote systems are optional.** Zero or one remote integrations may be active at a time.
- **Local data is never overwritten by remote systems.** Existing time entries are never fetched from a remote source.
- **Upload is deliberate.** Pending remote transactions accumulate as the user works, but uploading to a remote system is always a conscious user action.

---

## Terminology

- **Upload** is the standard term for sending time entries to a remote system — used as both a verb ("upload entries") and a noun ("the pending upload"). It is preferred over "submit" (which implies a formal period or contract) and "sync" (which implies bidirectionality).
- **Aggregation** is the standard term for combining several local entries into a single remote record at upload time (e.g. one Jira worklog representing three same-day entries against the same issue). It is distinct from — and never implies — merging, editing, or deleting the underlying local entries themselves.

---

## Core Concepts

### The Working Surface

YATTA is one persistent application, not a set of invocations. Because it is expected to stay open, three things are always visible without navigation:

- **The current timer** — what is running, against which task, and for how long. This is the single most-consulted piece of information in the product and it must never require a keystroke to see.
- **Today's accumulated time** — a running total for the current local day. This is not reporting or analytics; it is the ambient feedback that makes under-recording visible while there is still time to fix it.
- **Attention indicators** — counts for pending uploads, failed uploads, and entries whose remote task has departed.

Everything else is reached by keystroke from this resting view.

### Time Entries

- Each entry records a time block (via start/stop timer or manual entry).
- Duration is always derived: `duration = end − start`. There is no separate duration field.
- Each entry **should** have an associated task.
- Each entry **may** have a free-text note.
- The UI works exclusively with **local records**. All editing, display, and interaction is against the local copy.
- Entries can be freely edited or deleted up until they have been uploaded to a remote system.
- **Search and recall are primary interactions, not conveniences.** Finding an earlier entry, filtering the list, and resuming a recent task are among the most frequent actions in daily use — a timer is far more often restarted against something already tracked than created from nothing. Filtering and resume-from-recent must be immediate.
- Upload failures must be discoverable. YATTA provides an **error list** enumerating entries whose last upload failed, each with a *go-to* action that jumps to the affected entry in the primary time entry interface. The error list is purely for discovery and navigation — it is not a sync queue, and every correction is made on the entry itself in the primary UI. When aggregation links several entries to one failed remote record, all of them appear in the error list, since all are affected by the same failure.

### Local and Remote Records

Local records and remote records are stored separately and linked by reference. The local record is what actually happened; the remote record is the business-facing projection of it. Upload policy — rounding and, optionally, aggregation — determines the shape of that projection. Keeping both allows the local timeline to remain honest and recognizable to the user, while the remote representation reflects the form that downstream consumers require.

- **Local record:** The user's authoritative copy. Always present. What the UI displays and the user edits.
- **Remote record:** A stored representation of what was actually uploaded to the remote system, including any rounding or aggregation applied at upload time. Present only after a successful upload.
- A local record may be linked to a remote record. When linked, both can be represented together in a merged view — showing, for example, that a 23-minute local entry was uploaded as 30 minutes.
- **A remote record may summarize several local records, not just one** — e.g. three local entries against the same task on the same day, rolled into a single Jira worklog. Each local record retains its own identity, timeline, and editability; only the remote projection combines them. The merged view shows the whole group together against the one remote value.
- The local record is never modified by the upload process. The remote record captures the uploaded values, including any fidelity loss from rounding or aggregation.
- State tracking (pending, uploaded, failed) is relative to the remote record. When a remote record summarizes multiple local records, that state applies to the group as a whole — every member shares the same fate. Before a group's first successful upload, an individual local record not yet linked to any remote record tracks its own pending/failed state directly, since no shared remote record exists yet to hold it.
- If a previously uploaded entry is edited locally, it becomes pending upload again (as an Update). The remote record continues to reflect the last successfully uploaded state until the next upload. The UI makes this divergence visible. The same applies when a member is added to or removed from an already-uploaded aggregate group: the shared remote record — and therefore every member linked to it, including ones that were not themselves touched — becomes pending again.

### Task Hierarchies

- Tasks are hierarchical at **arbitrary depth**.
- A **local task hierarchy** is always available, independent of any remote integration.
- When a remote system is configured, its task hierarchy is fetched separately and exists alongside (not replacing) the local hierarchy.
- Remote task hierarchies have their own distinct structures per integration type, for example:
  - Redmine: Project → Version → Issue
  - Jira: Initiative → Epic → Story → Task
  - Toggl: Workspace → Client → Project → Task
- The local and remote task trees are **distinct namespaces** — an entry is associated with either a local task or a remote task, never both.
- **A remote task that disappears is never silently dropped.** If a re-fetch removes a remote task that existing entries point to, the association is preserved and the affected entries are flagged for the user's attention rather than quietly losing their reference. The user can then reassociate them with a current task. What the time was originally recorded against remains visible until they do — consistent with YATTA's refusal to lose source truth.
- **Reassociation is not a distinct feature.** Changing an entry's task is the same act as choosing it in the first place — the ordinary task selector on the entry. If the entry had already been uploaded under its previous remote task, changing to a different task means the prior remote record is deleted on the next upload (and, if the new task is also remote, a new record created in its place) — or, under aggregation, the entry simply stops being a member of its old group.

### Remote Integration (0 or 1)

- At most one remote system may be active.
- Remote systems serve two purposes:
  1. **Task source** — the remote task hierarchy is fetched and made available for associating with entries.
  2. **Reporting target** — uploaded entries are written to the remote system.
- Only entries associated with tasks originating from the remote system are eligible for upload. Entries on local tasks remain local-only and are never uploaded.
- An entry maps to at most one remote record, matching the single-active-integration principle.

### Pending Transactions

Unsubmitted remote operations are represented as **pending transactions** — a set of intended Create, Update, or Delete operations on individual time entries or, under aggregation, on groups of them.

- A transaction set is not a log of every intermediate change. If an entry is created, edited three times, and then deleted before upload, the net result is nothing to upload.
- The purpose of deferred upload is to let the user freely edit local data without causing volatility in the remote system of record. The remote system only ever sees clean, intentional snapshots.
- Upload is always explicit. The user decides when pending entries are ready to send.
- There is no dedicated "pending transactions" view. Pending state is surfaced as an indicator on entries within the normal time entry interface, plus a count in the always-visible status area.
- Once an entry (or, under aggregation, a group) is uploaded, the remote-assigned identifier and the remote record are stored locally, linked to the local entry or entries.

### Upload Scope

- An upload action is not tied to any particular reporting period.
- The user may optionally specify an **end date (inclusive)** to limit which pending entries are included in a given upload — for example, uploading only through last Sunday while leaving the current week pending.
- No minimum scope is implied; the default is all pending entries.

---

## Rounding

Rounding applies only at upload time and only to remote-bound entries. Rounding is applied to a derived copy of each entry before it is sent; the result is stored as the **remote record**. The local record is never modified by the upload process.

### Rounding Options

**Increment** (select one):

- None *(default)*
- 15 minutes (¼ hr)
- 30 minutes (½ hr)
- 60 minutes (1 hr)

**Direction** (select one, applies when increment is set):

- Round to nearest *(default)*
- Round up
- Round down

**Minimum duration** (optional):

- A minimum duration threshold may be configured.
- If an entry (or, under aggregation, a group's summed duration) falls below the minimum after rounding:
  - Round up to minimum, or
  - Exclude the entry (or group) from the upload (local records are preserved)

Entries excluded by a minimum-duration rule are reported at upload confirmation. An entry that is silently skipped would remain pending indefinitely with no visible cause, which is precisely the kind of quiet data loss the product exists to prevent.

### Aggregation

Aggregation combines multiple local entries into a single remote record at upload time — for example, three entries against the same task on the same day, uploaded as one Jira worklog. This happens purely in the upload/rounding transform: local records are never merged, edited, or deleted by aggregation, and each keeps its own identity and timeline in the UI. What changes is only how many local records a given remote record represents.

Aggregation exists because remote systems often impose conventions that have nothing to do with how the work was actually tracked — for example, a company Jira convention of one worklog entry per day per issue, rounded to the nearest 15 minutes, minimum 15 minutes. That convention is a property of the remote system, not of the source data, and it may change over time without any change to the underlying local records — which is exactly why rounding and aggregation are applied only at the upload boundary rather than to local records.

**Aggregation key** (select one):

- None *(default)* — every local entry produces its own remote record.
- By task and day — local entries targeting the same remote task, falling on the same local calendar day, are grouped into one remote record.

"Day" is the local calendar day (device timezone) at the time of upload — not the UTC date the instant is stored under. This is consistent with local timezone being applied only at the UI render/interaction boundary and never persisted, but it is worth stating explicitly, since a grouping boundary near midnight is exactly the kind of place a UTC/local mismatch would silently misgroup entries.

Aggregation is a deliberately narrow concern, separate from full cross-system timesheet reporting (for example, rolling up ticketed and non-ticketed work into one NetSuite-style total across systems). That is a different capability — see Resolved Decisions and Scope Boundaries.

---

## Data Model Notes

- The schema must accommodate optional fields driven by integration type.
- Remote task records carry integration-specific metadata; local tasks do not.
- The representation of pending transactions is an implementation detail and does not need to mirror queue semantics.
- Local and remote entry records are stored separately and linked by reference. The remote record preserves the uploaded values (post-rounding, post-aggregation); the local record is the user's editable copy. Under aggregation, several local records may link to the same remote record.

---

## Scope Boundaries

| In Scope | Out of Scope |
|---|---|
| Local time entry (timer + manual) | Multi-user / team features |
| A persistent, keyboard-driven terminal interface | Graphical desktop client |
| Always-visible current timer and today's running total | Mobile clients |
| Search, filter, and resume-from-recent over entries | Cloud sync, hosted backend, or any multi-machine replication |
| Hierarchical local task management | Browser-based interface |
| One optional remote integration | Reading existing entries from remote |
| Deliberate upload with optional date bound | Reporting / analytics UI beyond the current-day total |
| Rounding at upload time (stored as remote record) | Full cross-system timesheet reporting (e.g. summarizing ticketed and non-ticketed work into one NetSuite-style total) |
| Aggregation at upload time (by task + day, into the remote record) | Scriptable or user-defined rounding rules |
| Merged local/remote view per entry (and per aggregate group) | Aggregation, merging, or editing of local records themselves |

---

## Target Remote Integrations (Initial)

Three integrations are planned to drive the abstraction layer design:

| System | Primary Purpose | Notes |
|---|---|---|
| Jira | Project/issue tracker | Task hierarchy: Initiative → Epic → Story → Task |
| Redmine | Project/issue tracker | Task hierarchy: Project → Version → Issue |
| Toggl | Time tracker | Task hierarchy: Workspace → Client → Project → Task; represents a peer tool rather than an issue tracker |

Toggl is intentionally included as a third integration type — it is a time-tracking-first system rather than an issue tracker, which will stress-test the integration abstraction differently than Jira or Redmine.

---

## Resolved Decisions

- **Terminal interface.** The target user works at a terminal already, so a terminal interface meets them where they are rather than asking them to switch contexts to record time. Beyond audience fit, the Scope Boundaries above exclude reporting and analytics, which is the category where a graphical interface holds a genuine advantage; what remains in scope is a list, a tree picker, a form, a status area, and an error list, all of which a terminal renders natively and navigates faster. A terminal interface also composes with an always-running pane in a way a windowed application does not.
- **No mobile client.** Mobile is incompatible with **Data isolation**. Data cannot both be usefully available on a phone and be prevented from syncing; and if the data must live in exactly one place, the workstation already trusted with the same company context is the correct place. Convenience does not outweigh this.
- **No browser-based interface in this iteration.** A local process serving a browser UI is a plausible future delivery surface (see Scope of This Iteration) but is out of scope here: it is substantial additional machinery for no gain over a terminal interface for the target user, and it introduces a listening socket that would require its own security assessment.
- **Command-line-only is not sufficient.** A pure CLI would be lowest-friction for the single act of starting a timer, but it cannot reasonably serve search, resume-from-recents, or correcting a recent entry — all of which need a viewport and are primary interactions rather than edge cases. The friction advantage a CLI holds also assumes an application one must switch to, which does not apply to an application intended to remain open. A set of CLI verbs is a plausible *adjunct* for scripting and for starting a timer from a shell already in hand, but it is not the interface and nothing in the design may assume it exists.
- **Implementation language and toolkit: Go with Bubble Tea.** The interface is dominated by list, filter, select, and edit interactions, and Bubble Tea's component library supplies all of them — including list filtering, which maps directly onto the search and recall requirement. Go's standard library covers the three HTTP integrations without an async runtime, and its message-passing model maps cleanly onto per-operation upload results. Dependency weight also favors Go: a pure-Go SQLite driver and standard-library HTTP yield a small dependency tree and a single static binary. Rust with Ratatui was the principal alternative and remains a strong fit for the domain model, but it would require building the interactive form and input layer by hand and carries a larger transitive dependency graph for the same feature set. The cost of Go is a weaker type system for expressing the domain's variants; this is accepted, mitigated by exhaustiveness linting where unions remain, and offset by the fact that the most intricate logic in the product is pure and I/O-free and therefore well covered by table-driven tests. See `YATTA-ARCH.md`.
- **Multiple simultaneous remotes, or one entry feeding several remotes:** Rejected. A single local entry maps to at most one remote, matching the "zero or one active integrations" principle. Letting one entry feed several remotes at once (e.g. Jira and NetSuite together) would be difficult to build an interface for, track, or reason about — and it isn't what the underlying need actually calls for.
- **Full cross-system timesheet reporting (the NetSuite case):** Out of scope, and deliberately not modeled as an extension of the remote upload mechanism. Summarizing ticketed and non-ticketed work into one full total belongs to a distinct reporting/summarization capability, not to the entry-to-remote-record relationship — the same underlying reason two separate systems (e.g. Jira and NetSuite) exist for it today rather than one.
- **Aggregation start/anchor:** An aggregate remote record's reported start is the earliest contributing local entry's start; its duration is the sum of the group's rounded durations.
- **Aggregation day boundary:** "Day" for the task+day grouping key is the local calendar day (device timezone) at upload time, not the UTC date the instant is stored under.
- **Upload bound granularity:** The optional upload bound is an inclusive local *date*, normalized to end-of-day. Honoring it as a date rather than an arbitrary instant guarantees it can never bisect a task+day aggregation group.
- **Cross-entry/cross-group overlap validation:** Not enforced by rounding or upload logic, and there is no whole-upload abort behavior for it. Rounding and aggregation may produce entries or groups with overlapping time ranges without complaint — most remotes (Jira included) care about total duration per task/day, not literal time-of-day precision, so a generic overlap check would solve a problem the target integrations don't have, and would force awkward, often-unresolvable local massaging on top of a projection that is already an approximation once aggregation is summarizing several entries into one span. If a remote genuinely rejects overlapping entries, that validation and any conflict resolution belongs in that remote's own adapter — a rejection there is an ordinary upload failure for the affected record, handled the same as any other adapter error, not a special whole-upload abort.

## Open Questions

- **Is today's running total genuinely in scope?** It is specified above as a property of the always-present working surface rather than as reporting, on the grounds that it changes recording behavior while the day is still correctable. The boundary against "reporting / analytics UI" is real but thin, and the answer determines whether a week-to-date total or a per-task daily breakdown is a natural extension or a scope violation. If the total is judged to be reporting, it should be removed from Core Concepts and Scope Boundaries together.
- **What happens when an already-uploaded group falls below the minimum duration after an edit?** The policy says exclude, but the group already has a remote record. Deleting it upstream is the consistent reading; leaving it stale contradicts "divergence must be visible"; treating the exclusion as inapplicable to settled groups is the least surprising but the least principled.
