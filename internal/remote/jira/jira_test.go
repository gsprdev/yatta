package jira

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
	method, path, auth string
	body               map[string]any
}

// server replies to each request with the next canned response and records
// what it received.
func server(t *testing.T, responses ...string) (*httptest.Server, *[]call) {
	t.Helper()
	var calls []call
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		c := call{method: r.Method, path: r.URL.Path, auth: r.Header.Get("Authorization")}
		b, _ := io.ReadAll(r.Body)
		if len(b) > 0 {
			if err := json.Unmarshal(b, &c.body); err != nil {
				t.Errorf("request body is not JSON: %s", b)
			}
		}
		i := len(calls)
		calls = append(calls, c)
		if i >= len(responses) {
			t.Errorf("unexpected request %d: %s %s", i, r.Method, r.URL.Path)
			w.WriteHeader(500)
			return
		}
		status, resp, _ := strings.Cut(responses[i], " ")
		code := 200
		switch status {
		case "201":
			code = 201
		case "400":
			code = 400
		}
		w.WriteHeader(code)
		io.WriteString(w, resp)
	}))
	t.Cleanup(srv.Close)
	return srv, &calls
}

const page1 = `200 {"issues":[
 {"id":"101","key":"PROJ-1","fields":{"summary":"Login bug","issuetype":{"name":"Story"},
  "project":{"id":"10","key":"PROJ","name":"Project"},
  "parent":{"id":"100","key":"PROJ-0","fields":{"summary":"Auth epic","issuetype":{"name":"Epic"}}}}}
 ],"nextPageToken":"p2","isLast":false}`

const page2 = `200 {"issues":[
 {"id":"102","key":"PROJ-2","fields":{"summary":"Docs","issuetype":{"name":"Task"},
  "project":{"id":"10","key":"PROJ","name":"Project"}}}
 ],"isLast":true}`

func TestFetchCloud(t *testing.T) {
	srv, calls := server(t, page1, page2)
	tasks, err := New(srv.URL, "me@example.com", "tok", "").FetchTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]core.FetchedTask{}
	for _, task := range tasks {
		got[task.NativeID] = task
	}
	if len(tasks) != 4 {
		t.Fatalf("got %d tasks; want project, epic, and two issues: %+v", len(tasks), tasks)
	}
	if p := got["project:10"]; p.Label != "PROJ" || p.ParentNativeID != "" {
		t.Errorf("project = %+v", p)
	}
	if e := got["100"]; e.Label != "PROJ-0" || e.NodeType != "epic" || e.ParentNativeID != "project:10" {
		t.Errorf("epic = %+v", e)
	}
	if s := got["101"]; s.Label != "PROJ-1" || s.Name != "Login bug" || s.ParentNativeID != "100" {
		t.Errorf("story = %+v", s)
	}
	if s := got["102"]; s.ParentNativeID != "project:10" {
		t.Errorf("issue without a parent should sit under its project: %+v", s)
	}

	c := *calls
	if c[0].path != "/rest/api/3/search/jql" || c[0].body["jql"] != DefaultQuery {
		t.Errorf("first call = %+v", c[0])
	}
	if c[1].body["nextPageToken"] != "p2" {
		t.Errorf("second call did not pass the page token: %+v", c[1].body)
	}
	if !strings.HasPrefix(c[0].auth, "Basic ") {
		t.Errorf("cloud auth = %q; want basic", c[0].auth)
	}
}

func TestFetchDataCenter(t *testing.T) {
	srv, calls := server(t,
		`200 {"issues":[{"id":"1","key":"A-1","fields":{"summary":"x","issuetype":{"name":"Task"},"project":{"id":"9","key":"A","name":"A"}}}],"total":2}`,
		`200 {"issues":[{"id":"2","key":"A-2","fields":{"summary":"y","issuetype":{"name":"Task"},"project":{"id":"9","key":"A","name":"A"}}}],"total":2}`)
	tasks, err := New(srv.URL, "", "pat", "project = A").FetchTasks(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(tasks) != 3 {
		t.Fatalf("got %d tasks; want 3", len(tasks))
	}
	c := *calls
	if c[0].path != "/rest/api/2/search" || c[0].auth != "Bearer pat" || c[0].body["jql"] != "project = A" {
		t.Errorf("first call = %+v", c[0])
	}
	if c[1].body["startAt"] != float64(1) {
		t.Errorf("second page startAt = %v; want 1", c[1].body["startAt"])
	}
}

func issueTask() core.Task {
	return core.Task{ID: "t", Remote: &core.RemoteTask{Integration: "jira", NativeID: "101", Label: "PROJ-1", NodeType: "story"}}
}

func TestCreateCloud(t *testing.T) {
	srv, calls := server(t, `201 {"id":"5001"}`)
	u := core.Unit{Start: time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC), Duration: 30 * time.Minute, Note: "Fixed it."}
	id, err := New(srv.URL, "me@example.com", "tok", "").Create(context.Background(), issueTask(), u)
	if err != nil || id != "5001" {
		t.Fatalf("Create = %q, %v", id, err)
	}
	c := (*calls)[0]
	if c.method != "POST" || c.path != "/rest/api/3/issue/101/worklog" {
		t.Errorf("call = %s %s", c.method, c.path)
	}
	if c.body["started"] != "2026-03-02T14:00:00.000+0000" || c.body["timeSpentSeconds"] != float64(1800) {
		t.Errorf("body = %+v", c.body)
	}
	comment, _ := json.Marshal(c.body["comment"])
	if !strings.Contains(string(comment), `"type":"doc"`) || !strings.Contains(string(comment), "Fixed it.") {
		t.Errorf("comment is not ADF: %s", comment)
	}
}

func TestCreateDataCenterPlainComment(t *testing.T) {
	srv, calls := server(t, `201 {"id":"7"}`)
	u := core.Unit{Start: time.Unix(0, 0), Duration: time.Hour, Note: "n"}
	if _, err := New(srv.URL, "", "pat", "").Create(context.Background(), issueTask(), u); err != nil {
		t.Fatal(err)
	}
	if c := (*calls)[0]; c.path != "/rest/api/2/issue/101/worklog" || c.body["comment"] != "n" {
		t.Errorf("call = %+v", c)
	}
}

func TestCreateErrors(t *testing.T) {
	srv, _ := server(t, `400 {"errorMessages":[],"errors":{"comment":"The comment is too long."}}`)
	_, err := New(srv.URL, "e", "t", "").Create(context.Background(), issueTask(), core.Unit{Duration: time.Hour})
	if err == nil || !strings.Contains(err.Error(), "comment is too long") {
		t.Errorf("error = %v; want Jira's message", err)
	}
	project := core.Task{Remote: &core.RemoteTask{Integration: "jira", NativeID: "project:10", NodeType: "project"}}
	if _, err := New("http://unused", "e", "t", "").Create(context.Background(), project, core.Unit{}); err == nil {
		t.Error("worklog on a project was attempted")
	}
}
