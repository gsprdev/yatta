// Package toggl uploads time as Toggl Track time entries and fetches
// workspaces, clients, projects, and tasks as tasks.
package toggl

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote"
)

// DefaultBaseURL is Toggl Track's API host.
const DefaultBaseURL = "https://api.track.toggl.com"

const perPage = 200

type Adapter struct {
	c     remote.Client
	token string // for describing the credential, never for display in full
}

// New builds a Toggl adapter. baseURL may be empty for the public API.
func New(baseURL, apiToken string) *Adapter {
	if baseURL == "" {
		baseURL = DefaultBaseURL
	}
	return &Adapter{token: apiToken, c: remote.Client{BaseURL: baseURL, Auth: func(r *http.Request) {
		r.SetBasicAuth(apiToken, "api_token")
	}}}
}

func (a *Adapter) Integration() string { return "toggl" }

// Verify names the account the API token belongs to.
func (a *Adapter) Verify(ctx context.Context) (string, error) {
	var me struct {
		Fullname string `json:"fullname"`
		Email    string `json:"email"`
	}
	if err := a.c.Do(ctx, http.MethodGet, "/api/v9/me", nil, &me); err != nil {
		return "", fmt.Errorf("Toggl, API token %s: %w", remote.Fingerprint(a.token), err)
	}
	if me.Email == "" {
		return "", fmt.Errorf("Toggl did not identify the account")
	}
	return strings.TrimSpace("Toggl as "+me.Fullname) + " <" + me.Email + ">", nil
}

type named struct {
	ID   int64  `json:"id"`
	Name string `json:"name"`
}

type project struct {
	named
	ClientID *int64 `json:"client_id"`
}

type task struct {
	named
	ProjectID int64 `json:"project_id"`
}

// Native IDs carry the workspace (and project) they belong to, since Toggl
// addresses everything within a workspace.
func wsID(w int64) string         { return fmt.Sprintf("ws:%d", w) }
func clientID(w, c int64) string  { return fmt.Sprintf("client:%d:%d", w, c) }
func projectID(w, p int64) string { return fmt.Sprintf("project:%d:%d", w, p) }
func taskID(w, p, t int64) string { return fmt.Sprintf("task:%d:%d:%d", w, p, t) }

// FetchTasks returns each workspace with its clients, active projects, and
// active tasks. Workspaces without tasks (a paid Toggl feature) have none.
func (a *Adapter) FetchTasks(ctx context.Context) ([]core.FetchedTask, error) {
	var workspaces []named
	if err := a.c.Do(ctx, http.MethodGet, "/api/v9/me/workspaces", nil, &workspaces); err != nil {
		return nil, fmt.Errorf("list workspaces: %w", err)
	}
	var out []core.FetchedTask
	for _, ws := range workspaces {
		w := ws.ID
		out = append(out, core.FetchedTask{NativeID: wsID(w), Name: ws.Name, NodeType: "workspace"})

		var clients []named
		if err := a.c.Do(ctx, http.MethodGet, fmt.Sprintf("/api/v9/workspaces/%d/clients", w), nil, &clients); err != nil {
			return nil, fmt.Errorf("list clients: %w", err)
		}
		for _, c := range clients {
			out = append(out, core.FetchedTask{NativeID: clientID(w, c.ID), ParentNativeID: wsID(w), Name: c.Name, NodeType: "client"})
		}

		projects, err := pages[project](ctx, a, fmt.Sprintf("/api/v9/workspaces/%d/projects?active=true", w))
		if err != nil {
			return nil, fmt.Errorf("list projects: %w", err)
		}
		for _, p := range projects {
			parent := wsID(w)
			if p.ClientID != nil {
				parent = clientID(w, *p.ClientID)
			}
			out = append(out, core.FetchedTask{NativeID: projectID(w, p.ID), ParentNativeID: parent, Name: p.Name, NodeType: "project"})
		}

		tasks, err := pages[task](ctx, a, fmt.Sprintf("/api/v9/workspaces/%d/tasks?active=true", w))
		var herr *remote.Error
		if errors.As(err, &herr) && (herr.Status == http.StatusPaymentRequired || herr.Status == http.StatusForbidden) {
			tasks, err = nil, nil // tasks are not available on this plan
		}
		if err != nil {
			return nil, fmt.Errorf("list tasks: %w", err)
		}
		for _, t := range tasks {
			out = append(out, core.FetchedTask{NativeID: taskID(w, t.ProjectID, t.ID), ParentNativeID: projectID(w, t.ProjectID), Name: t.Name, NodeType: "task"})
		}
	}
	return out, nil
}

// pages walks a paginated list. Toggl answers either with a bare array or
// with {"data": [...]}, depending on the endpoint.
func pages[T any](ctx context.Context, a *Adapter, path string) ([]T, error) {
	var all []T
	for page := 1; ; page++ {
		var raw json.RawMessage
		if err := a.c.Do(ctx, http.MethodGet, fmt.Sprintf("%s&per_page=%d&page=%d", path, perPage, page), nil, &raw); err != nil {
			return nil, err
		}
		var items []T
		if err := json.Unmarshal(raw, &items); err != nil {
			var wrapped struct {
				Data []T `json:"data"`
			}
			if err := json.Unmarshal(raw, &wrapped); err != nil {
				return nil, fmt.Errorf("unexpected response from %s: %w", path, err)
			}
			items = wrapped.Data
		}
		all = append(all, items...)
		if len(items) < perPage {
			return all, nil
		}
	}
}

// Create adds a time entry in the task's workspace, on its project and task
// where it has them.
func (a *Adapter) Create(ctx context.Context, t core.Task, u core.Unit) (string, error) {
	if t.Remote == nil {
		return "", fmt.Errorf("not a Toggl task")
	}
	parts := strings.Split(t.Remote.NativeID, ":")
	ids := make([]int64, len(parts)-1)
	for i, p := range parts[1:] {
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil {
			return "", fmt.Errorf("malformed Toggl task id %q", t.Remote.NativeID)
		}
		ids[i] = n
	}
	entry := map[string]any{
		"created_with": "yatta",
		"start":        u.Start.UTC().Format(time.RFC3339),
		"duration":     int64(u.Duration.Seconds()),
		"description":  u.Note,
	}
	switch {
	case parts[0] == "ws" && len(ids) == 1:
	case parts[0] == "project" && len(ids) == 2:
		entry["project_id"] = ids[1]
	case parts[0] == "task" && len(ids) == 3:
		entry["project_id"], entry["task_id"] = ids[1], ids[2]
	case parts[0] == "client":
		return "", fmt.Errorf("Toggl records time on projects, not clients: choose a project")
	default:
		return "", fmt.Errorf("malformed Toggl task id %q", t.Remote.NativeID)
	}
	entry["workspace_id"] = ids[0]
	var resp struct {
		ID int64 `json:"id"`
	}
	if err := a.c.Do(ctx, http.MethodPost, fmt.Sprintf("/api/v9/workspaces/%d/time_entries", ids[0]), entry, &resp); err != nil {
		return "", err
	}
	if resp.ID == 0 {
		return "", fmt.Errorf("Toggl accepted the time entry but returned no id")
	}
	return strconv.FormatInt(resp.ID, 10), nil
}
