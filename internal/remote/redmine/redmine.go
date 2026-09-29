// Package redmine uploads time as Redmine time entries and fetches projects,
// versions, and issues as tasks.
package redmine

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote"
)

// DefaultQuery is the issue filter used when none is configured.
const DefaultQuery = "status_id=open&assigned_to_id=me"

const pageSize = 100

type Adapter struct {
	c     remote.Client
	query url.Values
	loc   *time.Location // for the spent_on date
	key   string         // for describing the credential, never for display in full
}

// New builds a Redmine adapter. query is an issues.json filter in URL query
// form; loc is the device timezone, which decides the day time is logged on.
func New(baseURL, apiKey, query string, loc *time.Location) (*Adapter, error) {
	if query == "" {
		query = DefaultQuery
	}
	q, err := url.ParseQuery(query)
	if err != nil {
		return nil, fmt.Errorf("Redmine issue filter: %w", err)
	}
	return &Adapter{
		c:     remote.Client{BaseURL: baseURL, Auth: func(r *http.Request) { r.Header.Set("X-Redmine-API-Key", apiKey) }},
		query: q,
		loc:   loc,
		key:   apiKey,
	}, nil
}

func (a *Adapter) Integration() string { return "redmine" }

// Verify names the account the API key belongs to.
func (a *Adapter) Verify(ctx context.Context) (string, error) {
	var resp struct {
		User struct {
			Login     string `json:"login"`
			Firstname string `json:"firstname"`
			Lastname  string `json:"lastname"`
		} `json:"user"`
	}
	if err := a.c.Do(ctx, http.MethodGet, "/users/current.json", nil, &resp); err != nil {
		return "", fmt.Errorf("Redmine, API key %s: %w", remote.Fingerprint(a.key), err)
	}
	u := resp.User
	if u.Login == "" {
		return "", fmt.Errorf("Redmine did not identify the account")
	}
	return strings.TrimSpace("Redmine as "+u.Firstname+" "+u.Lastname) + " (" + u.Login + ")", nil
}

type ref struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

type project struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	Identifier string `json:"identifier"`
	Parent     *ref   `json:"parent"`
}

type issue struct {
	ID           int    `json:"id"`
	Subject      string `json:"subject"`
	Project      ref    `json:"project"`
	Tracker      ref    `json:"tracker"`
	FixedVersion *ref   `json:"fixed_version"`
	Parent       *ref   `json:"parent"`
}

// FetchTasks returns every visible project, and the issues matching the
// filter beneath their target version where they have one.
func (a *Adapter) FetchTasks(ctx context.Context) ([]core.FetchedTask, error) {
	var projects []project
	err := a.pages(ctx, "/projects.json", nil, func(get func(any) error) (int, error) {
		var page struct {
			Projects []project `json:"projects"`
			Total    int       `json:"total_count"`
		}
		err := get(&page)
		projects = append(projects, page.Projects...)
		return page.Total, err
	})
	if err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	var issues []issue
	err = a.pages(ctx, "/issues.json", a.query, func(get func(any) error) (int, error) {
		var page struct {
			Issues []issue `json:"issues"`
			Total  int     `json:"total_count"`
		}
		err := get(&page)
		issues = append(issues, page.Issues...)
		return page.Total, err
	})
	if err != nil {
		return nil, fmt.Errorf("list issues: %w", err)
	}

	var out []core.FetchedTask
	for _, p := range projects {
		t := core.FetchedTask{NativeID: projectID(p.ID), Name: p.Name, Label: p.Identifier, NodeType: "project"}
		if p.Parent != nil {
			t.ParentNativeID = projectID(p.Parent.ID)
		}
		out = append(out, t)
	}
	listed := map[int]bool{}
	for _, is := range issues {
		listed[is.ID] = true
	}
	versions := map[int]bool{}
	for _, is := range issues {
		if v := is.FixedVersion; v != nil && !versions[v.ID] {
			versions[v.ID] = true
			out = append(out, core.FetchedTask{
				NativeID: "version:" + strconv.Itoa(v.ID), ParentNativeID: projectID(is.Project.ID),
				Name: v.Name, NodeType: "version",
			})
		}
	}
	for _, is := range issues {
		parent := projectID(is.Project.ID)
		switch {
		case is.Parent != nil && listed[is.Parent.ID]:
			parent = issueID(is.Parent.ID)
		case is.FixedVersion != nil:
			parent = "version:" + strconv.Itoa(is.FixedVersion.ID)
		}
		out = append(out, core.FetchedTask{
			NativeID: issueID(is.ID), ParentNativeID: parent, Name: is.Subject,
			Label: "#" + strconv.Itoa(is.ID), NodeType: strings.ToLower(is.Tracker.Name),
		})
	}
	return out, nil
}

// pages walks a paginated list endpoint. read decodes one page and reports
// the total count.
func (a *Adapter) pages(ctx context.Context, path string, query url.Values, read func(get func(any) error) (int, error)) error {
	for offset := 0; ; offset += pageSize {
		q := url.Values{}
		for k, v := range query {
			q[k] = v
		}
		q.Set("limit", strconv.Itoa(pageSize))
		q.Set("offset", strconv.Itoa(offset))
		total, err := read(func(out any) error {
			return a.c.Do(ctx, http.MethodGet, path+"?"+q.Encode(), nil, out)
		})
		if err != nil {
			return err
		}
		if offset+pageSize >= total {
			return nil
		}
	}
}

func projectID(id int) string { return "project:" + strconv.Itoa(id) }
func issueID(id int) string   { return "issue:" + strconv.Itoa(id) }

// Create logs a time entry on the task's issue, or on the project itself.
// Redmine cannot log time on a version.
func (a *Adapter) Create(ctx context.Context, task core.Task, u core.Unit) (string, error) {
	if task.Remote == nil {
		return "", fmt.Errorf("not a Redmine task")
	}
	kind, idStr, _ := strings.Cut(task.Remote.NativeID, ":")
	id, err := strconv.Atoi(idStr)
	if err != nil {
		return "", fmt.Errorf("malformed Redmine task id %q", task.Remote.NativeID)
	}
	entry := map[string]any{
		"spent_on": u.Start.In(a.loc).Format("2006-01-02"),
		"hours":    u.Duration.Hours(),
	}
	switch kind {
	case "issue":
		entry["issue_id"] = id
	case "project":
		entry["project_id"] = id
	default:
		return "", fmt.Errorf("Redmine records time on issues or projects, not on a %s: choose an issue", kind)
	}
	if u.Note != "" {
		entry["comments"] = u.Note
	}
	var resp struct {
		TimeEntry struct {
			ID int `json:"id"`
		} `json:"time_entry"`
	}
	if err := a.c.Do(ctx, http.MethodPost, "/time_entries.json", map[string]any{"time_entry": entry}, &resp); err != nil {
		return "", err
	}
	if resp.TimeEntry.ID == 0 {
		return "", fmt.Errorf("Redmine accepted the time entry but returned no id")
	}
	return strconv.Itoa(resp.TimeEntry.ID), nil
}
