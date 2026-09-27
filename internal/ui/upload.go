package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote"
	"github.com/gsprdev/yatta/internal/store"
)

type uploadStage int

const (
	stageBound   uploadStage = iota // choose the optional end date
	stageConfirm                    // review the plan
	stageRunning                    // blocking: results stream in
	stageDone
)

// uploadTimeout bounds each remote call.
const uploadTimeout = 30 * time.Second

type uploadModel struct {
	stage   uploadStage
	bound   textinput.Model
	err     string
	plan    core.Plan
	results []error // one per upload unit; nil until done or on success
	done    []bool
	next    int
	failed  int
}

type uploadPlannedMsg struct {
	plan core.Plan
	err  error
}

type exclusionsRecordedMsg struct{ err error }

type unitDoneMsg struct {
	index int
	err   error
}

func (m Model) openUpload() (tea.Model, tea.Cmd) {
	if m.ad == nil {
		return m, flash("no remote integration is configured (settings: ,)", true)
	}
	b := textinput.New()
	b.Prompt = "Upload entries through (YYYY-MM-DD, empty for all): "
	b.Focus()
	m.upload = uploadModel{bound: b}
	m.mode = modeUpload
	return m, textinput.Blink
}

func (u uploadModel) help() string {
	switch u.stage {
	case stageBound:
		return "enter plan · esc cancel"
	case stageConfirm:
		if len(u.plan.Upload)+len(u.plan.Excluded) == 0 {
			return "esc back"
		}
		return "y upload · esc cancel"
	case stageRunning:
		return "uploading… input is blocked until every record is done"
	}
	return "enter done"
}

func (m Model) updateUpload(msg tea.Msg) (tea.Model, tea.Cmd) {
	u := &m.upload
	switch msg := msg.(type) {
	case uploadPlannedMsg:
		if msg.err != nil {
			u.stage, u.err = stageBound, msg.err.Error()
			return m, nil
		}
		u.plan, u.stage = msg.plan, stageConfirm
		u.results = make([]error, len(msg.plan.Upload))
		u.done = make([]bool, len(msg.plan.Upload))
		return m, nil
	case exclusionsRecordedMsg:
		if msg.err != nil {
			// Nothing has been sent yet; stop before any remote call.
			u.stage, u.err = stageDone, "recording exclusions failed: "+msg.err.Error()
			return m, nil
		}
		return m, m.uploadUnit(0)
	case unitDoneMsg:
		u.results[msg.index], u.done[msg.index] = msg.err, true
		if msg.err != nil {
			u.failed++
		}
		if msg.index+1 < len(u.plan.Upload) {
			return m, m.uploadUnit(msg.index + 1)
		}
		u.stage = stageDone
		return m, load(m.st)
	case tea.KeyMsg:
		switch u.stage {
		case stageBound:
			switch msg.String() {
			case "esc":
				m.mode = modeEntries
				return m, nil
			case "enter":
				return m, m.planUpload(strings.TrimSpace(u.bound.Value()))
			}
			var cmd tea.Cmd
			u.bound, cmd = u.bound.Update(msg)
			return m, cmd
		case stageConfirm:
			switch msg.String() {
			case "esc", "n":
				m.mode = modeEntries
			case "y":
				if len(u.plan.Upload)+len(u.plan.Excluded) == 0 {
					return m, nil
				}
				u.stage = stageRunning
				return m, m.recordExclusions()
			}
		case stageDone:
			switch msg.String() {
			case "enter", "esc", "q":
				m.mode = modeEntries
				return m, load(m.st)
			}
		}
	}
	return m, nil
}

func (m Model) planUpload(bound string) tea.Cmd {
	var through time.Time
	if bound != "" {
		day, err := time.ParseInLocation("2006-01-02", bound, m.loc)
		if err != nil {
			return func() tea.Msg { return uploadPlannedMsg{err: errors.New("date: use YYYY-MM-DD")} }
		}
		through = core.EndOfDay(day, m.loc)
	}
	st, policy, loc := m.st, m.data.policy, m.loc
	return func() tea.Msg {
		entries, err := st.UnlockedRemoteEntries(through)
		if err != nil {
			return uploadPlannedMsg{err: err}
		}
		return uploadPlannedMsg{plan: core.PlanUpload(entries, policy, loc, through)}
	}
}

// recordExclusions writes the sentinel records before anything is sent, so an
// excluded unit is settled even if the uploads that follow fail.
func (m Model) recordExclusions() tea.Cmd {
	st, excluded, integ := m.st, m.upload.plan.Excluded, m.ad.Integration()
	return func() tea.Msg {
		for _, u := range excluded {
			if err := st.RecordExcluded(u, integ); err != nil {
				return exclusionsRecordedMsg{err}
			}
		}
		return exclusionsRecordedMsg{}
	}
}

