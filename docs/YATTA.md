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
- **Upload is deliberate and final.** Pending entries accumulate as the user works, but uploading to a remote system is always a conscious user action — the point at which the user has reviewed what is pending and commits to it. Upload is one-way: once time is uploaded, the remote system is its system of record, and any later correction happens there.

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
- **Attention indicators** — counts for pending uploads, failed uploads, not-yet-uploaded entries whose remote task has departed, and entries with no task.

Everything else is reached by keystroke from this resting view.

### Time Entries

- Each entry records a time block (via start/stop timer or manual entry).
- Duration is always derived: `duration = end − start`. There is no separate duration field.
- Each entry **should** have an associated task. A task is required only for upload: an entry, or a running timer, may be recorded without one and is flagged as unassigned until the user picks a task.
- Each entry **may** have a free-text note.
- The UI works exclusively with **local records**. All editing, display, and interaction is against the local copy.
- Entries can be freely edited until they have been uploaded. Once uploaded, an entry is locked: it remains visible for review and recall, but it cannot be changed.
- An entry that has not been uploaded can be discarded. An uploaded entry is never deleted individually.
- Local records have a natural lifespan once their time has been reported, but when they stop being useful is the user's call, so cleanup is a deliberate **purge** of every entry before a user-chosen date, in any state. Purge warns before it runs when the range holds entries that were never uploaded: a plain warning for pending or unassigned entries, which may be normal for how the user works, and a louder one for failed entries, which are unresolved errors.
- **Search and recall are primary interactions, not conveniences.** Finding an earlier entry, filtering the list, and resuming a recent task are among the most frequent actions in daily use — a timer is far more often restarted against something already tracked than created from nothing. Filtering and resume-from-recent must be immediate.
- Upload failures must be discoverable. YATTA provides an **error list** enumerating entries whose last upload failed, each with a *go-to* action that jumps to the affected entry in the primary time entry interface. The error list is purely for discovery and navigation — it is not a sync queue, and every correction is made on the entry itself in the primary UI. A failed entry was never uploaded, so it is still editable. When aggregation combined several entries into one failed upload, all of them appear in the error list, since all are affected by the same failure.

### Local and Remote Records

Local records and remote records are stored separately and linked by reference. The local record is what actually happened; the remote record is the business-facing projection of it, written once at upload. Upload policy — rounding and, optionally, aggregation — determines the shape of that projection. Keeping both allows the local timeline to remain honest and recognizable to the user, while the remote representation reflects the form that downstream consumers require.

- **Local record:** The user's authoritative copy of what happened. What the UI displays and the user edits, until it is uploaded.
- **Remote record:** A stored representation of what was actually uploaded, including any rounding or aggregation applied. **A remote record originates from one or more local records** — e.g. three local entries against the same task on the same day, rolled into a single Jira worklog. Its duration is the combined time of those entries with the upload policy applied, and its note is the concatenation of the distinct notes across them.
- A local record linked to a remote record is shown together with it in a merged view — showing, for example, that a 23-minute local entry was uploaded as 30 minutes, or that three entries became one worklog. The merged view is for review only.
- The upload process never changes an entry's time, task, or note. It only links the entry to the record it produced, which is what locks it.
- **Upload is one-way.** YATTA creates remote records and never updates or deletes them. Once uploaded, the remote system is the system of record for that time; a correction is made there, not in YATTA.
- Every remote-bound entry is in one of three conditions: **pending** (not yet uploaded, editable), **failed** (an upload was attempted and rejected; still editable, retried on the next upload), or **uploaded** (linked to a remote record, locked). An entry excluded by a minimum-duration rule counts as handled — see Rounding.

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
- **Reassociation is not a distinct feature.** Changing an entry's task is the same act as choosing it in the first place — the ordinary task selector on the entry. It applies to entries not yet uploaded; an uploaded entry against a departed task is history, needs no action, and is not flagged.

### Remote Integration (0 or 1)

- At most one remote system may be active.
- Remote systems serve two purposes:
  1. **Task source** — the remote task hierarchy is fetched and made available for associating with entries.
  2. **Reporting target** — uploaded entries are written to the remote system.
- Only entries associated with tasks originating from the remote system are eligible for upload. Entries on local tasks remain local-only and are never uploaded.
- An entry maps to at most one remote record, matching the single-active-integration principle.

