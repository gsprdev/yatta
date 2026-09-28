package ui

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/huh"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote/jira"
	"github.com/gsprdev/yatta/internal/remote/redmine"
	"github.com/gsprdev/yatta/internal/store"
)

// Secrets stores credentials in the OS keyring. The database holds only the key.
type Secrets interface {
	Get(key string) (string, error)
	Set(key, value string) error
}

// Credential is what is stored in the keyring for an integration.
type Credential struct {
	User  string `json:"user,omitempty"` // Jira: account email
	Token string `json:"token"`          // API token or key
}

type settingsScreen int

const (
	screenMenu settingsScreen = iota
	screenPolicy
	screenIntegration
	screenPurgeDate
	screenPurgeConfirm
)

var settingsMenu = []string{"Upload policy", "Remote integration", "Purge old entries"}

type settingsModel struct {
	screen settingsScreen
	cursor int
	form   *huh.Form
	v      *settingsValues
}

// settingsValues is bound to the forms by pointer, so it lives on the heap and
// survives the model being copied.
type settingsValues struct {
	increment, direction, minimum, belowMin, aggregate string

	integration, baseURL, user, token, query string
	// stored is the credential of the configured integration, storedFor, so
	// the form can show its email and keep its token when the token field is
	// left empty.
	stored    *Credential
	storedFor string

	purgeDate                   string
	purgeBefore                 time.Time
	purgeOK                     bool
	failed, pending, unassigned int
}

type purgeCountsMsg struct {
	failed, pending, unassigned int
	err                         error
}

func newSettings(d data, secrets Secrets) settingsModel {
	v := &settingsValues{
		increment: fmt.Sprint(int(d.policy.Increment / time.Minute)),
		direction: string(orDefault(d.policy.Direction, core.Nearest)),
		minimum:   fmt.Sprint(int(d.policy.Minimum / time.Minute)),
		belowMin:  string(orDefault(d.policy.BelowMin, core.Exclude)),
		aggregate: string(orDefault(d.policy.Aggregate, core.AggNone)),
	}
	v.integration = "none"
	if c := d.config; c != nil {
		v.integration, v.baseURL, v.query = c.Integration, c.BaseURL, c.TaskQuery
		if secrets != nil {
			var cred Credential
			if raw, err := secrets.Get(c.KeyringKey); err == nil && json.Unmarshal([]byte(raw), &cred) == nil {
				v.stored, v.storedFor, v.user = &cred, c.Integration, cred.User
			}
		}
	}
	return settingsModel{v: v}
}

func orDefault[T ~string](v, def T) T {
	if v == "" {
		return def
	}
	return v
}

func (s settingsModel) init() tea.Cmd { return nil }

func minutesOptions(none string) []huh.Option[string] {
	return []huh.Option[string]{
		huh.NewOption(none, "0"), huh.NewOption("15 minutes", "15"),
		huh.NewOption("30 minutes", "30"), huh.NewOption("60 minutes", "60"),
	}
}

