package ui

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gsprdev/yatta/internal/remote"
	"github.com/gsprdev/yatta/internal/remote/jira"
	"github.com/gsprdev/yatta/internal/secret"
	"github.com/gsprdev/yatta/internal/store"
)

type memSecrets map[string]string

func (s memSecrets) Get(key string) (string, error) {
	v, ok := s[key]
	if !ok {
		return "", secret.ErrNotFound
	}
	return v, nil
}

func (s memSecrets) Set(key, value string) error { s[key] = value; return nil }

// probeAs makes every Jira address probe as site, or as Guess has it when
// site is nil, so no test reaches the network.
func probeAs(t *testing.T, site *jira.Site) {
	old := probeJira
	probeJira = func(_ context.Context, u string) (jira.Site, error) {
		if site != nil {
			return *site, nil
		}
		return jira.Guess(u), nil
	}
	t.Cleanup(func() { probeJira = old })
}

// Editing only the query must keep the stored email and token (losing the
// email silently switched a Jira Cloud setup to Data Center mode), and the
// reconnection is verified and shown.
func TestIntegrationEditKeepsCredential(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if u, p, _ := r.BasicAuth(); u != "me@example.com" || p != "tok" {
			w.WriteHeader(401)
			io.WriteString(w, `{"errorMessages":["Client must be authenticated"]}`)
			return
		}
		switch r.URL.Path {
		case "/rest/api/3/myself":
			io.WriteString(w, `{"displayName":"Me","emailAddress":"me@example.com"}`)
		case "/rest/api/3/search/jql":
			io.WriteString(w, `{"issues":[],"isLast":true}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer srv.Close()
	must(t, st.SetIntegration(store.IntegrationConfig{Integration: "jira", BaseURL: srv.URL, KeyringKey: "jira"}))
	keys := memSecrets{"jira": `{"user":"me@example.com","token":"tok"}`}
	connect := func(cfg *store.IntegrationConfig) (remote.Adapter, error) {
		var cred Credential
		json.Unmarshal([]byte(keys[cfg.KeyringKey]), &cred)
		return jira.New(jira.Site{URL: cfg.BaseURL, Cloud: true}, cred.User, cred.Token, cfg.TaskQuery), nil
	}
	probeAs(t, &jira.Site{URL: srv.URL, Cloud: true})

	d := &driver{t: t, m: New(st, connect, keys, time.UTC)}
	d.send(load(st)())
	d.keys(",", "j", "enter") // settings → Remote integration
	d.keys("enter", "enter", "enter", "project = X", "enter", "enter")

	cfg, err := st.Integration()
	must(t, err)
	if cfg.TaskQuery != "project = X" {
		t.Fatalf("query = %q; the form did not complete", cfg.TaskQuery)
	}
	var cred Credential
	must(t, json.Unmarshal([]byte(keys["jira"]), &cred))
	if cred != (Credential{User: "me@example.com", Token: "tok"}) {
		t.Errorf("stored credential = %+v; want the original email and token kept", cred)
	}
	if got := d.model().View(); !strings.Contains(got, "✓ Jira Cloud as Me <me@example.com> · ⚠ unscoped token") {
		t.Errorf("settings do not show the verified account:\n%s", got)
	}

	// A rejected credential is shown as such.
	keys["jira"] = `{"user":"me@example.com","token":"revoked"}`
	d.send(d.model().connectCmd(false)())
	if got := d.model().View(); !strings.Contains(got, "✗ could not verify the credentials: Jira Cloud, me@example.com with API token (7 chars)") {
		t.Errorf("settings do not show the failed verification:\n%s", got)
	}
}

// A token left empty is kept only for the integration it belongs to.
func TestIntegrationSwitchNeedsToken(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must(t, st.SetIntegration(store.IntegrationConfig{Integration: "jira", BaseURL: "https://x", KeyringKey: "jira"}))
	keys := memSecrets{"jira": `{"user":"me@example.com","token":"tok"}`}
	probeAs(t, nil)
	d := &driver{t: t, m: New(st, nil, keys, time.UTC)}
	d.send(load(st)())
	d.keys(",", "j", "enter")
	d.keys("j", "enter", "enter", "enter", "enter") // Redmine; URL; filter; empty token
	if d.model().settings.form == nil {
		t.Fatal("the form completed with no token for a newly chosen integration")
	}
	if _, ok := keys["redmine"]; ok {
		t.Error("a credential was stored for redmine")
	}
}

// A Jira Cloud site cannot be saved without an account email, which would
// otherwise select Data Center mode.
func TestJiraCloudNeedsEmail(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	must(t, st.SetIntegration(store.IntegrationConfig{Integration: "jira", BaseURL: "https://x.atlassian.net", KeyringKey: "jira"}))
	keys := memSecrets{"jira": `{"token":"tok"}`} // the email lost to the earlier bug
	probeAs(t, nil)
	d := &driver{t: t, m: New(st, nil, keys, time.UTC)}
	d.send(load(st)())
	d.keys(",", "j", "enter")
	d.keys("enter", "enter", "enter") // system; URL; empty email
	if got := d.model().View(); !strings.Contains(got, "Jira Cloud needs your account email") {
		t.Errorf("an empty email was accepted for a Cloud site:\n%s", got)
	}
	d.keys("me@example.com", "enter", "enter") // email; query
	if got := d.model().View(); !strings.Contains(got, jira.TokenURL) || !strings.Contains(got, jira.Scopes) {
		t.Errorf("the token field does not say where to create a token:\n%s", got)
	}
	d.keys("enter") // kept token
	var cred Credential
	must(t, json.Unmarshal([]byte(keys["jira"]), &cred))
	if cred != (Credential{User: "me@example.com", Token: "tok"}) {
		t.Errorf("stored credential = %+v", cred)
	}
}

// A Data Center site is asked only for a personal access token, and the
// address is saved as the site root, not the page it was copied from.
func TestJiraDataCenterSetup(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	keys := memSecrets{}
	probeAs(t, nil)
	d := &driver{t: t, m: New(st, nil, keys, time.UTC)}
	d.send(load(st)())
	d.keys(",", "j", "enter")
	d.keys("j", "enter")                                      // Jira
	d.keys("https://jira.example.com/browse/PROJ-1", "enter") // address
	d.keys("enter")                                           // default JQL
	if got := d.model().View(); !strings.Contains(got, "Personal access token") || strings.Contains(got, "email") {
		t.Errorf("want the personal access token field next, with no email asked:\n%s", got)
	}
	d.keys("pat", "enter")

	cfg, err := st.Integration()
	must(t, err)
	if cfg == nil || cfg.BaseURL != "https://jira.example.com" {
		t.Fatalf("config = %+v; want the site root saved", cfg)
	}
	if keys["jira"] != `{"token":"pat"}` {
		t.Errorf("stored credential = %s", keys["jira"])
	}
}
