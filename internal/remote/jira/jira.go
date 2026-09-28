// Package jira uploads time as Jira worklogs and fetches issues as tasks.
//
// Jira Cloud is used when an account email is given (basic auth with an API
// token, REST v3). Without one, the token is sent as a bearer personal access
// token to REST v2, as Jira Data Center expects.
package jira

import (
	"cmp"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote"
)

// DefaultQuery selects the issues offered as tasks when none is configured.
const DefaultQuery = "resolution = Unresolved AND (assignee = currentUser() OR reporter = currentUser() OR watcher = currentUser()) ORDER BY key"

const pageSize = 100

type Adapter struct {
	c     remote.Client
	cloud bool
	query string
}

func New(baseURL, email, token, query string) *Adapter {
	a := &Adapter{cloud: email != "", query: query}
	if a.query == "" {
		a.query = DefaultQuery
	}
	a.c = remote.Client{BaseURL: baseURL, Auth: func(r *http.Request) {
		if a.cloud {
			r.SetBasicAuth(email, token)
		} else {
			r.Header.Set("Authorization", "Bearer "+token)
		}
	}}
	return a
}

func (a *Adapter) Integration() string { return "jira" }

// Verify names the account and which of Cloud or Data Center is assumed: an
// account email selects Cloud, so a missing one shows here, not as an empty
// fetch.
func (a *Adapter) Verify(ctx context.Context) (string, error) {
	var me struct {
		Name         string `json:"name"`
		DisplayName  string `json:"displayName"`
		EmailAddress string `json:"emailAddress"`
	}
	if err := a.c.Do(ctx, http.MethodGet, a.api()+"/myself", nil, &me); err != nil {
		return "", err
	}
	who := me.DisplayName
	if id := cmp.Or(me.EmailAddress, me.Name); id != "" {
		who = strings.TrimSpace(who + " <" + id + ">")
	}
	if who == "" {
		return "", fmt.Errorf("Jira did not identify the account")
	}
	if a.cloud {
		return "Jira Cloud (REST v3) as " + who, nil
	}
	return "Jira Data Center (REST v2, access token) as " + who, nil
}

func (a *Adapter) api() string {
	if a.cloud {
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
	if a.cloud {
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
		if a.cloud {
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
