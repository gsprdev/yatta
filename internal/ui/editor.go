package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/store"
)

const (
	fieldDate = iota
	fieldStart
	fieldEnd
	fieldNote
	fieldTask
	fieldCount
)

type editorModel struct {
	entry  core.TimeEntry
	taskID string
	inputs [fieldTask]textinput.Model
	focus  int
	err    string
	loc    *time.Location
	record *core.RemoteRecord // loaded for a locked entry
}

type recordLoadedMsg struct {
	record core.RemoteRecord
	err    error
}

func newEditor(e core.TimeEntry, d data, loc *time.Location) editorModel {
	ed := editorModel{entry: e, taskID: e.TaskID, loc: loc}
	start, end := e.Start.In(loc), e.End().In(loc)
	values := [fieldTask]string{start.Format("2006-01-02"), start.Format("15:04"), end.Format("15:04"), e.Note}
	placeholders := [fieldTask]string{"YYYY-MM-DD", "HH:MM", "HH:MM or a length like 1h30m", "what was done"}
	for i := range ed.inputs {
		ti := textinput.New()
		ti.SetValue(values[i])
		ti.Placeholder = placeholders[i]
		ti.Prompt = ""
		ed.inputs[i] = ti
	}
	ed.setFocus(fieldDate)
	return ed
}

func (ed *editorModel) setFocus(f int) {
	ed.focus = (f + fieldCount) % fieldCount
	for i := range ed.inputs {
		if i == ed.focus && !ed.entry.Locked() {
			ed.inputs[i].Focus()
		} else {
			ed.inputs[i].Blur()
		}
	}
}

func (ed editorModel) help() string {
	if ed.entry.Locked() {
		return "uploaded entries are read-only · esc back"
	}
	return "tab/↑↓ move · enter on task: choose · ctrl+s save · esc cancel"
}

// parse builds the edited entry from the form.
func (ed editorModel) parse() (core.TimeEntry, error) {
	e := ed.entry
	v := func(i int) string { return strings.TrimSpace(ed.inputs[i].Value()) }
	day, err := time.ParseInLocation("2006-01-02", v(fieldDate), ed.loc)
	if err != nil {
		return e, fmt.Errorf("date: use YYYY-MM-DD")
	}
	start, err := clockOn(day, v(fieldStart), ed.loc)
	if err != nil {
		return e, fmt.Errorf("start: use HH:MM")
	}
	var end time.Time
	if d, err := time.ParseDuration(v(fieldEnd)); err == nil {
		end = start.Add(d)
	} else if end, err = clockOn(day, v(fieldEnd), ed.loc); err != nil {
		return e, fmt.Errorf("end: use HH:MM or a length like 1h30m")
	} else if !end.After(start) {
		end = end.AddDate(0, 0, 1) // ends after midnight
	}
	if end.Sub(start) < time.Second {
		return e, fmt.Errorf("the entry must be longer than zero")
	}
	e.Start = start.UTC()
	e.Duration = end.Sub(start).Truncate(time.Second)
	e.Note = v(fieldNote)
	e.TaskID = ed.taskID
	return e, nil
}

func clockOn(day time.Time, hhmm string, loc *time.Location) (time.Time, error) {
	c, err := time.Parse("15:04", hhmm)
	if err != nil {
		return time.Time{}, err
	}
	return time.Date(day.Year(), day.Month(), day.Day(), c.Hour(), c.Minute(), 0, 0, loc), nil
}

func (m Model) openEditor(e core.TimeEntry) (tea.Model, tea.Cmd) {
	m.editor = newEditor(e, m.data, m.loc)
	m.mode = modeEditor
	if !e.Locked() {
		return m, nil
	}
	st, id := m.st, e.Upload.RecordID
	return m, func() tea.Msg {
		r, err := st.Record(id)
		return recordLoadedMsg{r, err}
	}
}

