package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/store"
)

// entriesModel shows one local day of entries at a time, by start time. A
// search covers every day.
type entriesModel struct {
	list      list.Model
	confirm   string // entry ID awaiting discard confirmation
	showKeys  bool   // full key list in place of the entries
	all       []entryItem
	days      []time.Time // local midnights with entries, and today; newest first
	day       time.Time   // the day shown; zero until the first load
	searching bool        // the list holds every day's entries, for a search
	loc       *time.Location
}

const entryKeys = `Timer
  space / s   resume: start a timer on the selected entry's task
  n           start a new timer (choose a task, or none)
  T           set the running timer's task
  E           edit the running timer: backdate its start, add a note, set the task
  x           stop the timer

Entries
  a           add an entry by hand
  e / enter   edit the selected entry (uploaded entries open read-only)
  d           discard the selected entry (not once uploaded)
  ← / →       previous or next day (h / l also work)
  pgup / pgdn page within a long day
  /           search every day by task, ticket label, note, or date

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
	date  string // shown only in search results, where days are mixed
	desc  string
	find  string
	dated bool
}

func (i entryItem) Title() string { return i.title }
func (i entryItem) Description() string {
	if i.dated {
		return i.date + "  " + i.desc
	}
	return i.desc
}
func (i entryItem) FilterValue() string { return i.find }

func newEntriesModel() entriesModel {
	l := list.New(nil, list.NewDefaultDelegate(), 0, 0)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	// Left and right move between days; the list pages only on pgup/pgdn.
	l.KeyMap.PrevPage.SetKeys("pgup")
	l.KeyMap.NextPage.SetKeys("pgdown")
	return entriesModel{list: l}
}

// setEntries replaces the entries, keeping the shown day and selection. When
// the selected entry has moved to another day, the view follows it.
func (m *entriesModel) setEntries(d data, loc *time.Location, now time.Time) {
	selected := m.selectedID()
	m.loc = loc
	m.all = make([]entryItem, len(d.entries))
	today := dayOf(now, loc)
	m.days = []time.Time{today}
	for i, e := range d.entries {
		m.all[i] = newEntryItem(e, d, loc)
		day := dayOf(e.Start, loc)
		if !slices.ContainsFunc(m.days, day.Equal) {
			m.days = append(m.days, day)
		}
	}
	slices.SortFunc(m.days, func(a, b time.Time) int { return b.Compare(a) })
	if m.day.IsZero() {
		m.day = today
	}
	if e, ok := m.find(selected); ok && !m.searching {
		m.day = dayOf(e.Start, loc)
	}
	m.fill()
	m.selectID(selected)
}

// fill puts the shown day's entries, or every entry while searching, in the list.
func (m *entriesModel) fill() {
	var items []list.Item
	for _, it := range m.all {
		if m.searching || dayOf(it.e.Start, m.loc).Equal(m.day) {
			it.dated = m.searching
			items = append(items, it)
		}
	}
	// While a search is applied the list refilters in a command; run it now so
	// the selection that follows sees the results.
	if cmd := m.list.SetItems(items); cmd != nil {
		m.list, _ = m.list.Update(cmd())
	}
}

func (m entriesModel) find(id string) (core.TimeEntry, bool) {
	for _, it := range m.all {
		if id != "" && it.e.ID == id {
			return it.e, true
		}
	}
	return core.TimeEntry{}, false
}

// dayOf is the local midnight starting t's day.
func dayOf(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	return time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
}

// step moves to the next older (-1) or newer (+1) day that has entries, or
// today.
func (m *entriesModel) step(dir int) {
	next := m.day
	for _, d := range m.days { // newest first
		if dir < 0 && d.Before(m.day) {
			next = d
			break
		}
		if dir > 0 && d.After(m.day) {
			next = d
		}
	}
	if next.Equal(m.day) {
		return
	}
	m.day = next
	m.fill()
	m.list.ResetSelected()
}

// goTo ends any search and shows the entry's day with the entry selected.
func (m *entriesModel) goTo(id string) {
	m.list.ResetFilter()
	m.searching = false
	if e, ok := m.find(id); ok {
		m.day = dayOf(e.Start, m.loc)
	}
	m.fill()
	m.selectID(id)
}

// update passes msg to the list. A search starts on every day's entries, and
// once cleared returns to the day of the entry that was selected.
func (m *entriesModel) update(msg tea.Msg) tea.Cmd {
	if key, ok := msg.(tea.KeyMsg); ok && !m.searching && key.String() == "/" {
		m.searching = true
		m.fill()
	}
	selected := m.selectedID()
	var cmd tea.Cmd
	m.list, cmd = m.list.Update(msg)
	if m.searching && m.list.FilterState() == list.Unfiltered {
		m.goTo(selected)
	}
	return cmd
}

// title names the shown day, relative to today where that helps.
func (m entriesModel) title(now time.Time) string {
	if m.searching {
		return "All days"
	}
	title := m.day.Format("Mon 02 Jan 2006")
	switch today := dayOf(now, m.loc); {
	case m.day.Equal(today):
		title += " · today"
	case m.day.Equal(today.AddDate(0, 0, -1)):
		title += " · yesterday"
	}
	return title
}

func newEntryItem(e core.TimeEntry, d data, loc *time.Location) entryItem {
	task := d.taskByID[e.TaskID]
	title := taskTitle(task)
	if e.Note != "" {
		title += " — " + e.Note
	}
	start, end := e.Start.In(loc), e.End().In(loc)
	desc := fmt.Sprintf("%s–%s  %s", start.Format("15:04"), end.Format("15:04"), short(e.Duration))
	if state := entryState(e, task); state != "" {
		desc += "  " + state
	}
	find := strings.Join([]string{taskPath(task, d.taskByID), labelOf(task), e.Note, start.Format("2006-01-02 Mon")}, " ")
	return entryItem{e: e, title: title, date: start.Format("Mon 02 Jan"), desc: desc, find: find}
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
	for i, it := range m.list.VisibleItems() {
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
	return "space resume · n new timer · x stop · a add · e edit · ←/→ day · / search · u upload · ! attention · ? all keys"
}

func (m entriesModel) view(now time.Time) string {
	if m.showKeys {
		return entryKeys
	}
	m.list.Title = m.title(now)
	return m.list.View()
}

func (m Model) updateEntries(msg tea.Msg) (tea.Model, tea.Cmd) {
	key, isKey := msg.(tea.KeyMsg)
	if !isKey || m.entries.list.SettingFilter() {
		return m, m.entries.update(msg)
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
	case "E":
		if m.data.timer == nil {
			return m, flash("no timer running", true)
		}
		return m.openTimerEditor(*m.data.timer)
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
		m.settings = newSettings(m.data, m.secrets)
		m.mode = modeSettings
		return m, m.settings.init()
	case "f":
		return m, m.startFetch()
	case "left", "h":
		if !m.entries.searching {
			m.entries.step(-1)
		}
		return m, nil
	case "right", "l":
		if !m.entries.searching {
			m.entries.step(1)
		}
		return m, nil
	}
	return m, m.entries.update(msg)
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
