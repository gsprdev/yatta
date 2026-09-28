package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/remote"
)

const (
	fetchTimeout  = 2 * time.Minute
	verifyTimeout = 30 * time.Second
)

type connectedMsg struct {
	ad    remote.Adapter
	who   string // the verified account and API, from Adapter.Verify
	err   error
	fetch bool // fetch remote tasks once connected
}

type fetchedMsg struct {
	n   int
	err error
}

// connectCmd builds the adapter for the configured integration and verifies
// its credentials, so a wrong account or mode shows before any fetch.
func (m Model) connectCmd(fetch bool) tea.Cmd {
	st, connect := m.st, m.connect
	return func() tea.Msg {
		cfg, err := st.Integration()
		if err != nil || cfg == nil || connect == nil {
			return connectedMsg{err: err}
		}
		ad, err := connect(cfg)
		if err != nil || ad == nil {
			return connectedMsg{err: err}
		}
		ctx, cancel := context.WithTimeout(context.Background(), verifyTimeout)
		defer cancel()
		who, err := ad.Verify(ctx)
		if err != nil {
			// Keep the adapter: the remote may only be briefly unreachable.
			return connectedMsg{ad: ad, err: fmt.Errorf("could not verify the credentials: %w", err)}
		}
		return connectedMsg{ad: ad, who: who, fetch: fetch}
	}
}

func (m Model) onConnected(msg connectedMsg) (tea.Model, tea.Cmd) {
	m.ad, m.who, m.connErr = msg.ad, msg.who, msg.err
	if msg.err != nil {
		m.flash, m.isErr = "remote integration: "+msg.err.Error(), true
		return m, nil
	}
	if msg.who != "" {
		m.flash, m.isErr = "connected: "+msg.who, false
	}
	if msg.fetch && m.ad != nil {
		return m, m.startFetch()
	}
	return m, nil
}

// startFetch fetches the remote task hierarchy in the background and
// reconciles it into the store. The interface stays usable meanwhile.
func (m *Model) startFetch() tea.Cmd {
	if m.ad == nil {
		return flash("no remote integration is configured (settings: ,)", true)
	}
	if m.fetching {
		return nil
	}
	m.fetching = true
	st, ad, now := m.st, m.ad, m.now
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), fetchTimeout)
		defer cancel()
		tasks, err := ad.FetchTasks(ctx)
		if err != nil {
			return fetchedMsg{err: err}
		}
		if err := st.ReconcileRemoteTasks(ad.Integration(), tasks); err != nil {
			return fetchedMsg{err: err}
		}
		return fetchedMsg{n: len(tasks), err: st.MarkFetched(now())}
	}
}

func (m Model) onFetched(msg fetchedMsg) (tea.Model, tea.Cmd) {
	m.fetching = false
	if msg.err != nil {
		m.flash, m.isErr = "fetching remote tasks: "+msg.err.Error(), true
		return m, nil
	}
	st := m.st
	flash := fmt.Sprintf("fetched %d remote tasks", msg.n)
	if m.who != "" {
		flash += " · " + m.who
	}
	return m, func() tea.Msg {
		l := load(st)().(loadedMsg)
		l.flash = flash
		return l
	}
}