### Pending Entries

Entries on remote tasks that have not yet been uploaded are **pending**. There is no separate log of intended operations: what gets uploaded is derived from the pending entries as they stand at upload time.

- The purpose of deferred upload is to let the user freely edit local data without causing volatility in the remote system of record. The remote system only ever sees clean, intentional snapshots.
- Upload is always explicit. The user decides when pending entries are ready to send.
- There is no dedicated "pending" view. Pending state is surfaced as an indicator on entries within the normal time entry interface, plus a count in the always-visible status area.
- Once entries are uploaded, the remote-assigned identifier and the remote record are stored locally, linked to the local entry or entries.

### Upload Scope

- An upload action is not tied to any particular reporting period.
- The user may optionally specify an **end date (inclusive)** to limit which pending entries are included in a given upload — for example, uploading only through last Sunday while leaving the current week pending.
- No minimum scope is implied; the default is all pending entries.
- Entries on a departed remote task are attempted like any other and fail, landing in the error list. That is deliberate: the task must be reassigned before that time can be uploaded, and the failure is what makes that visible.
- Upload is a blocking operation. While it runs, the user watches its per-entry results arrive and cannot edit entries. One rejected record does not abort the rest: each record succeeds or fails on its own.

---

## Rounding

Rounding applies only at upload time and only to remote-bound time. It is applied to a derived copy before it is sent; the result is stored as the **remote record**. The local record is never modified by the upload process.

Rounding and minimums are evaluated on the time a remote record will carry — a single entry's duration, or under aggregation the **sum of the group's raw durations**. Individual entries are never rounded before summing.

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
- If the time (after rounding) falls below the minimum:
  - Round up to minimum, or
  - Exclude it from the upload

A duration that rounds to zero is excluded, unless a minimum with round-up behavior raises it — so with no minimum configured, zero is always excluded. Round to nearest rounds a value exactly halfway up.

An excluded entry or group is not left pending. It is recorded as **handled with no upload** — a sentinel remote record with nothing sent — and the entries are locked like any uploaded entry. Exclusions are listed on the upload confirmation before anything is sent. Leaving them pending instead would make them reappear at every upload and keep the pending count from ever reaching zero.

### Aggregation

Aggregation combines multiple local entries into a single remote record at upload time — for example, three entries against the same task on the same day, uploaded as one Jira worklog. This happens purely in the upload transform: local records are never merged or edited by aggregation, and each keeps its own identity and timeline in the UI.

Aggregation exists because remote systems often impose conventions that have nothing to do with how the work was actually tracked — for example, a company Jira convention of one worklog entry per day per issue, rounded to the nearest 15 minutes, minimum 15 minutes. That convention is a property of the remote system, not of the source data, and it may change over time without any change to the underlying local records — which is exactly why rounding and aggregation are applied only at the upload boundary rather than to local records.

**Aggregation key** (select one):

- None *(default)* — every local entry produces its own remote record.
- By task and day — pending entries targeting the same remote task, falling on the same local calendar day, are grouped into one remote record.

An aggregate record's start is the earliest member's start; its duration is the group's summed duration with the policy applied.

Its note combines the members' notes in start order: each note is trimmed of surrounding whitespace, empty and duplicate notes are dropped, every note but the last gets a trailing period if it does not already end with one, and the notes are joined with a single space. `Fixed login bug` and `Wrote tests.` become `Fixed login bug. Wrote tests.`

Aggregation groups only the entries in one upload. Entries for a task and day that was already uploaded are grouped on their own the next time, so uploading twice in one day produces two remote records for that task and day. That is the expected result of one-way upload, not a condition YATTA detects or corrects.

"Day" is the local calendar day (device timezone) at the time of upload — not the UTC date the instant is stored under. This is consistent with local timezone being applied only at the UI render/interaction boundary and never persisted, but it is worth stating explicitly, since a grouping boundary near midnight is exactly the kind of place a UTC/local mismatch would silently misgroup entries.

Aggregation is a deliberately narrow concern, separate from full cross-system timesheet reporting (for example, rolling up ticketed and non-ticketed work into one NetSuite-style total across systems). That is a different capability — see Resolved Decisions and Scope Boundaries.

---

## Data Model Notes

