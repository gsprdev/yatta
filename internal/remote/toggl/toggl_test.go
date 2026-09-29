package toggl

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gsprdev/yatta/internal/core"
)

func server(t *testing.T, routes map[string]string) (*httptest.Server, *[]map[string]any) {
	t.Helper()
	var bodies []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if user, pass, ok := r.BasicAuth(); !ok || user != "tok" || pass != "api_token" {
			t.Errorf("%s: bad auth", r.URL)
		}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			var m map[string]any
			json.Unmarshal(b, &m)
			bodies = append(bodies, m)
		}
		for prefix, resp := range routes {
			if strings.HasPrefix(r.Method+" "+r.URL.RequestURI(), prefix) {
				status, body, _ := strings.Cut(resp, " ")
				code := map[string]int{"200": 200, "402": 402, "400": 400}[status]
				w.WriteHeader(code)
				io.WriteString(w, body)
				return
			}
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL.RequestURI())
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return srv, &bodies
}

func TestFetch(t *testing.T) {
	srv, _ := server(t, map[string]string{
		"GET /api/v9/me/workspaces":          `200 [{"id":1,"name":"Acme"}]`,
		"GET /api/v9/workspaces/1/clients":   `200 [{"id":7,"name":"Client A"}]`,
		"GET /api/v9/workspaces/1/projects?": `200 [{"id":20,"name":"Site","client_id":7},{"id":21,"name":"Internal","client_id":null}]`,
		"GET /api/v9/workspaces/1/tasks?":    `200 {"data":[{"id":300,"name":"Design","project_id":20}]}`,
	})
	tasks, err := New(srv.URL, "tok").FetchTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{ // native ID -> parent
		"ws:1": "", "client:1:7": "ws:1", "project:1:20": "client:1:7",
		"project:1:21": "ws:1", "task:1:20:300": "project:1:20",
	}
	if len(tasks) != len(want) {
		t.Fatalf("got %d tasks; want %d: %+v", len(tasks), len(want), tasks)
	}
	for _, task := range tasks {
		if p, ok := want[task.NativeID]; !ok || p != task.ParentNativeID {
			t.Errorf("%s parent = %q; want %q", task.NativeID, task.ParentNativeID, p)
		}
	}
}

func TestFetchWithoutTasksFeature(t *testing.T) {
	srv, _ := server(t, map[string]string{
		"GET /api/v9/me/workspaces":          `200 [{"id":1,"name":"Acme"}]`,
		"GET /api/v9/workspaces/1/clients":   `200 null`,
		"GET /api/v9/workspaces/1/projects?": `200 [{"id":20,"name":"Site"}]`,
		"GET /api/v9/workspaces/1/tasks?":    `402 "Tasks are a paid feature"`,
	})
	tasks, err := New(srv.URL, "tok").FetchTasks(context.Background())
	if err != nil || len(tasks) != 2 {
		t.Fatalf("got %d tasks, %v; want the workspace and its project", len(tasks), err)
	}
}

func TestCreate(t *testing.T) {
	srv, bodies := server(t, map[string]string{"POST /api/v9/workspaces/1/time_entries": `200 {"id":9001}`})
	task := core.Task{Remote: &core.RemoteTask{Integration: "toggl", NativeID: "task:1:20:300"}}
	u := core.Unit{Start: time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC), Duration: 90 * time.Minute, Note: "Mockups."}
	id, err := New(srv.URL, "tok").Create(context.Background(), task, u)
	if err != nil || id != "9001" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	b := (*bodies)[0]
	if b["workspace_id"] != float64(1) || b["project_id"] != float64(20) || b["task_id"] != float64(300) ||
		b["start"] != "2026-03-02T14:00:00Z" || b["duration"] != float64(5400) || b["description"] != "Mockups." ||
		b["created_with"] != "yatta" {
		t.Errorf("body = %+v", b)
	}
}

func TestCreateOnClientRefused(t *testing.T) {
	task := core.Task{Remote: &core.RemoteTask{Integration: "toggl", NativeID: "client:1:7"}}
	if _, err := New("http://unused", "tok").Create(context.Background(), task, core.Unit{Duration: time.Hour}); err == nil {
		t.Error("time entry on a client was attempted")
	}
}

func TestVerify(t *testing.T) {
	srv, _ := server(t, map[string]string{
		"GET /api/v9/me": `200 {"id":1,"fullname":"Jane Doe","email":"jane@example.com"}`,
	})
	got, err := New(srv.URL, "tok").Verify(context.Background())
	if err != nil || got != "Toggl as Jane Doe <jane@example.com>" {
		t.Errorf("Verify = %q, %v", got, err)
	}
}
