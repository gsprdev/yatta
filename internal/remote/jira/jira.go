// Package jira uploads time as Jira worklogs and fetches issues as tasks.
//
// Which Jira a site runs is found by Probe, not asked of the user. Jira Cloud
// (hosted by Atlassian) takes the account email and an API token as basic
// auth, on REST v3. A scoped token, the least-privilege choice, is accepted
// only through Atlassian's API gateway, so Cloud requests go there when the
// site's cloud ID is known; a classic token is accepted only at the site
// itself. Jira Data Center (run by the company itself) takes a personal
// access token as a bearer token, on REST v2.
package jira

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote"
)

// DefaultQuery selects the issues offered as tasks when none is configured.
const DefaultQuery = "resolution = Unresolved AND (assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser()) ORDER BY key"

const pageSize = 100

// TokenURL is where a Jira Cloud user creates an API token.
const TokenURL = "https://id.atlassian.com/manage-profile/security/api-tokens"

// Scopes are all a Cloud token needs: who am I (/myself), searching issues,
// and adding worklogs.
const Scopes = "read:jira-user  read:jira-work  write:jira-work"

// CloudTokenHelp and DataCenterTokenHelp say how to make the token, in the
// words of the menus the user clicks through.
const (
	CloudTokenHelp = "Click your profile picture → Manage account → Security →\n" +
		"Create and manage API tokens. Or open:\n" + TokenURL + "\n" +
		"Choose \"Create API token with scopes\", app Jira, and only these scopes:\n" +
		"  " + Scopes
	DataCenterTokenHelp = "Click your profile picture → Profile → Personal Access Tokens →\n" +
		"Create token. Give it an expiry date."
)

// gateway is Atlassian's API gateway. A variable so tests can replace it.
var gateway = "https://api.atlassian.com"

// Site is a Jira address and what Probe learned of it.
type Site struct {
	URL     string // the site's root, without any page path
	Cloud   bool   // hosted by Atlassian
	CloudID string // the Cloud site's ID, for the API gateway; "" if unknown
}

// Guess is Probe without the network: an atlassian.net host is Cloud.
// raw may be any address copied from the browser.
func Guess(raw string) Site {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" {
		return Site{URL: strings.TrimRight(strings.TrimSpace(raw), "/")}
	}
	origin := u.Scheme + "://" + u.Host
	if strings.HasSuffix(u.Hostname(), ".atlassian.net") {
		return Site{URL: origin, Cloud: true}
	}
	// Data Center may be installed under a path such as /jira; keep it, but
	// not a page within it.
	path := u.Path
	for _, page := range []string{"/browse/", "/secure/", "/projects/", "/issues/", "/plugins/", "/rest/"} {
		if i := strings.Index(path+"/", page); i >= 0 {
			path = path[:i]
		}
	}
	return Site{URL: origin + strings.TrimRight(path, "/")}
}

// Probe finds whether raw is Jira Cloud by asking: only a Cloud site answers
// /_edge/tenant_info, without authentication, with its cloud ID. An error
// means the site could not be reached, and the Site returned is Guess's.
func Probe(ctx context.Context, raw string) (Site, error) {
	s := Guess(raw)
	u, err := url.Parse(s.URL)
	if err != nil || u.Host == "" {
		return s, fmt.Errorf("not a web address: %q", raw)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.Scheme+"://"+u.Host+"/_edge/tenant_info", nil)
	if err != nil {
		return s, err
	}
	req.Header.Set("User-Agent", "yatta")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return s, err
	}
	defer resp.Body.Close()
	var info struct {
		CloudID string `json:"cloudId"`
	}
	if resp.StatusCode == http.StatusOK && json.NewDecoder(resp.Body).Decode(&info) == nil && info.CloudID != "" {
		return Site{URL: u.Scheme + "://" + u.Host, Cloud: true, CloudID: info.CloudID}, nil
	}
	return s, nil
}

type Adapter struct {
	c            remote.Client
	site         Site
	classic      bool // a Cloud token accepted only at the site: unscoped
	query        string
	email, token string // for describing the credential, never for display in full
}