func (s *settingsModel) open(screen settingsScreen) tea.Cmd {
	s.screen = screen
	v := s.v
	switch screen {
	case screenPolicy:
		s.form = huh.NewForm(huh.NewGroup(
			huh.NewSelect[string]().Title("Round to").Options(minutesOptions("No rounding")...).Value(&v.increment),
			huh.NewSelect[string]().Title("Rounding direction").Options(
				huh.NewOption("Nearest", string(core.Nearest)), huh.NewOption("Up", string(core.Up)),
				huh.NewOption("Down", string(core.Down))).Value(&v.direction),
			huh.NewSelect[string]().Title("Minimum duration").Options(minutesOptions("No minimum")...).Value(&v.minimum),
			huh.NewSelect[string]().Title("Below the minimum").Options(
				huh.NewOption("Exclude from upload", string(core.Exclude)),
				huh.NewOption("Round up to the minimum", string(core.RoundUpToMin))).Value(&v.belowMin),
			huh.NewSelect[string]().Title("Aggregation").Options(
				huh.NewOption("None: one record per entry", string(core.AggNone)),
				huh.NewOption("One record per task per day", string(core.AggTaskDay))).Value(&v.aggregate),
		))
	case screenIntegration:
		v.token = ""
		tokenDesc := "Stored in the OS keyring, never in the database."
		if v.stored != nil {
			tokenDesc += "\nLeave empty to keep the stored " + v.storedFor + " token."
		}
		s.form = huh.NewForm(
			huh.NewGroup(huh.NewSelect[string]().Title("Remote system").Options(
				huh.NewOption("None", "none"), huh.NewOption("Jira", "jira"),
				huh.NewOption("Redmine", "redmine"), huh.NewOption("Toggl", "toggl")).Value(&v.integration)),
			huh.NewGroup(
				huh.NewInput().Title("Base URL").Placeholder("https://example.atlassian.net").Value(&v.baseURL).
					Validate(func(u string) error {
						if !strings.HasPrefix(u, "https://") && !strings.HasPrefix(u, "http://") {
							return fmt.Errorf("enter the full URL, starting with https://")
						}
						return nil
					}),
			).WithHideFunc(func() bool { return v.integration != "jira" && v.integration != "redmine" }),
			huh.NewGroup(
				huh.NewInput().Title("Account email").
					Description("Jira Cloud: your Atlassian email, with an API token below.\nJira Data Center: leave empty and use a personal access token.").
					Value(&v.user),
				huh.NewInput().Title("Issues to offer as tasks (JQL)").
					Description("Empty for: "+jira.DefaultQuery).Value(&v.query),
			).WithHideFunc(func() bool { return v.integration != "jira" }),
			huh.NewGroup(
				huh.NewInput().Title("Issues to offer as tasks (issue filter)").
					Description("Empty for: "+redmine.DefaultQuery).Value(&v.query),
			).WithHideFunc(func() bool { return v.integration != "redmine" }),
			huh.NewGroup(
				huh.NewInput().Title("API token").Description(tokenDesc).
					EchoMode(huh.EchoModePassword).Value(&v.token).
					Validate(func(t string) error {
						if strings.TrimSpace(t) == "" && (v.stored == nil || v.integration != v.storedFor) {
							return fmt.Errorf("a token is required")
						}
						return nil
					}),
			).WithHideFunc(func() bool { return v.integration == "none" }),
		)
	case screenPurgeDate:
		v.purgeDate = ""
		s.form = huh.NewForm(huh.NewGroup(
			huh.NewInput().Title("Purge entries starting before").Placeholder("YYYY-MM-DD").
				Description("Removes every entry before that date, in any state. Uploaded time stays in the remote system.").
				Value(&v.purgeDate).Validate(func(d string) error {
				if _, err := time.Parse("2006-01-02", strings.TrimSpace(d)); err != nil {
					return fmt.Errorf("use YYYY-MM-DD")
				}
				return nil
			}),
		))
	case screenPurgeConfirm:
		desc := "No unuploaded entries are affected."
		var warn []string
		if v.failed > 0 {
			warn = append(warn, fmt.Sprintf("⚠ %d FAILED %s will be lost: these uploads never succeeded.",
				v.failed, plural(v.failed, "entry", "entries")))
		}
		if v.pending > 0 {
			warn = append(warn, fmt.Sprintf("%d pending %s never uploaded.", v.pending, plural(v.pending, "entry was", "entries were")))
		}
		if v.unassigned > 0 {
			warn = append(warn, fmt.Sprintf("%d %s no task.", v.unassigned, plural(v.unassigned, "entry has", "entries have")))
		}
		if len(warn) > 0 {
			desc = strings.Join(warn, "\n")
		}
		v.purgeOK = false
		s.form = huh.NewForm(huh.NewGroup(
			huh.NewConfirm().Title("Purge entries before " + v.purgeDate + "?").Description(desc).
				Affirmative("Purge").Negative("Cancel").Value(&v.purgeOK),
		))
	default:
		s.form = nil
		return nil
	}
	return s.form.Init()
}

func (s settingsModel) help() string {
	if s.screen == screenMenu {
		return "↑↓ choose · enter open · esc back"
	}
	return "enter next · esc back"
}

// view shows the menu, with the state of the remote connection beneath its
// entry.
func (s settingsModel) view(connection string) string {
	if s.form != nil {
		return s.form.View()
	}
	var b strings.Builder
	b.WriteString(boldStyle.Render("Settings") + "\n\n")
	for i, item := range settingsMenu {
		marker := "  "
		if i == s.cursor {
			marker = "› "
		}
		b.WriteString(marker + item + "\n")
		if i == 1 && connection != "" {
			b.WriteString("    " + connection + "\n")
		}
	}
	return b.String()
}

