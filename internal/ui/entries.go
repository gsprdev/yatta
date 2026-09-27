package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/store"
)

type entriesModel struct {
	list     list.Model
	confirm  string // entry ID awaiting discard confirmation
	showKeys bool   // full key list in place of the entries
}

const entryKeys = `Timer
  space / s   resume: start a timer on the selected entry's task
  n           start a new timer (choose a task, or none)
  T           set the running timer's task
  x           stop the timer

Entries
  a           add an entry by hand
  e / enter   edit the selected entry (uploaded entries open read-only)
  d           discard the selected entry (not once uploaded)
  /           search by task, ticket label, note, or date

Elsewhere
  u           upload pending entries
  !           attention: failed, departed, unassigned
  t           local tasks
  ,           settings: upload policy, integration, purge
  f           fetch remote tasks now
  q           quit (a running timer keeps running)

? or esc to close`

type entryItem struct {
	e     core.TimeEntry
	title string
	desc  string
	find  string
}

func (i entryItem) Title() string       { return i.title }
func (i entryItem) Description() string { return i.desc }
func (i entryItem) FilterValue() string { return i.find }

func newEntriesModel() entriesModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.Title = "Entries"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	return entriesModel{list: l}
}

func (m *entriesModel) setEntries(d data, loc *time.Location) {
	selected := m.selectedID()
	items := make([]list.Item, len(d.entries))
	for i, e := range d.entries {
		items[i] = newEntryItem(e, d, loc)
	}
	m.list.SetItems(items)
	m.selectID(selected)
}

func newEntryItem(e core.TimeEntry, d data, loc *time.Location) entryItem {
	task := d.taskByID[e.TaskID]
	title := taskTitle(task)
	if e.Note != "" {
		title += " — " + e.Note
	}
	start, end := e.Start.In(loc), e.End().In(loc)
	desc := fmt.Sprintf("%s  %s–%s  %s", start.Format("Mon 02 Jan"), start.Format("15:04"), end.Format("15:04"), short(e.Duration))
	if state := entryState(e, task); state != "" {
		desc += "  " + state
	}
	find := strings.Join([]string{taskPath(task, d.taskByID), labelOf(task), e.Note, start.Format("2006-01-02 Mon")}, " ")
	return entryItem{e: e, title: title, desc: desc, find: find}
}

func labelOf(t core.Task) string {
	if t.Remote != nil {
		return t.Remote.Label
	}
	return ""
}

// entryState is the short marker shown after an entry's times.
func entryState(e core.TimeEntry, task core.Task) string {
	if e.TaskID == "" {
		return "○ unassigned"
	}
	if e.Upload == nil {
		return ""
	}
	departed := task.Remote != nil && task.Remote.Departed != nil
	switch e.Upload.Phase {
	case core.Pending:
		if departed {
			return "⚠ task departed"
		}
		return "◌ pending"
	case core.Failed:
		return "✗ failed: " + e.Upload.Err
	case core.Uploaded:
		return "✓ uploaded"
	case core.Excluded:
		return "– excluded (below minimum)"
	}
	return ""
}

func (m entriesModel) selected() (core.TimeEntry, bool) {
	it, ok := m.list.SelectedItem().(entryItem)
	return it.e, ok
}

func (m entriesModel) selectedID() string {
	e, _ := m.selected()
	return e.ID
}

func (m *entriesModel) selectID(id string) {
	if id == "" {
		return
	}
	for i, it := range m.list.Items() {
		if it.(entryItem).e.ID == id {
			m.list.Select(i)
			return
		}
	}
}

func (m entriesModel) help() string {
	if m.confirm != "" {
		return "discard this entry? y to confirm, any other key to cancel"
	}
	return "space resume · n new timer · x stop · a add · e edit · / search · u upload · ! attention · ? all keys"
}

func (m entriesModel) view() string {
	if m.showKeys {
		return entryKeys
	}
	return m.list.View()
}

func (m Model) updateEntries(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey || m.entries.list.SettingFilter() {
		var cmd tea.Cmd
		m.entries.list, cmd = m.entries.list.Update(msg)
		return m, cmd
	}
	if id := m.entries.confirm; id != "" {
		m.entries.confirm = ""
		if key.String() == "y" {
			return m, m.mutate("entry discarded", func(st *store.Store) error { return st.DiscardEntry(id) })
		}
		return m, nil
	}

	if m.entries.showKeys {
		m.entries.showKeys = false
		return m, nil
	}
	e, hasEntry := m.entries.selected()
	now := m.now()
	switch key.String() {
	case "q":
		return m, tea.Quit
	case "?":
		m.entries.showKeys = true
		return m, nil
	case " ", "s": // resume the selected entry's task
		if !hasEntry {
			return m, nil
		}
		return m, m.startTimer(e.TaskID, now)
	case "n":
		m.picker.open(pickTimer, m.data, "")
		m.mode = modePicker
		return m, nil
	case "x":
		if m.data.timer == nil {
			return m, nil
		}
		return m, m.mutate("timer stopped", func(st *store.Store) error {
			_, err := st.StopTimer(now)
			return err
		})
	case "T":
		if m.data.timer == nil {
			return m, nil
		}
		m.picker.open(pickTimerTask, m.data, "")
		m.mode = modePicker
		return m, nil
	case "a":
		return m.openEditor(core.TimeEntry{Start: now.Truncate(time.Minute).Add(-time.Hour), Duration: time.Hour})
	case "e", "enter":
		if !hasEntry {
			return m, nil
		}
		return m.openEditor(e)
	case "d":
		if !hasEntry {
			return m, nil
		}
		if e.Locked() {
			return m, flash("uploaded entries cannot be discarded", true)
		}
		m.entries.confirm = e.ID
		return m, nil
	case "u":
		return m.openUpload()
	case "!":
		m.mode = modeAttention
		return m, nil
	case "t":
		m.mode = modeTasks
		return m, nil
	case ",":
		m.settings = newSettings(m.data)
		m.mode = modeSettings
		return m, m.settings.init()
	case "f":
		return m, m.startFetch()
	}
	var cmd tea.Cmd
	m.entries.list, cmd = m.entries.list.Update(msg)
	return m, cmd
}

func (m Model) startTimer(taskID string, now time.Time) tea.Cmd {
	msg := "timer started"
	if taskID != "" {
		msg += ": " + taskTitle(m.data.taskByID[taskID])
	}
	return m.mutate(msg, func(st *store.Store) error {
		_, err := st.StartTimer(taskID, now)
		return err
	})
}

func flash(text string, isErr bool) tea.Cmd {
	return func() tea.Msg { return flashMsg{text: text, isErr: isErr} }
}
