# yatta

A user-focused time tracking client

# Why "yatta"?

yatta is may have been named for the Japanese やった ("I did it!") an exclamation of excitement and accomplishment.
This name reflects the joy and satisfaction we all feel when submitting our timesheets.
Alternatively, it might stand for "Yet Another Time Tracking Application".
We may never know which for certain.

# Building and running

yatta is a single Go binary with no runtime dependencies:

```sh
CGO_ENABLED=0 go build ./cmd/yatta
./yatta            # data lives in your user data directory
./yatta --db x.db  # or somewhere else
```

Press `?` for keys. Remote integrations (Jira, Redmine, Toggl) are set up under settings (`,`); credentials go to the OS keyring.

Design documents are in [`docs/`](docs/).