- The schema must accommodate optional fields driven by integration type.
- Remote task records carry integration-specific metadata; local tasks do not.
- Pending state is derived from the entries themselves; there is no operation queue.
- Local and remote entry records are stored separately and linked by reference. The remote record preserves the uploaded values (post-aggregation, post-rounding); the local record is the user's copy, editable until linked. Under aggregation, several local records link to the same remote record.

---

## Scope Boundaries

| In Scope | Out of Scope |
|---|---|
| Local time entry (timer + manual) | Multi-user / team features |
| A persistent, keyboard-driven terminal interface | Graphical desktop client |
| Always-visible current timer and today's running total | Mobile clients |
| Search, filter, and resume-from-recent over entries | Cloud sync, hosted backend, or any multi-machine replication |
| Hierarchical local task management | Browser-based interface |
| Finding remote tasks by their label (e.g. a ticket number) | |
| One optional remote integration | Reading existing entries from remote |
| Deliberate, one-way upload with optional date bound | Reporting / analytics UI beyond the current-day total |
| Rounding at upload time (stored as remote record) | Full cross-system timesheet reporting (e.g. summarizing ticketed and non-ticketed work into one NetSuite-style total) |
| Aggregation at upload time (by task + day, into the remote record) | Scriptable or user-defined rounding rules |
| Merged local/remote view per entry (and per aggregate group), read-only | Aggregation, merging, or editing of local records themselves |
| Purge of local records before a chosen date | Updating or deleting time already uploaded — corrected in the remote system |

---

## Target Remote Integrations (Initial)

Three integrations are planned to drive the abstraction layer design:

| System | Primary Purpose | Notes |
|---|---|---|
| Jira | Project/issue tracker | Task hierarchy: Initiative → Epic → Story → Task |
| Redmine | Project/issue tracker | Task hierarchy: Project → Version → Issue |
| Toggl | Time tracker | Task hierarchy: Workspace → Client → Project → Task; represents a peer tool rather than an issue tracker |

Remote tasks are found by their human-facing label as well as their name — a Jira key such as `PROJ-123`, a Redmine issue number such as `#4521`.

Toggl is intentionally included as a third integration type — it is a time-tracking-first system rather than an issue tracker, which will stress-test the integration abstraction differently than Jira or Redmine.

---

## Definition of Done

This iteration is done when, against each of Jira, Redmine, and Toggl, a user can:

- start and stop a timer, and resume one from a recent entry;
- find a remote task by name or by its label (e.g. ticket number) and record time against it;
- upload that time successfully.

---

## Resolved Decisions

