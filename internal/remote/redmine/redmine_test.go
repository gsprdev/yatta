package redmine

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

type call struct {
	method, url, key string
	body             map[string]any
}

func server(t *testing.T, routes map[string]string) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, url: r.URL.String(), key: r.Header.Get("X-Redmine-API-Key")}
		if b, _ := io.ReadAll(r.Body); len(b) > 0 {
			json.Unmarshal(b, &c.body)
		}
		calls = append(calls, c)
		for prefix, resp := range routes {
			if strings.HasPrefix(r.Method+" "+r.URL.String(), prefix) {
				status, body, _ := strings.Cut(resp, " ")
				code := map[string]int{"200": 200, "201": 201, "422": 422}[status]
				w.WriteHeader(code)
				io.WriteString(w, body)
				return
			}
		}
		t.Errorf("unexpected request %s %s", r.Method, r.URL)
		w.WriteHeader(404)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

func TestFetch(t *testing.T) {
	srv, calls := server(t, map[string]string{
		"GET /projects.json": `200 {"projects":[{"id":1,"name":"Web","identifier":"web"},
			{"id":2,"name":"API","identifier":"api","parent":{"id":1,"name":"Web"}}],"total_count":2}`,
		"GET /issues.json": `200 {"issues":[
			{"id":10,"subject":"Login","project":{"id":2,"name":"API"},"tracker":{"id":1,"name":"Bug"},"fixed_version":{"id":5,"name":"1.0"}},
			{"id":11,"subject":"Sub","project":{"id":2,"name":"API"},"tracker":{"id":2,"name":"Task"},"parent":{"id":10}},
			{"id":12,"subject":"Loose","project":{"id":1,"name":"Web"},"tracker":{"id":2,"name":"Task"},"parent":{"id":99}}
			],"total_count":3}`,
	})
	a, err := New(srv.URL, "key", "", time.UTC)
	if err != nil {
		t.Fatal(err)
	}
	tasks, err := a.FetchTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]core.FetchedTask{}
	for _, task := range tasks {
		got[task.NativeID] = task
	}
	want := map[string]string{ // native ID -> parent
		"project:1": "", "project:2": "project:1", "version:5": "project:2",
		"issue:10": "version:5", "issue:11": "issue:10", "issue:12": "project:1",
	}
	if len(got) != len(want) {
		t.Fatalf("got %d tasks; want %d: %+v", len(got), len(want), tasks)
	}
	for id, parent := range want {
		if got[id].ParentNativeID != parent {
			t.Errorf("%s parent = %q; want %q", id, got[id].ParentNativeID, parent)
		}
	}
	if got["issue:10"].Label != "#10" || got["issue:10"].NodeType != "bug" {
		t.Errorf("issue = %+v", got["issue:10"])
	}
	for _, c := range *calls {
		if c.key != "key" {
			t.Errorf("%s sent without the API key", c.url)
		}
	}
	if !strings.Contains((*calls)[1].url, "assigned_to_id=me") || !strings.Contains((*calls)[1].url, "status_id=open") {
		t.Errorf("issues query = %s; want the default filter", (*calls)[1].url)
	}
}

func TestPagination(t *testing.T) {
	var offsets []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/projects.json" {
			offsets = append(offsets, r.URL.Query().Get("offset"))
			io.WriteString(w, `{"projects":[],"total_count":250}`)
			return
		}
		io.WriteString(w, `{"issues":[],"total_count":0}`)
	}))
	defer srv.Close()
	a, _ := New(srv.URL, "k", "", time.UTC)
	if _, err := a.FetchTasks(context.Background()); err != nil {
		t.Fatal(err)
	}
	if strings.Join(offsets, ",") != "0,100,200" {
		t.Errorf("offsets = %v; want 0,100,200", offsets)
	}
}

func TestCreate(t *testing.T) {
	ny, _ := time.LoadLocation("America/New_York")
	srv, calls := server(t, map[string]string{"POST /time_entries.json": `201 {"time_entry":{"id":77}}`})
	a, _ := New(srv.URL, "key", "", ny)
	task := core.Task{Remote: &core.RemoteTask{Integration: "redmine", NativeID: "issue:10"}}
	// 01:00 UTC on the 3rd is the evening of the 2nd in New York.
	u := core.Unit{Start: time.Date(2026, 3, 3, 1, 0, 0, 0, time.UTC), Duration: 45 * time.Minute, Note: "Done."}
	id, err := a.Create(context.Background(), task, u)
	if err != nil || id != "77" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	te := (*calls)[0].body["time_entry"].(map[string]any)
	if te["issue_id"] != float64(10) || te["hours"] != 0.75 || te["spent_on"] != "2026-03-02" || te["comments"] != "Done." {
		t.Errorf("time_entry = %+v", te)
	}
}

func TestCreateOnVersionRefused(t *testing.T) {
	a, _ := New("http://unused", "k", "", time.UTC)
	task := core.Task{Remote: &core.RemoteTask{Integration: "redmine", NativeID: "version:5"}}
	if _, err := a.Create(context.Background(), task, core.Unit{Duration: time.Hour}); err == nil {
		t.Error("time entry on a version was attempted")
	}
}

func TestCreateError(t *testing.T) {
	srv, _ := server(t, map[string]string{"POST /time_entries.json": `422 {"errors":["Activity cannot be blank"]}`})
	a, _ := New(srv.URL, "k", "", time.UTC)
	task := core.Task{Remote: &core.RemoteTask{Integration: "redmine", NativeID: "project:1"}}
	_, err := a.Create(context.Background(), task, core.Unit{Duration: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "Activity cannot be blank") {
		t.Errorf("error = %v; want Redmine's message", err)
	}
}