// uploadUnit sends one unit and persists its result. A failure is stored on
// the unit's entries; it never stops the units after it.
func (m Model) uploadUnit(i int) tea.Cmd {
	units := m.upload.plan.Upload
	if i >= len(units) {
		return func() tea.Msg { return unitDoneMsg{index: i - 1} }
	}
	unit, task, st, ad := units[i], m.data.taskByID[units[i].TaskID], m.st, m.ad
	return func() tea.Msg {
		err := uploadOne(ad, st, task, unit)
		if err != nil {
			if serr := st.RecordFailure(unit, err); serr != nil {
				err = fmt.Errorf("%v (and saving the failure: %v)", err, serr)
			}
			return unitDoneMsg{i, err}
		}
		return unitDoneMsg{i, nil}
	}
}

// uploadOne sends one unit and records the success. A unit whose task has
// departed fails without a remote call: the task may still exist upstream,
// but the user must reassign the entries first.
func uploadOne(ad remote.Adapter, st *store.Store, task core.Task, unit core.Unit) error {
	if task.Remote == nil {
		return errors.New("task is not a remote task")
	}
	if task.Remote.Departed != nil {
		return errors.New("task departed upstream: reassign these entries before uploading")
	}
	if task.Remote.Integration != ad.Integration() {
		return fmt.Errorf("task belongs to %s, but the configured integration is %s", task.Remote.Integration, ad.Integration())
	}
	ctx, cancel := context.WithTimeout(context.Background(), uploadTimeout)
	defer cancel()
	remoteID, err := ad.Create(ctx, task, unit)
	if err != nil {
		return err
	}
	if err := st.RecordUpload(unit, ad.Integration(), remoteID); err != nil {
		// The remote has the record; retrying would duplicate it.
		return fmt.Errorf("uploaded as remote id %s, but saving that locally failed: %w; check the remote before retrying", remoteID, err)
	}
	return nil
}

func (u uploadModel) view(d data, loc *time.Location) string {
	var b strings.Builder
	b.WriteString(boldStyle.Render("Upload") + "\n\n")
	switch u.stage {
	case stageBound:
		b.WriteString(u.bound.View() + "\n")
		if u.err != "" {
			b.WriteString("\n" + errStyle.Render(u.err) + "\n")
		}
		return b.String()
	case stageConfirm:
		if len(u.plan.Upload)+len(u.plan.Excluded) == 0 {
			return b.String() + "Nothing is pending in that range.\n"
		}
	}
	if n := len(u.plan.Upload); n > 0 {
		fmt.Fprintf(&b, "%d %s to upload:\n", n, plural(n, "record", "records"))
		for i, unit := range u.plan.Upload {
			status := "  "
			if u.stage >= stageRunning {
				switch {
				case !u.done[i]:
					status = faintStyle.Render("… ")
				case u.results[i] != nil:
					status = errStyle.Render("✗ ")
				default:
					status = okStyle.Render("✓ ")
				}
			}
			b.WriteString(status + unitLine(unit, d, loc) + "\n")
			if u.done != nil && u.done[i] && u.results[i] != nil {
				b.WriteString("    " + errStyle.Render(u.results[i].Error()) + "\n")
			}
		}
	}
	if n := len(u.plan.Excluded); n > 0 {
		fmt.Fprintf(&b, "\n%s\n", warnStyle.Render(fmt.Sprintf("%d %s below the minimum, recorded as excluded and not sent:",
			n, plural(n, "record is", "records are"))))
		for _, unit := range u.plan.Excluded {
			b.WriteString("  " + unitLine(unit, d, loc) + "\n")
		}
	}
	if u.stage == stageDone {
		ok := len(u.plan.Upload) - u.failed
		fmt.Fprintf(&b, "\nDone: %d uploaded, %d failed, %d excluded.\n", ok, u.failed, len(u.plan.Excluded))
		if u.err != "" {
			b.WriteString(errStyle.Render(u.err) + "\n")
		}
		if u.failed > 0 {
			b.WriteString("Failed entries are listed under attention (!) and stay editable.\n")
		}
	}
	return b.String()
}

func unitLine(u core.Unit, d data, loc *time.Location) string {
	dur := short(u.Duration)
	if u.Duration != u.Raw {
		dur = short(u.Raw) + " → " + dur
	}
	line := fmt.Sprintf("%s  %-8s %s", u.Start.In(loc).Format("Mon 02 Jan"), dur, taskTitle(d.taskByID[u.TaskID]))
	if n := len(u.Entries); n > 1 {
		line += fmt.Sprintf(" (%d entries)", n)
	}
	if u.Note != "" {
		line += " — " + u.Note
	}
	return line
}