// connection describes the remote connection for the settings menu.
func (m Model) connection() string {
	switch {
	case m.connErr != nil:
		return errStyle.Render("✗ " + m.connErr.Error())
	case m.who != "":
		return okStyle.Render("✓ " + m.who)
	case m.data.config != nil:
		return faintStyle.Render("not yet verified")
	}
	return faintStyle.Render("none configured")
}

func (m Model) updateSettings(msg tea.Msg) (tea.Model, tea.Cmd) {
	s := &m.settings
	if msg, ok := msg.(purgeCountsMsg); ok {
		if msg.err != nil {
			s.screen, s.form = screenMenu, nil
			return m, flash(msg.err.Error(), true)
		}
		s.v.failed, s.v.pending, s.v.unassigned = msg.failed, msg.pending, msg.unassigned
		return m, s.open(screenPurgeConfirm)
	}
	key, isKey := msg.(tea.KeyMsg)
	if s.form == nil {
		if !isKey {
			return m, nil
		}
		switch key.String() {
		case "esc", "q":
			m.mode = modeEntries
		case "up", "k":
			s.cursor = (s.cursor + len(settingsMenu) - 1) % len(settingsMenu)
		case "down", "j":
			s.cursor = (s.cursor + 1) % len(settingsMenu)
		case "enter":
			return m, s.open([]settingsScreen{screenPolicy, screenIntegration, screenPurgeDate}[s.cursor])
		}
		return m, nil
	}
	if isKey && key.String() == "esc" {
		s.screen, s.form = screenMenu, nil
		return m, nil
	}
	f, cmd := s.form.Update(msg)
	s.form = f.(*huh.Form)
	switch s.form.State {
	case huh.StateAborted:
		s.screen, s.form = screenMenu, nil
		return m, nil
	case huh.StateCompleted:
		screen := s.screen
		s.screen, s.form = screenMenu, nil
		return m.settingsDone(screen)
	}
	return m, cmd
}

func (m Model) settingsDone(screen settingsScreen) (tea.Model, tea.Cmd) {
	v := m.settings.v
	switch screen {
	case screenPolicy:
		var inc, min int
		_, _ = fmt.Sscan(v.increment, &inc)
		_, _ = fmt.Sscan(v.minimum, &min)
		p := core.Policy{
			Increment: time.Duration(inc) * time.Minute, Direction: core.Direction(v.direction),
			Minimum: time.Duration(min) * time.Minute, Aggregate: core.AggKey(v.aggregate),
		}
		if p.Minimum > 0 {
			p.BelowMin = core.BelowMin(v.belowMin)
		}
		return m, m.mutate("upload policy saved", func(st *store.Store) error { return st.SetPolicy(p) })

	case screenIntegration:
		if v.integration == "none" {
			m.ad, m.who, m.connErr = nil, "", nil
			return m, m.mutate("integration removed", func(st *store.Store) error { return st.ClearIntegration() })
		}
		cfg := store.IntegrationConfig{Integration: v.integration, KeyringKey: v.integration}
		if v.integration != "toggl" {
			cfg.BaseURL = strings.TrimRight(strings.TrimSpace(v.baseURL), "/")
			cfg.TaskQuery = strings.TrimSpace(v.query)
		}
		token := strings.TrimSpace(v.token)
		if token == "" && v.stored != nil && v.integration == v.storedFor {
			token = v.stored.Token
		}
		cred, err := json.Marshal(Credential{User: strings.TrimSpace(v.user), Token: token})
		if err != nil {
			return m, flash(err.Error(), true)
		}
		secrets := m.secrets
		if secrets == nil {
			return m, flash("no keyring available to store the credential", true)
		}
		save := m.mutate("integration saved", func(st *store.Store) error {
			if err := secrets.Set(cfg.KeyringKey, string(cred)); err != nil {
				return fmt.Errorf("store credential in keyring: %w", err)
			}
			return st.SetIntegration(cfg)
		})
		return m, tea.Sequence(save, m.connectCmd(true))

	case screenPurgeDate:
		day, _ := time.ParseInLocation("2006-01-02", strings.TrimSpace(v.purgeDate), m.loc)
		v.purgeBefore = day
		st := m.st
		return m, func() tea.Msg {
			f, p, u, err := st.PurgeCounts(day)
			return purgeCountsMsg{f, p, u, err}
		}

	case screenPurgeConfirm:
		if !v.purgeOK {
			return m, flash("purge cancelled", false)
		}
		before := v.purgeBefore
		return m, m.mutate("entries purged", func(st *store.Store) error { return st.Purge(before) })
	}
	return m, nil
}