// New builds an adapter for site, as found by Probe. email is required for
// Cloud and unused for Data Center.
func New(site Site, email, token, query string) *Adapter {
	a := &Adapter{site: site, query: cmp.Or(query, DefaultQuery), email: email, token: token}
	// Without the cloud ID the gateway cannot be used, and at the site only a
	// classic token is accepted.
	a.classic = site.Cloud && site.CloudID == ""
	base := site.URL
	if site.Cloud && site.CloudID != "" {
		base = gateway + "/ex/jira/" + site.CloudID
	}
	a.c = remote.Client{BaseURL: base, Auth: func(r *http.Request) {
		if site.Cloud {
			r.SetBasicAuth(email, token)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}}
	return a
}

func (a *Adapter) Integration() string { return "jira" }

type myself struct {
	Name         string `json:"name"`
	DisplayName  string `json:"displayName"`
	EmailAddress string `json:"emailAddress"`
}

// Verify names the account. A Cloud token refused by the gateway is tried at
// the site, where only a classic (unscoped) token is accepted; if that works
// the adapter stays there, and the result says the token has full access.
func (a *Adapter) Verify(ctx context.Context) (string, error) {
	var me myself
	err := a.c.Do(ctx, http.MethodGet, a.api()+"/myself", nil, &me)
	if err != nil && unauthorized(err) && a.site.Cloud && a.c.BaseURL != a.site.URL {
		atSite := a.c
		atSite.BaseURL = a.site.URL
		var me2 myself
		if atSite.Do(ctx, http.MethodGet, a.api()+"/myself", nil, &me2) == nil {
			a.c, a.classic, me, err = atSite, true, me2, nil
		}
	}
	if err != nil {
		if unauthorized(err) && a.site.Cloud {
			return "", fmt.Errorf("%s: %w (check the email is exactly your Atlassian account's, "+
				"and the token has the scopes %s)", a.credential(), err, Scopes)
		}
		return "", fmt.Errorf("%s: %w", a.credential(), err)
	}
	who := me.DisplayName
	if id := cmp.Or(me.EmailAddress, me.Name); id != "" {
		who = strings.TrimSpace(who + " <" + id + ">")
	}
	if who == "" {
		return "", fmt.Errorf("Jira did not identify the account")
	}
	switch {
	case a.classic:
		return "Jira Cloud as " + who + " · ⚠ unscoped token: it can do anything your account can; a scoped token is safer", nil
	case a.site.Cloud:
		return "Jira Cloud as " + who, nil
	}
	return "Jira Data Center as " + who, nil
}

func unauthorized(err error) bool {
	var re *remote.Error
	return errors.As(err, &re) && (re.Status == http.StatusUnauthorized || re.Status == http.StatusForbidden)
}

// credential says what Verify sent, so a rejection shows which kind of Jira,
// account, and token were tried.
func (a *Adapter) credential() string {
	if !a.site.Cloud {
		return fmt.Sprintf("Jira Data Center at %s, personal access token %s", a.site.URL, remote.Fingerprint(a.token))
	}
	s := fmt.Sprintf("Jira Cloud, %s with API token %s", cmp.Or(a.email, "(no email)"), remote.Fingerprint(a.token))
	if a.site.CloudID == "" {
		s += ", at the site only (its cloud ID is unknown, so a scoped token cannot work)"
	}
	return s
}

func (a *Adapter) api() string {
	if a.site.Cloud {
		return "/rest/api/3"
	}
	return "/rest/api/2"
}

// extra is the Jira-specific part of a task, kept in core.RemoteTask.Extra.
type extra struct {
	Key string `json:"key,omitempty"`
}

type issue struct {
	ID     string `json:"id"`
	Key    string `json:"key"`
	Fields struct {
		Summary   string    `json:"summary"`
		IssueType issueType `json:"issuetype"`
		Project   struct {
			ID   string `json:"id"`
			Key  string `json:"key"`
			Name string `json:"name"`
		} `json:"project"`
		Parent *struct {
			ID     string `json:"id"`
			Key    string `json:"key"`
			Fields struct {
				Summary   string    `json:"summary"`
				IssueType issueType `json:"issuetype"`
			} `json:"fields"`
		} `json:"parent"`
	} `json:"fields"`
}

type issueType struct {
	Name string `json:"name"`
}

// FetchTasks returns the issues matching the query, beneath their projects
// and, where Jira reports one, their parent issue (epic or story).
func (a *Adapter) FetchTasks(ctx context.Context) ([]core.FetchedTask, error) {
	issues, err := a.search(ctx)
	if err != nil {
		return nil, err
	}
	var out []core.FetchedTask
	index := map[string]int{}
	put := func(t core.FetchedTask, override bool) {
		if i, ok := index[t.NativeID]; ok {
			if override {
				out[i] = t
			}
			return
		}
		index[t.NativeID] = len(out)
		out = append(out, t)
	}
	for _, is := range issues {
		p := is.Fields.Project
		put(core.FetchedTask{NativeID: "project:" + p.ID, Name: p.Name, Label: p.Key, NodeType: "project"}, false)
	}
	for _, is := range issues {
		projectID := "project:" + is.Fields.Project.ID
		parentID := projectID
		if par := is.Fields.Parent; par != nil {
			parentID = par.ID
			// The parent may not match the query itself; list it from what
			// Jira embeds, unless the issue is listed in its own right.
			put(core.FetchedTask{
				NativeID: par.ID, ParentNativeID: projectID, Name: par.Fields.Summary, Label: par.Key,
				NodeType: nodeType(par.Fields.IssueType), Extra: mustJSON(extra{Key: par.Key}),
			}, false)
		}
		put(core.FetchedTask{
			NativeID: is.ID, ParentNativeID: parentID, Name: is.Fields.Summary, Label: is.Key,
			NodeType: nodeType(is.Fields.IssueType), Extra: mustJSON(extra{Key: is.Key}),
		}, true)
	}
	return out, nil
}

func (a *Adapter) search(ctx context.Context) ([]issue, error) {
	fields := []string{"summary", "issuetype", "project", "parent"}
	var all []issue
	if a.site.Cloud {
		token := ""
		for {
			body := map[string]any{"jql": a.query, "fields": fields, "maxResults": pageSize}
			if token != "" {
				body["nextPageToken"] = token
			}
			var page struct {
				Issues        []issue `json:"issues"`
				NextPageToken string  `json:"nextPageToken"`
				IsLast        bool    `json:"isLast"`
			}
			if err := a.c.Do(ctx, http.MethodPost, a.api()+"/search/jql", body, &page); err != nil {
				return nil, fmt.Errorf("search issues: %w", err)
			}
			all = append(all, page.Issues...)
			if page.IsLast || page.NextPageToken == "" || len(page.Issues) == 0 {
				return all, nil
			}
			token = page.NextPageToken
		}
	}
	for {
		body := map[string]any{"jql": a.query, "fields": fields, "maxResults": pageSize, "startAt": len(all)}
		var page struct {
			Issues []issue `json:"issues"`
			Total  int     `json:"total"`
		}
		if err := a.c.Do(ctx, http.MethodPost, a.api()+"/search", body, &page); err != nil {
			return nil, fmt.Errorf("search issues: %w", err)
		}
		all = append(all, page.Issues...)
		if len(page.Issues) == 0 || len(all) >= page.Total {
			return all, nil
		}
	}
}

func nodeType(t issueType) string { return strings.ToLower(t.Name) }

// Create adds a worklog to the task's issue.
func (a *Adapter) Create(ctx context.Context, task core.Task, u core.Unit) (string, error) {
	if task.Remote == nil || task.Remote.NodeType == "project" {
		return "", fmt.Errorf("Jira records time on issues, not projects: choose an issue")
	}
	body := map[string]any{
		"started":          u.Start.UTC().Format("2006-01-02T15:04:05.000-0700"),
		"timeSpentSeconds": int64(u.Duration.Seconds()),
	}
	if u.Note != "" {
		if a.site.Cloud {
			body["comment"] = adf(u.Note)
		} else {
			body["comment"] = u.Note
		}
	}
	var resp struct {
		ID string `json:"id"`
	}
	path := fmt.Sprintf("%s/issue/%s/worklog", a.api(), task.Remote.NativeID)
	if err := a.c.Do(ctx, http.MethodPost, path, body, &resp); err != nil {
		return "", err
	}
	if resp.ID == "" {
		return "", fmt.Errorf("Jira accepted the worklog but returned no id")
	}
	return resp.ID, nil
}

// adf wraps plain text in the Atlassian Document Format REST v3 requires.
func adf(text string) map[string]any {
	return map[string]any{
		"type": "doc", "version": 1,
		"content": []any{map[string]any{
			"type":    "paragraph",
			"content": []any{map[string]any{"type": "text", "text": text}},
		}},
	}
}

func mustJSON(v any) json.RawMessage {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
