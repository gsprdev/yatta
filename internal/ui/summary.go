package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
)

// zoom is the period the summary totals over.
type zoom int

const (
	zoomDay zoom = iota
	zoomWeek
	zoomMonth
	zoomAll
	zooms
)

// summaryModel shows the time recorded in one period, per task, each task
// totaling its own time and its descendants'. It is review: nothing leaves
// the screen.
type summaryModel struct {
	list   list.Model
	zoom   zoom
	anchor time.Time // a local midnight in the shown period
	title  string
}

func newSummaryModel() summaryModel {
	d := list.NewDefaultDelegate()
	d.ShowDescription = false
	d.SetSpacing(0)
	l := list.New(nil, d, 0, 0)
	l.SetShowHelp(false)
	l.SetShowStatusBar(false)
	l.DisableQuitKeybindings()
	return summaryModel{list: l, zoom: zoomWeek}
}

// period is the local [from, to) of the zoom containing anchor. Weeks start
// on Monday. zoomAll is unbounded: both are zero.
func period(anchor time.Time, z zoom, loc *time.Location) (from, to time.Time) {
	day := dayOf(anchor, loc)
	switch z {
	case zoomDay:
		return day, day.AddDate(0, 0, 1)
	case zoomWeek:
		from = day.AddDate(0, 0, -(int(day.Weekday())+6)%7)
		return from, from.AddDate(0, 0, 7)
	case zoomMonth:
		from = time.Date(day.Year(), day.Month(), 1, 0, 0, 0, 0, loc)
		return from, from.AddDate(0, 1, 0)
	case zoomAll, zooms:
	}
	return time.Time{}, time.Time{}
}

// fill totals the shown period and lays it out as the task tree, omitting
// tasks with no time in it.
func (s *summaryModel) fill(d data, loc *time.Location, now time.Time) {
	if s.anchor.IsZero() {
		s.anchor = dayOf(now, loc)
	}
	from, to := period(s.anchor, s.zoom, loc)
	own := core.TaskTotals(d.entries, d.timer, from, to, now)
	rolled := map[string]time.Duration{}
	var total time.Duration
	for id, dur := range own {
		total += dur
		if id == "" {
			continue
		}
		seen := map[string]bool{}
		for p := id; p != "" && !seen[p]; p = d.taskByID[p].ParentID {
			seen[p] = true
			rolled[p] += dur
		}
	}
	var items []list.Item
	for _, ft := range flattenTasks(d.tasks) {
		t := ft.task
		if rolled[t.ID] == 0 {
			continue
		}
		items = append(items, taskItem{
			task:  t,
			title: fmt.Sprintf("%7s  %s%s", short(rolled[t.ID]), strings.Repeat("  ", ft.depth), taskTitle(t)),
			find:  labelOf(t) + " " + taskPath(t, d.taskByID),
		})
	}
	if own[""] > 0 {
		items = append(items, taskItem{title: fmt.Sprintf("%7s  (no task)", short(own[""])), find: "no task"})
	}

	switch s.zoom {
	case zoomDay:
		s.title = from.Format("Mon 02 Jan 2006")
	case zoomWeek:
		s.title = "Week of " + from.Format("Mon 02 Jan 2006")
	case zoomMonth:
		s.title = from.Format("January 2006")
	case zoomAll, zooms:
		s.title = "All recorded time"
	}
	s.title += " · total " + short(total)
	if len(items) == 0 {
		s.title += " · nothing recorded"
	}
	if cmd := s.list.SetItems(items); cmd != nil {
		s.list, _ = s.list.Update(cmd())
	}
}

// step moves the anchor one period earlier (-1) or later (+1).
func (s *summaryModel) step(dir int) {
	switch s.zoom {
	case zoomDay:
		s.anchor = s.anchor.AddDate(0, 0, dir)
	case zoomWeek:
		s.anchor = s.anchor.AddDate(0, 0, 7*dir)
	case zoomMonth:
		// From the 1st, so a 31st never skips a short month.
		y, mo, _ := s.anchor.Date()
		s.anchor = time.Date(y, mo+time.Month(dir), 1, 0, 0, 0, 0, s.anchor.Location())
	case zoomAll, zooms:
	}
	s.list.ResetSelected()
}

func (s summaryModel) view() string {
	s.list.Title = s.title
	return s.list.View()
}

func (m Model) updateSummary(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyMsg); ok && !m.summary.list.SettingFilter() {
		switch key.String() {
		case "esc", "q":
			if m.summary.list.IsFiltered() {
				m.summary.list.ResetFilter()
				return m, nil
			}
			m.mode = modeEntries
			return m, nil
		case "tab":
			m.summary.zoom = (m.summary.zoom + 1) % zooms
			m.summary.fill(m.data, m.loc, m.now())
			return m, nil
		case "left", "h", "right", "l":
			dir := 1
			if k := key.String(); k == "left" || k == "h" {
				dir = -1
			}
			m.summary.step(dir)
			m.summary.fill(m.data, m.loc, m.now())
			return m, nil
		}
	}
	var cmd tea.Cmd
	m.summary.list, cmd = m.summary.list.Update(msg)
	return m, cmd
}
