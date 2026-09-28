package jira

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/gsprdev/yatta/internal/core"
)

// TestLive runs against a real Jira Cloud site, and only when configured:
//
//	YATTA_JIRA_URL    e.g. https://gsprdev.atlassian.net
//	YATTA_JIRA_EMAIL  the account email
//	YATTA_JIRA_TOKEN  an API token for that account: scoped (read:jira-user,
//	                  read:jira-work, write:jira-work) to test the gateway, or
//	                  classic to test the fallback to the site
//
// It expects issues labelled yatta-test: an epic with a story, and a subtask
// under that story. It adds a 15-minute worklog to that story, which yatta
// never removes, so run it only against a development site.
func TestLive(t *testing.T) {
	url, email, token := os.Getenv("YATTA_JIRA_URL"), os.Getenv("YATTA_JIRA_EMAIL"), os.Getenv("YATTA_JIRA_TOKEN")
	if url == "" || email == "" || token == "" {
		t.Skip("set YATTA_JIRA_URL, YATTA_JIRA_EMAIL, and YATTA_JIRA_TOKEN to run against a live Jira")
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	site, err := Probe(ctx, url)
	if err != nil {
		t.Fatalf("Probe: %v", err)
	}
	t.Logf("site: %+v", site)
	a := New(site, email, token, "labels = yatta-test ORDER BY key")
	who, err := a.Verify(ctx)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	t.Log(who)

	tasks, err := a.FetchTasks(ctx)
	if err != nil {
		t.Fatalf("FetchTasks: %v", err)
	}
	byNative := map[string]core.FetchedTask{}
	for _, task := range tasks {
		byNative[task.NativeID] = task
		t.Logf("%-14s %-8s %-8s parent=%-14s %s", task.NativeID, task.Label, task.NodeType, task.ParentNativeID, task.Name)
	}
	var subtask, story core.FetchedTask
	for _, task := range tasks {
		if task.NodeType == "subtask" {
			subtask = task
		}
	}
	if subtask.NativeID == "" {
		t.Fatal("no subtask among the fetched issues")
	}
	story = byNative[subtask.ParentNativeID]
	if story.NodeType != "story" {
		t.Errorf("subtask's parent = %+v; want a story", story)
	}
	if epic := byNative[story.ParentNativeID]; epic.NodeType != "epic" {
		t.Errorf("story's parent = %+v; want an epic", epic)
	}

	task := core.Task{Name: story.Name, Remote: &core.RemoteTask{
		Integration: "jira", NativeID: story.NativeID, Label: story.Label, NodeType: story.NodeType,
	}}
	u := core.Unit{
		Start:    time.Now().UTC().Truncate(time.Minute).Add(-time.Hour),
		Duration: 15 * time.Minute,
		Note:     "yatta live test. Safe to delete.",
	}
	id, err := a.Create(ctx, task, u)
	if err != nil {
		t.Fatalf("Create on %s: %v", story.Label, err)
	}
	t.Logf("worklog %s created on %s", id, story.Label)
}
