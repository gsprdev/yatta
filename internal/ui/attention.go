package ui

import (
	"fmt"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
)

type attentionKind int

const (
	attnAll attentionKind = iota
	attnFailed
	attnDeparted
	attnUnassigned
	attnKinds
)

var attentionTitles = [attnKinds]string{
	"Needs attention", "Failed uploads", "Entries on departed tasks", "Entries with no task",
}

type attentionModel struct {
	list  list.Model
	kind  attentionKind
	items [attnKinds][]list.Item
}

func newAttentionModel() attentionModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	l.Title = attentionTitles[attnAll]
	return attentionModel{list: l}
}

// classify reports which attention kind an entry falls under, if any. Every
// member of a failed aggregate unit carries the failure, so all appear.
func classify(e core.TimeEntry, d data) (attentionKind, bool) {
	if e.TaskID == "" {
		return attnUnassigned, true
	}
	if e.Upload == nil || e.Locked() {
		return 0, false
	}
	if t := d.taskByID[e.TaskID]; t.Remote != nil && t.Remote.Departed != nil {
		return attnDeparted, true
	}
	if e.Upload.Phase == core.Failed {
		return attnFailed, true
	}
	return 0, false
}

func (m *Model) fillAttention() {
	a := &m.attention
	for k := range a.items {
		a.items[k] = nil
	}
	for _, e := range m.data.entries {
		kind, ok := classify(e, m.data)
		if !ok {
			continue
		}
		it := newEntryItem(e, m.data, m.loc)
		a.items[kind] = append(a.items[kind], it)
		a.items[attnAll] = append(a.items[attnAll], it)
	}
	a.show()
}

func (a *attentionModel) show() {
	a.list.Title = fmt.Sprintf("%s (%d)", attentionTitles[a.kind], len(a.items[a.kind]))
	a.list.SetItems(a.items[a.kind])
}

func (m Model) updateAttention(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && !m.attention.list.SettingFilter() {
		switch key.String() {
		case "esc", "q":
			m.mode = modeEntries
			return m, nil
		case "tab":
			m.attention.kind = (m.attention.kind + 1) % attnKinds
			m.attention.show()
			return m, nil
		case "enter":
			it, ok := m.attention.list.SelectedItem().(entryItem)
			if !ok {
				return m, nil
			}
			m.entries.list.ResetFilter()
			m.entries.selectID(it.e.ID)
			m.mode = modeEntries
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.attention.list, cmd = m.attention.list.Update(msg)
	return m, cmd
}
