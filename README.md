# yatta

A user-focused time tracking client: a local-first, single-user terminal app for recording your own time honestly, and, if you need to, uploading it to whatever reporting system your company requires. Jira, Redmine, and Toggl are supported today.

## Why yatta exists

Most time reporting tools are built for the managers and analysts who read the data. The people actually entering time are left with unfamiliar interfaces, poor UX, and tooling shaped by the destination system rather than by the act of recording. Inaccurate reporting is often a direct consequence of poor tooling.

yatta inverts that priority. Its core insight is that **the ideal local experience for honest time tracking is the same regardless of where that time ultimately gets reported.** Whether the destination is Jira, Redmine, Toggl, some other system, or nothing at all, your workflow is the same. yatta owns that local experience and treats remote systems as optional, downstream concerns.

## What yatta is

- **For one person, at a terminal.** This iteration builds one thing well: a terminal time tracker for a technical individual contributor who already lives in a terminal and tracks their own time in detail.
- **Always present.** A keyboard-driven application meant to be left running in a pane. Its resting state is a live view of the current timer, today's accumulated time, and anything needing attention.
- **Local-first.** Your data lives in a local SQLite file on one machine. No account, no server, no sync, no hosted backend. Time entries carry client, employer, and project detail that should not be replicated casually, so yatta never copies them anywhere else.
- **Low friction.** Search and resume-from-recent are primary interactions, and a task is only required at upload time, so you can record first and classify later. Easier entry leads to more accurate data.
- **Optionally connected.** Zero or one remote system may be active. It supplies your task hierarchy and receives your time; yatta never reads existing time entries back from it.
- **Deliberate about upload.** Entries accumulate locally and stay freely editable. Uploading is always an explicit act, one-way and final: yatta creates remote records and never updates or deletes them, and corrections happen in the remote system.
- **Honest about what happened.** Rounding and aggregation (for example, one worklog per task per day, rounded to 15 minutes) are applied only to a copy at upload time. Your local records are never modified, so your timeline stays true even when a remote system's conventions change.

## What yatta is not

Multi-user or team features, cloud sync, mobile or browser clients, and reporting or analytics beyond today's running total are all out of scope by design. The narrow focus is deliberate; it is what makes it possible to finish.

## Why "yatta"?

yatta may have been named for the Japanese やった ("I did it!") an exclamation of excitement and accomplishment.
This name reflects the joy and satisfaction we all feel when submitting our timesheets.
Alternatively, it might stand for "Yet Another Time Tracking Application".
We may never know which for certain.

## Getting started

yatta is a single static Go binary with no runtime dependencies. The quickest route is [mise](https://mise.jdx.dev), which installs the pinned Go toolchain and provides the project's tasks:

```sh
git clone https://github.com/gsprdev/yatta
cd yatta
mise install   # installs the Go toolchain from mise.toml
mise run build # produces ./yatta
./yatta
```

Or, with Go 1.25 or newer already installed:

```sh
CGO_ENABLED=0 go build ./cmd/yatta
./yatta            # data lives in your user data directory
./yatta --db x.db  # or somewhere else
```

Press `?` for keys. Remote integrations are set up under settings (`,`); credentials go to the OS keyring.

### Development tasks

| Command | What it does |
|---|---|
| `mise run build` | Build the `yatta` binary |
| `mise run run` | Run yatta without leaving a binary behind |
| `mise run test` | Run all tests |
| `mise run test-race` | Run all tests under the race detector (needs a C compiler) |
| `mise run test-tz` | Run all tests in several timezones |
| `mise run vet` | Run `go vet` |
| `mise run lint` | Run golangci-lint (config in `.golangci.yml`) |
| `mise run fmt` | Format the code |
| `mise run check` | Lint, vet, and all three test runs in one go; what CI runs |

## Design documents

The design lives in [`docs/`](docs/):

- [`YATTA.md`](docs/YATTA.md): product goals, principles, scope, and terminology.
- [`YATTA-ARCH.md`](docs/YATTA-ARCH.md): package layout, domain types, and UI structure.
- [`YATTA-DATA.md`](docs/YATTA-DATA.md): SQLite schema, constraints, and migrations.