- **Terminal interface.** The target user works at a terminal already, so a terminal interface meets them where they are rather than asking them to switch contexts to record time. Beyond audience fit, the Scope Boundaries above exclude reporting and analytics, which is the category where a graphical interface holds a genuine advantage; what remains in scope is a list, a tree picker, a form, a status area, and an error list, all of which a terminal renders natively and navigates faster. A terminal interface also composes with an always-running pane in a way a windowed application does not.
- **No mobile client.** Mobile is incompatible with **Data isolation**. Data cannot both be usefully available on a phone and be prevented from syncing; and if the data must live in exactly one place, the workstation already trusted with the same company context is the correct place. Convenience does not outweigh this.
- **No browser-based interface in this iteration.** A local process serving a browser UI is a plausible future delivery surface (see Scope of This Iteration) but is out of scope here: it is substantial additional machinery for no gain over a terminal interface for the target user, and it introduces a listening socket that would require its own security assessment.
- **Command-line-only is not sufficient.** A pure CLI would be lowest-friction for the single act of starting a timer, but it cannot reasonably serve search, resume-from-recents, or correcting a recent entry — all of which need a viewport and are primary interactions rather than edge cases. The friction advantage a CLI holds also assumes an application one must switch to, which does not apply to an application intended to remain open. A set of CLI verbs is a plausible *adjunct* for scripting and for starting a timer from a shell already in hand, but it is not the interface and nothing in the design may assume it exists.
- **Implementation language and toolkit: Go with Bubble Tea.** The interface is dominated by list, filter, select, and edit interactions, and Bubble Tea's component library supplies all of them — including list filtering, which maps directly onto the search and recall requirement. Go's standard library covers the three HTTP integrations without an async runtime, and its message-passing model maps cleanly onto per-operation upload results. Dependency weight also favors Go: a pure-Go SQLite driver and standard-library HTTP yield a small dependency tree and a single static binary. Rust with Ratatui was the principal alternative and remains a strong fit for the domain model, but it would require building the interactive form and input layer by hand and carries a larger transitive dependency graph for the same feature set. The cost of Go is a weaker type system for expressing the domain's variants; this is accepted, mitigated by exhaustiveness linting where unions remain, and offset by the fact that the most intricate logic in the product is pure and I/O-free and therefore well covered by table-driven tests. See `YATTA-ARCH.md`.
- **Multiple simultaneous remotes, or one entry feeding several remotes:** Rejected. A single local entry maps to at most one remote, matching the "zero or one active integrations" principle. Letting one entry feed several remotes at once (e.g. Jira and NetSuite together) would be difficult to build an interface for, track, or reason about — and it isn't what the underlying need actually calls for.
- **Full cross-system timesheet reporting (the NetSuite case):** Out of scope, and deliberately not modeled as an extension of the remote upload mechanism. Summarizing ticketed and non-ticketed work into one full total belongs to a distinct reporting/summarization capability, not to the entry-to-remote-record relationship — the same underlying reason two separate systems (e.g. Jira and NetSuite) exist for it today rather than one.
- **Upload is one-way and final.** YATTA only ever creates remote records. An uploaded entry is locked, and corrections to uploaded time are made in the remote system of record. This removes remote update and delete entirely, and with it the questions of re-uploading edited groups, stale remote copies, and records whose members have left.
- **Discard before upload; purge by date after.** An entry not yet uploaded can be discarded. Uploaded entries are never deleted individually; the user clears out old local records by purging every entry before a chosen date. Purge covers every state, and warns first when the range holds entries never uploaded — louder for failed entries than for pending or unassigned ones.
- **A task is required only for upload.** Unassigned entries and timers are allowed and counted as needing attention, since recording first and classifying later is the lowest-friction path.
- **Today's running total is in scope.** It belongs to the always-visible working surface, not to reporting.
- **Zero after rounding is excluded** unless a round-up minimum raises it. It gets the same sentinel as a below-minimum exclusion.
- **Entries on departed tasks fail at upload.** They are not filtered out beforehand; the failure puts them in the error list, where the user reassigns them.
- **Repeated uploads are independent.** Two uploads on the same day produce two records for the same task and day. This is expected, not handled.
- **Known limitation: duplicate after a crash.** If YATTA stops after the remote accepts a record but before the result is saved locally, those entries stay pending and the next upload sends them again. No reasonable prevention exists for a create-only interface; the user corrects the duplicate in the remote system.
- **Aggregation start/anchor:** An aggregate remote record's reported start is the earliest contributing local entry's start; its duration is the sum of the members' raw durations, with rounding and minimum applied once to that sum. Its note is the distinct member notes concatenated.
- **Below-minimum exclusion is recorded, not deferred.** An excluded entry or group gets a sentinel remote record marking it handled with nothing sent, so it does not stay pending forever.
- **Upload blocks.** Entries cannot be edited while an upload runs, so what is uploaded is exactly what was confirmed.
- **Aggregation day boundary:** "Day" for the task+day grouping key is the local calendar day (device timezone) at upload time, not the UTC date the instant is stored under.
- **Upload bound granularity:** The optional upload bound is an inclusive local *date*, normalized to end-of-day. Honoring it as a date rather than an arbitrary instant guarantees it can never bisect a task+day aggregation group.
- **Cross-entry/cross-group overlap validation:** Not enforced by rounding or upload logic, and there is no whole-upload abort behavior for it. Rounding and aggregation may produce entries or groups with overlapping time ranges without complaint — most remotes (Jira included) care about total duration per task/day, not literal time-of-day precision, so a generic overlap check would solve a problem the target integrations don't have, and would force awkward, often-unresolvable local massaging on top of a projection that is already an approximation once aggregation is summarizing several entries into one span. If a remote genuinely rejects overlapping entries, that validation and any conflict resolution belongs in that remote's own adapter — a rejection there is an ordinary upload failure for the affected record, handled the same as any other adapter error, not a special whole-upload abort.

## Open Questions

None at present.
