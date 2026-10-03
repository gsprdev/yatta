// Package ui is YATTA's Bubble Tea interface.
package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote"
	"github.com/gsprdev/yatta/internal/store"
)

type mode int

const (
	modeEntries   mode = iota // resting view
	modePicker                // choosing a task
	modeEditor                // creating or correcting an entry; read-only when locked
	modeUpload                // confirm scope, then blocking progress
	modeAttention             // failed uploads, departed tasks, unassigned entries
	modeTasks                 // local task management
	modeSettings              // policy, integration setup, purge
)

// Connector builds the adapter for the configured integration. It returns a
// nil adapter when no integration is configured.
type Connector func(*store.IntegrationConfig) (remote.Adapter, error)

// Model is the root model. It owns the shared data and the sub-models, and
// renders the status bar on every frame.
type Model struct {
	mode          mode
	width, height int

	st      *store.Store
	ad      remote.Adapter
	who     string // the verified account and API of ad
	connErr error  // why ad could not be built or verified
	connect Connector
	secrets Secrets
	loc     *time.Location
	now     func() time.Time

	entries   entriesModel
	picker    pickerModel
	editor    editorModel
	upload    uploadModel
	attention attentionModel
	tasks     tasksModel
	settings  settingsModel

	data     data
	flash    string
	isErr    bool
	fetching bool // a remote fetch is running
}

// data is everything loaded from the store, replaced wholesale on reload.
type data struct {
	entries  []core.TimeEntry
	tasks    []core.Task
	taskByID map[string]core.Task
	timer    *core.ActiveTimer
	counts   store.Counts
	policy   core.Policy
	config   *store.IntegrationConfig
}

func New(st *store.Store, connect Connector, secrets Secrets, loc *time.Location) Model {
	m := Model{st: st, connect: connect, secrets: secrets, loc: loc, now: time.Now}
	m.entries = newEntriesModel()
	m.picker = newPickerModel()
	m.attention = newAttentionModel()
	m.tasks = newTasksModel()
	return m
}

type loadedMsg struct {
	data  data
	err   error
	flash string // shown on success
}

type tickMsg time.Time

type flashMsg struct {
	text  string
	isErr bool
}

func tick() tea.Cmd {
	return tea.Every(time.Second, func(t time.Time) tea.Msg { return tickMsg(t) })
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(load(m.st), tick(), m.connectCmd(true))
}

// load reads everything the interface shows.
func load(st *store.Store) tea.Cmd {
	return func() tea.Msg {
		var d data
		var err error
		if d.entries, err = st.Entries(time.Time{}, time.Time{}); err != nil {
			return loadedMsg{err: err}
		}
		if d.tasks, err = st.Tasks(); err != nil {
			return loadedMsg{err: err}
		}
		d.taskByID = make(map[string]core.Task, len(d.tasks))
		for _, t := range d.tasks {
			d.taskByID[t.ID] = t
		}
		if d.timer, err = st.Timer(); err != nil {
			return loadedMsg{err: err}
		}
		if d.counts, err = st.Counts(); err != nil {
			return loadedMsg{err: err}
		}
		if d.policy, err = st.Policy(); err != nil {
			return loadedMsg{err: err}
		}
		if d.config, err = st.Integration(); err != nil {
			return loadedMsg{err: err}
		}
		return loadedMsg{data: d}
	}
}

// mutate runs fn against the store, then reloads. flash is shown on success.
func (m Model) mutate(flash string, fn func(*store.Store) error) tea.Cmd {
	st := m.st
	return func() tea.Msg {
		if err := fn(st); err != nil {
			return flashMsg{text: err.Error(), isErr: true}
		}
		msg := load(st)().(loadedMsg)
		msg.flash = flash
		return msg
	}
}

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.resize()
		return m, nil
	case tickMsg:
		return m, tick()
	case loadedMsg:
		if msg.err != nil {
			m.flash, m.isErr = msg.err.Error(), true
			return m, nil
		}
		m.data = msg.data
		m.refresh()
		if msg.flash != "" {
			m.flash, m.isErr = msg.flash, false
		}
		return m, nil
	case flashMsg:
		m.flash, m.isErr = msg.text, msg.isErr
		return m, nil
	case connectedMsg:
		return m.onConnected(msg)
	case fetchedMsg:
		return m.onFetched(msg)
	case pickedMsg:
		return m.onPicked(msg)
	case tea.KeyMsg:
		if msg.String() == "ctrl+c" && m.mode != modeUpload {
			return m, tea.Quit
		}
		if m.mode != modeUpload && m.flash != "" {
			m.flash = ""
		}
	}

	switch m.mode {
	case modePicker:
		return m.updatePicker(msg)
	case modeEditor:
		return m.updateEditor(msg)
	case modeUpload:
		return m.updateUpload(msg)
	case modeAttention:
		return m.updateAttention(msg)
	case modeTasks:
		return m.updateTasks(msg)
	case modeSettings:
		return m.updateSettings(msg)
	default:
		return m.updateEntries(msg)
	}
}

