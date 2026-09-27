package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/store"
)

// tasksModel manages the local task hierarchy. Remote tasks are not listed:
// they come from the remote system.
type tasksModel struct {
	list   list.Model
	input  textinput.Model
	action string // "add", "child", "rename" while the name input is open
}

func newTasksModel() tasksModel {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	l := list.New(nil, d, 0, 0)
	l.Title = "Local tasks"
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	ti := textinput.New()
	ti.Prompt = "Name: "
	return tasksModel{list: l, input: ti}
}

func (m *tasksModel) setTasks(tasks []core.Task) {
	sel := m.selected().ID
	byID := map[string]core.Task{}
	for _, t := range tasks {
		byID[t.ID] = t
	}
	var items []list.Item
	for _, ft := range flattenTasks(tasks) {
		t := ft.task
		if t.Remote != nil {
			continue
		}
		title := strings.Repeat("  ", ft.depth) + t.Name
		if t.Archived != nil {
			title += "  (archived)"
		}
		items = append(items, taskItem{task: t, title: title, find: taskPath(t, byID)})
	}
	m.list.SetItems(items)
	for i, it := range items {
		if it.(taskItem).task.ID == sel {
			m.list.Select(i)
		}
	}
}

func (m tasksModel) selected() core.Task {
	it, _ := m.list.SelectedItem().(taskItem)
	return it.task
}

func (m tasksModel) help() string {
	if m.action != "" {
		return "enter save · esc cancel"
	}
	return "a add · c add child · r rename · m move · x archive/restore · / search · esc back"
}

func (m tasksModel) view() string {
	if m.action != "" {
		return m.list.View() + "\n" + m.input.View()
	}
	return m.list.View()
}

func (m Model) updateTasks(msg tea.Msg) (tea.Model, tea.Cmd) {
	tm := &m.tasks
	key, isKey := msg.(tea.KeyMsg)
	if tm.action != "" {
		if isKey {
			switch key.String() {
			case "esc":
				tm.action = ""
				return m, nil
			case "enter":
				name := strings.TrimSpace(tm.input.Value())
				action, sel := tm.action, tm.selected()
				tm.action = ""
				if name == "" {
					return m, nil
				}
				var t core.Task
				switch action {
				case "add":
					t = core.Task{Name: name, ParentID: sel.ParentID}
				case "child":
					t = core.Task{Name: name, ParentID: sel.ID}
				case "rename":
					t = sel
					t.Name = name
				}
				return m, m.mutate("task saved", func(st *store.Store) error {
					_, err := st.SaveLocalTask(t)
					return err
				})
			}
		}
		var cmd tea.Cmd
		tm.input, cmd = tm.input.Update(msg)
		return m, cmd
	}
	if isKey && !tm.list.SettingFilter() {
		sel := tm.selected()
		switch key.String() {
		case "esc", "q":
			m.mode = modeEntries
			return m, nil
		case "a", "c", "r":
			if key.String() != "a" && sel.ID == "" {
				return m, nil
			}
			tm.action = map[string]string{"a": "add", "c": "child", "r": "rename"}[key.String()]
			tm.input.SetValue("")
			if tm.action == "rename" {
				tm.input.SetValue(sel.Name)
			}
			tm.input.Focus()
			return m, textinput.Blink
		case "m":
			if sel.ID == "" {
				return m, nil
			}
			m.picker.open(pickMove, m.data, sel.ID)
			m.mode = modePicker
			return m, nil
		case "x":
			if sel.ID == "" {
				return m, nil
			}
			flashText := "task archived"
			if sel.Archived != nil {
				sel.Archived, flashText = nil, "task restored"
			} else {
				now := m.now()
				sel.Archived = &now
			}
			return m, m.mutate(flashText, func(st *store.Store) error {
				_, err := st.SaveLocalTask(sel)
				return err
			})
		}
	}
	var cmd tea.Cmd
	tm.list, cmd = tm.list.Update(msg)
	return m, cmd
}
