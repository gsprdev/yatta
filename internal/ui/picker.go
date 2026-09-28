package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/store"
)

// pickPurpose says what the chosen task is for, and where to return.
type pickPurpose int

const (
	pickTimer     pickPurpose = iota // start a new timer
	pickTimerTask                    // assign the running timer's task
	pickEditor                       // the task field of the entry editor
	pickMove                         // new parent for a local task (subject)
)

type pickerModel struct {
	list    list.Model
	purpose pickPurpose
	subject string // task being moved, for pickMove
}

type pickedMsg struct {
	purpose pickPurpose
	taskID  string // "" => no task, or top level for pickMove
	subject string
}

type taskItem struct {
	task  core.Task
	title string
	find  string
}

func (i taskItem) Title() string       { return i.title }
func (i taskItem) Description() string { return "" }
func (i taskItem) FilterValue() string { return i.find }

func newPickerModel() pickerModel {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	l := list.New(nil, d, 0, 0)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	return pickerModel{list: l}
}

// open fills the picker for a purpose. Typing any character starts the
// search, since typing is the fastest way to a task.
func (p *pickerModel) open(purpose pickPurpose, d data, subject string) {
	p.purpose, p.subject = purpose, subject
	var items []list.Item
	switch purpose {
	case pickTimer, pickEditor:
		p.list.Title = "Choose a task"
		items = append(items, taskItem{title: "(no task — choose later)", find: "no task"})
	case pickTimerTask:
		p.list.Title = "Task for the running timer"
	case pickMove:
		p.list.Title = "Move under"
		items = append(items, taskItem{title: "(top level)", find: "top level root"})
	}
	exclude := map[string]bool{}
	if purpose == pickMove && subject != "" {
		exclude = subtree(subject, d.tasks)
	}
	for _, ft := range flattenTasks(d.tasks) {
		t := ft.task
		switch {
		case exclude[t.ID]:
			continue
		case purpose == pickMove && (t.Remote != nil || t.Archived != nil):
			continue
		case purpose != pickMove && !t.Selectable():
			continue
		}
		items = append(items, taskItem{
			task:  t,
			title: strings.Repeat("  ", ft.depth) + taskTitle(t),
			find:  labelOf(t) + " " + taskPath(t, d.taskByID),
		})
	}
	p.list.ResetFilter()
	p.list.SetItems(items)
	p.list.Select(0)
}

type flatTask struct {
	task  core.Task
	depth int
}

// flattenTasks orders tasks depth-first, local hierarchy first, keeping each
// parent's children in their stored order. Tasks whose parent is missing are
// treated as roots.
func flattenTasks(tasks []core.Task) []flatTask {
	byID := map[string]bool{}
	for _, t := range tasks {
		byID[t.ID] = true
	}
	children := map[string][]core.Task{}
	var roots []core.Task
	for _, t := range tasks {
		if t.ParentID == "" || !byID[t.ParentID] {
			roots = append(roots, t)
		} else {
			children[t.ParentID] = append(children[t.ParentID], t)
		}
	}
	var local, remote []core.Task
	for _, r := range roots {
		if r.Remote == nil {
			local = append(local, r)
		} else {
			remote = append(remote, r)
		}
	}
	var out []flatTask
	var walk func(t core.Task, depth int)
	walk = func(t core.Task, depth int) {
		out = append(out, flatTask{t, depth})
		for _, c := range children[t.ID] {
			walk(c, depth+1)
		}
	}
	for _, r := range append(local, remote...) {
		walk(r, 0)
	}
	return out
}

// subtree returns the IDs of a task and all its descendants.
func subtree(id string, tasks []core.Task) map[string]bool {
	out := map[string]bool{id: true}
	for changed := true; changed; {
		changed = false
		for _, t := range tasks {
			if !out[t.ID] && out[t.ParentID] {
				out[t.ID], changed = true, true
			}
		}
	}
	return out
}

func (m Model) updatePicker(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Enter while typing a search accepts it and chooses the top match.
	if key, ok := msg.(tea.KeyMsg); ok && key.String() == "enter" && m.picker.list.SettingFilter() {
		m.picker.list, _ = m.picker.list.Update(msg)
	}
	if key, ok := msg.(tea.KeyMsg); ok && !m.picker.list.SettingFilter() {
		switch key.String() {
		case "esc":
			if m.picker.list.IsFiltered() {
				m.picker.list.ResetFilter()
				return m, nil
			}
			m.mode = m.pickerReturn()
			return m, nil
		case "enter":
			it, ok := m.picker.list.SelectedItem().(taskItem)
			if !ok {
				return m, nil
			}
			p := m.picker
			return m, func() tea.Msg { return pickedMsg{purpose: p.purpose, taskID: it.task.ID, subject: p.subject} }
		}
		if key.Type == tea.KeyRunes && key.String() != "/" && !m.picker.list.IsFiltered() {
			m.picker.list, _ = m.picker.list.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'/'}})
		}
	}
	var cmd tea.Cmd
	m.picker.list, cmd = m.picker.list.Update(msg)
	return m, cmd
}

func (m Model) pickerReturn() mode {
	switch m.picker.purpose {
	case pickEditor:
		return modeEditor
	case pickMove:
		return modeTasks
	}
	return modeEntries
}

func (m Model) onPicked(msg pickedMsg) (tea.Model, tea.Cmd) {
	m.mode = m.pickerReturn()
	switch msg.purpose {
	case pickTimer:
		return m, m.startTimer(msg.taskID, m.now())
	case pickTimerTask:
		id := msg.taskID
		return m, m.mutate("timer task set", func(st *store.Store) error { return st.SetTimerTask(id) })
	case pickEditor:
		m.editor.taskID = msg.taskID
		return m, nil
	case pickMove:
		t, ok := m.data.taskByID[msg.subject]
		if !ok {
			return m, nil
		}
		t.ParentID = msg.taskID
		return m, m.mutate("task moved", func(st *store.Store) error {
			_, err := st.SaveLocalTask(t)
			return err
		})
	}
	return m, nil
}
