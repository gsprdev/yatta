package ui

import (
	"context"
	"fmt"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/remote"
)

const fetchTimeout = 2 * time.Minute

type connectedMsg struct {
	ad    remote.Adapter
	err   error
	fetch bool // fetch remote tasks once connected
}

type fetchedMsg struct {
	n   int
	err error
}

// connectCmd builds the adapter for the configured integration.
func (m Model) connectCmd(fetch bool) tea.Cmd {
	st, connect := m.st, m.connect
	return func() tea.Msg {
		cfg, err := st.Integration()
		if err != nil || cfg == nil || connect == nil {
			return connectedMsg{err: err}
		}
		ad, err := connect(cfg)
		return connectedMsg{ad: ad, err: err, fetch: fetch}
	}
}

func (m Model) onConnected(msg connectedMsg) (tea.Model, tea.Cmd) {
	m.ad = msg.ad
	if msg.err != nil {
		m.flash, m.isErr = "remote integration: "+msg.err.Error(), true
		return m, nil
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
	n := msg.n
	return m, func() tea.Msg {
		l := load(st)().(loadedMsg)
		l.flash = fmt.Sprintf("fetched %d remote tasks", n)
		return l
	}
}