func (m Model) updateEditor(msg tea.Msg) (tea.Model, tea.Cmd) {
	ed := &m.editor
	switch msg := msg.(type) {
	case recordLoadedMsg:
		if msg.err != nil {
			ed.err = msg.err.Error()
		} else {
			ed.record = &msg.record
		}
		return m, nil
	case tea.KeyMsg:
		switch msg.String() {
		case "esc":
			m.mode = modeEntries
			return m, nil
		}
		if ed.entry.Locked() {
			return m, nil
		}
		switch msg.String() {
		case "tab", "down":
			ed.setFocus(ed.focus + 1)
			return m, nil
		case "shift+tab", "up":
			ed.setFocus(ed.focus - 1)
			return m, nil
		case "ctrl+t":
			m.picker.open(pickEditor, m.data, "")
			m.mode = modePicker
			return m, nil
		case "enter":
			if ed.focus == fieldTask {
				m.picker.open(pickEditor, m.data, "")
				m.mode = modePicker
				return m, nil
			}
			ed.setFocus(ed.focus + 1)
			return m, nil
		case "ctrl+s":
			e, err := ed.parse()
			if err != nil {
				ed.err = err.Error()
				return m, nil
			}
			m.mode = modeEntries
			return m, m.mutate("entry saved", func(st *store.Store) error {
				_, err := st.SaveEntry(e)
				return err
			})
		}
	}
	if ed.focus < fieldTask && !ed.entry.Locked() {
		var cmd tea.Cmd
		ed.inputs[ed.focus], cmd = ed.inputs[ed.focus].Update(msg)
		return m, cmd
	}
	return m, nil
}

func (ed editorModel) view(d data) string {
	var b strings.Builder
	title := "Edit entry"
	switch {
	case ed.entry.ID == "":
		title = "New entry"
	case ed.entry.Locked():
		title = "Uploaded entry"
	}
	b.WriteString(boldStyle.Render(title) + "\n\n")
	labels := [fieldCount]string{"Date", "Start", "End", "Note", "Task"}
	for i := 0; i < fieldCount; i++ {
		marker := "  "
		if i == ed.focus && !ed.entry.Locked() {
			marker = "› "
		}
		var value string
		if i == fieldTask {
			task := d.taskByID[ed.taskID]
			value = taskTitle(task)
			if ed.taskID != "" {
				value = taskPath(task, d.taskByID)
				if l := labelOf(task); l != "" {
					value = l + "  " + value
				}
			}
		} else if ed.entry.Locked() {
			value = ed.inputs[i].Value()
		} else {
			value = ed.inputs[i].View()
		}
		fmt.Fprintf(&b, "%s%-6s %s\n", marker, labels[i], value)
	}
	if ed.entry.Locked() {
		b.WriteString("\n" + ed.mergedView(d))
	}
	if ed.err != "" {
		b.WriteString("\n" + errStyle.Render(ed.err) + "\n")
	}
	return b.String()
}

// mergedView shows what was sent for this entry: the record's values against
// the local ones, and every entry the record combined.
func (ed editorModel) mergedView(d data) string {
	r := ed.record
	if r == nil {
		return faintStyle.Render("loading upload record…")
	}
	var b strings.Builder
	if r.Excluded() {
		b.WriteString(warnStyle.Render("Excluded: below the minimum duration; nothing was sent.") + "\n")
	} else {
		fmt.Fprintf(&b, "Uploaded %s as %s (remote id %s)\n", r.CreatedAt.In(ed.loc).Format("2006-01-02 15:04"), short(r.Duration), r.RemoteID)
	}
	var members []core.TimeEntry
	var local time.Duration
	for _, e := range d.entries {
		if e.Upload != nil && e.Upload.RecordID == r.ID {
			members = append(members, e)
			local += e.Duration
		}
	}
	fmt.Fprintf(&b, "Local time %s across %d %s", short(local), len(members), plural(len(members), "entry", "entries"))
	if r.Note != "" {
		fmt.Fprintf(&b, "\nRemote note: %s", r.Note)
	}
	if len(members) > 1 {
		for _, e := range members {
			start := e.Start.In(ed.loc)
			fmt.Fprintf(&b, "\n  %s %s  %s  %s", start.Format("01-02"), start.Format("15:04"), short(e.Duration), e.Note)
		}
	}
	return b.String()
}

func plural(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}