// refresh pushes freshly loaded data into the sub-models.
func (m *Model) refresh() {
	m.entries.setEntries(m.data, m.loc, m.now())
	m.fillAttention()
	m.tasks.setTasks(m.data.tasks)
}

func (m *Model) resize() {
	h := m.height - 2 // status bar and help line
	if h < 3 {
		h = 3
	}
	m.entries.list.SetSize(m.width, h)
	m.picker.list.SetSize(m.width, h)
	m.attention.list.SetSize(m.width, h)
	m.tasks.list.SetSize(m.width, h)
}

var (
	statusStyle = lipgloss.NewStyle().Reverse(true)
	helpStyle   = lipgloss.NewStyle().Faint(true)
	errStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("9")).Bold(true)
	okStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("10"))
	warnStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("11"))
	faintStyle  = lipgloss.NewStyle().Faint(true)
	boldStyle   = lipgloss.NewStyle().Bold(true)
)

func (m Model) View() string {
	var body, help string
	switch m.mode {
	case modePicker:
		body, help = m.picker.list.View(), "enter choose · / filter · esc cancel"
	case modeEditor:
		body, help = m.editor.view(m.data), m.editor.help()
	case modeUpload:
		body, help = m.upload.view(m.data, m.loc), m.upload.help()
	case modeAttention:
		body, help = m.attention.list.View(), "enter go to entry · tab next kind · esc back"
	case modeTasks:
		body, help = m.tasks.view(), m.tasks.help()
	case modeSettings:
		body, help = m.settings.view(m.connection()), m.settings.help()
	default:
		body, help = m.entries.view(m.now()), m.entries.help()
	}
	if m.flash != "" {
		if m.isErr {
			help = errStyle.Render(m.flash)
		} else {
			help = okStyle.Render(m.flash)
		}
	}
	return lipgloss.JoinVertical(lipgloss.Left, m.statusBar(), body, helpStyle.Render(help))
}

// statusBar is the always-visible surface: the running timer, today's total,
// and the attention counts.
func (m Model) statusBar() string {
	now := m.now()
	timer := "○ no timer"
	if t := m.data.timer; t != nil {
		task := "no task"
		if t.TaskID != "" {
			task = taskTitle(m.data.taskByID[t.TaskID])
		}
		timer = fmt.Sprintf("● %s  %s", clock(now.Sub(t.Start)), task)
		if t.Note != "" {
			timer += "  — " + truncate(t.Note, 30)
		}
	}
	today := core.DayTotal(m.data.entries, m.data.timer, now, m.loc)
	c := m.data.counts
	var attn []string
	for _, a := range []struct {
		n    int
		name string
	}{{c.Pending, "pending"}, {c.Failed, "failed"}, {c.Departed, "departed"}, {c.Unassigned, "unassigned"}} {
		if a.n > 0 {
			attn = append(attn, fmt.Sprintf("%d %s", a.n, a.name))
		}
	}
	right := "today " + short(today)
	if len(attn) > 0 {
		right += "  │  " + strings.Join(attn, " · ")
	}
	if m.fetching {
		right += "  │  fetching tasks…"
	}
	gap := m.width - lipgloss.Width(timer) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return statusStyle.Width(max(m.width, 1)).Render(" " + timer + strings.Repeat(" ", gap) + right + " ")
}

// truncate shortens s to at most n runes, marking the cut.
func truncate(s string, n int) string {
	if r := []rune(s); len(r) > n {
		return string(r[:n-1]) + "…"
	}
	return s
}

// clock formats a running duration as h:mm:ss.
func clock(d time.Duration) string {
	if d < 0 {
		d = 0
	}
	s := int(d / time.Second)
	return fmt.Sprintf("%d:%02d:%02d", s/3600, s/60%60, s%60)
}

// short formats a duration as "1h05m" or "25m".
func short(d time.Duration) string {
	mins := int(d / time.Minute)
	if mins < 60 {
		return fmt.Sprintf("%dm", mins)
	}
	return fmt.Sprintf("%dh%02dm", mins/60, mins%60)
}

// taskTitle is how a task is named in one line: its label, then its name.
func taskTitle(t core.Task) string {
	if t.ID == "" {
		return "no task"
	}
	if t.Remote != nil && t.Remote.Label != "" {
		return t.Remote.Label + " " + t.Name
	}
	return t.Name
}

// taskPath is the task's ancestry, root first, joined for display and search.
func taskPath(t core.Task, byID map[string]core.Task) string {
	parts := []string{t.Name}
	seen := map[string]bool{t.ID: true}
	for p := t.ParentID; p != "" && !seen[p]; {
		seen[p] = true
		parent, ok := byID[p]
		if !ok {
			break
		}
		parts = append([]string{parent.Name}, parts...)
		p = parent.ParentID
	}
	return strings.Join(parts, " › ")
}
