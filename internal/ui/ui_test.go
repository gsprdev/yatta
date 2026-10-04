package ui

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/remote/jira"
	"github.com/gsprdev/yatta/internal/store"
)

// driver feeds messages to the model and runs the commands it returns,
// feeding their results back. Commands that do not finish promptly (timer
// ticks, cursor blinks) are dropped. Like the real program, it renders the
// first frame before any message and another after every update.
type driver struct {
	t     *testing.T
	m     tea.Model
	drawn bool
}

func (d *driver) send(msg tea.Msg) {
	d.t.Helper()
	if !d.drawn {
		d.m.View()
		d.drawn = true
	}
	queue := []tea.Msg{msg}
	for steps := 0; len(queue) > 0; steps++ {
		if steps > 500 {
			d.t.Fatal("message loop did not settle")
		}
		msg, queue = queue[0], queue[1:]
		if batch, ok := msg.(tea.BatchMsg); ok {
			for _, c := range batch {
				queue = append(queue, run(c)...)
			}
			continue
		}
		// tea.Sequence's message type is unexported; it is a []tea.Cmd.
		if v := reflect.ValueOf(msg); v.Kind() == reflect.Slice && v.Type().Elem() == reflect.TypeOf(tea.Cmd(nil)) {
			for i := 0; i < v.Len(); i++ {
				if c, _ := v.Index(i).Interface().(tea.Cmd); c != nil {
					d.send(c())
				}
			}
			continue
		}
		var cmd tea.Cmd
		d.m, cmd = d.m.Update(msg)
		d.m.View()
		queue = append(queue, run(cmd)...)
	}
}

func run(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	ch := make(chan tea.Msg, 1)
	go func() { ch <- cmd() }()
	select {
	case msg := <-ch:
		if msg == nil {
			return nil
		}
		return []tea.Msg{msg}
	case <-time.After(200 * time.Millisecond):
		return nil
	}
}

func (d *driver) keys(ks ...string) {
	d.t.Helper()
	for _, k := range ks {
		switch k {
		case "enter":
			d.send(tea.KeyMsg{Type: tea.KeyEnter})
		case "esc":
			d.send(tea.KeyMsg{Type: tea.KeyEsc})
		case "ctrl+c":
			d.send(tea.KeyMsg{Type: tea.KeyCtrlC})
		default:
			d.send(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)})
		}
	}
}

func (d *driver) model() Model { return d.m.(Model) }

// fakeJira accepts worklogs, except on issues listed in reject.
type fakeJira struct {
	mu       sync.Mutex
	worklogs []map[string]any
	paths    []string
	reject   map[string]string
}

func (f *fakeJira) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.paths = append(f.paths, r.URL.Path)
	for issue, msg := range f.reject {
		if strings.Contains(r.URL.Path, "/issue/"+issue+"/") {
			w.WriteHeader(400)
			io.WriteString(w, `{"errorMessages":["`+msg+`"]}`)
			return
		}
	}
	var body map[string]any
	b, _ := io.ReadAll(r.Body)
	json.Unmarshal(b, &body)
	f.worklogs = append(f.worklogs, body)
	w.WriteHeader(201)
	io.WriteString(w, `{"id":"`+string(rune('0'+len(f.worklogs)))+`"}`)
}

func TestUploadEndToEnd(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	loc := time.UTC
	day := time.Date(2026, 3, 2, 9, 0, 0, 0, loc)

	fetched := []core.FetchedTask{
		{NativeID: "1", Name: "Login", Label: "PROJ-1"},
		{NativeID: "2", Name: "Tiny", Label: "PROJ-2"},
		{NativeID: "3", Name: "Gone", Label: "PROJ-3"},
		{NativeID: "4", Name: "Locked", Label: "PROJ-4"},
	}
	must(t, st.ReconcileRemoteTasks("jira", fetched))
	ids := map[string]string{}
	tasks, _ := st.Tasks()
	for _, task := range tasks {
		ids[task.Remote.Label] = task.ID
	}
	add := func(label string, at time.Time, d time.Duration, note string) core.TimeEntry {
		e, err := st.SaveEntry(core.TimeEntry{TaskID: ids[label], Start: at, Duration: d, Note: note})
		must(t, err)
		return e
	}
	a := add("PROJ-1", day, 10*time.Minute, " Fixed login ")
	b := add("PROJ-1", day.Add(2*time.Hour), 10*time.Minute, "Wrote tests.")
	tiny := add("PROJ-2", day, 5*time.Minute, "")
	gone := add("PROJ-3", day, time.Hour, "")
	locked := add("PROJ-4", day, time.Hour, "")
	unassigned := add("", day, time.Hour, "")
	must(t, st.ReconcileRemoteTasks("jira", append(fetched[:2:2], fetched[3]))) // PROJ-3 departs
	must(t, st.SetPolicy(core.Policy{Increment: 15 * time.Minute, Minimum: 15 * time.Minute,
		BelowMin: core.Exclude, Aggregate: core.AggTaskDay}))

	fake := &fakeJira{reject: map[string]string{"4": "Issue is closed"}}
	srv := httptest.NewServer(fake)
	defer srv.Close()
	d := &driver{t: t, m: New(st, nil, nil, loc)}
	d.send(load(st)())
	d.send(connectedMsg{ad: jira.New(jira.Site{URL: srv.URL, Cloud: true}, "me@example.com", "tok", "")})

	d.keys("u", "enter")
	if got := d.model().upload.stage; got != stageConfirm {
		t.Fatalf("stage after planning = %v; want confirm", got)
	}
	plan := d.model().upload.plan
	if len(plan.Upload) != 3 || len(plan.Excluded) != 1 {
		t.Fatalf("plan = %d uploads, %d excluded; want 3 and 1", len(plan.Upload), len(plan.Excluded))
	}
	d.keys("y")
	if got := d.model().upload.stage; got != stageDone {
		t.Fatalf("stage after upload = %v; want done", got)
	}

	// Only PROJ-1 (aggregated) reached Jira successfully; PROJ-4 was tried
	// and rejected; the departed PROJ-3 was never sent.
	if len(fake.worklogs) != 1 {
		t.Fatalf("Jira received %d worklogs; want 1", len(fake.worklogs))
	}
	w := fake.worklogs[0]
	if w["timeSpentSeconds"] != float64(15*60) {
		t.Errorf("PROJ-1 worklog = %v s; want 900 (10m + 10m summed, then rounded)", w["timeSpentSeconds"])
	}
	comment, _ := json.Marshal(w["comment"])
	if !strings.Contains(string(comment), "Fixed login. Wrote tests.") {
		t.Errorf("comment = %s; want the combined note", comment)
	}
	for _, p := range fake.paths {
		if strings.Contains(p, "/issue/3/") {
			t.Error("a worklog was sent for the departed task")
		}
	}

	phase := func(e core.TimeEntry) (core.Phase, string) {
		got, err := st.Entry(e.ID)
		must(t, err)
		if got.Upload == nil {
			return "", ""
		}
		return got.Upload.Phase, got.Upload.Err
	}
	for _, c := range []struct {
		name  string
		e     core.TimeEntry
		phase core.Phase
		err   string
	}{
		{"aggregated a", a, core.Uploaded, ""},
		{"aggregated b", b, core.Uploaded, ""},
		{"below minimum", tiny, core.Excluded, ""},
		{"departed", gone, core.Failed, "departed"},
		{"rejected", locked, core.Failed, "Issue is closed"},
		{"unassigned", unassigned, "", ""},
	} {
		p, e := phase(c.e)
		if p != c.phase || !strings.Contains(e, c.err) {
			t.Errorf("%s: phase %q err %q; want %q containing %q", c.name, p, e, c.phase, c.err)
		}
	}
	if c := d.model().data.counts; c.Failed != 2 || c.Pending != 0 || c.Departed != 1 || c.Unassigned != 1 {
		t.Errorf("counts = %+v", c)
	}

	// A second upload sends nothing new for the settled entries: only the
	// two failed units are planned again.
	d.keys("enter", "u", "enter")
	if n := len(d.model().upload.plan.Upload); n != 2 {
		t.Errorf("second plan has %d uploads; want the 2 failed units", n)
	}
}

func TestUploadBlocksInput(t *testing.T) {
	m := Model{mode: modeUpload, upload: uploadModel{stage: stageRunning}}
	next, cmd := m.Update(tea.KeyMsg{Type: tea.KeyCtrlC})
	if cmd != nil || next.(Model).mode != modeUpload {
		t.Error("ctrl+c interrupted a running upload")
	}
	next, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if next.(Model).mode != modeUpload {
		t.Error("esc left a running upload")
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestEditRunningTimer(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 3, 2, 9, 0, 30, 0, time.UTC)
	now := started.Add(10 * time.Minute)
	m := New(st, nil, nil, time.UTC)
	m.now = func() time.Time { return now }
	d := &driver{t: t, m: m}
	must(t, func() error { _, err := st.StartTimer("", started); return err }())
	d.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	d.send(load(st)())

	key := func(t tea.KeyType) { d.send(tea.KeyMsg{Type: t}) }
	save := func() { // tab down to the Save row and press enter
		for i := 0; d.model().editor.focus != fieldSave; i++ {
			if i > fieldCount {
				t.Fatal("Save row is unreachable")
			}
			key(tea.KeyTab)
		}
		key(tea.KeyEnter)
	}
	timer := func() core.ActiveTimer {
		got, err := st.Timer()
		must(t, err)
		return *got
	}

	// A note alone leaves the start exactly as it was, seconds included.
	d.keys("E")
	if d.model().mode != modeEditor || !d.model().editor.timer {
		t.Fatalf("E did not open the timer editor: mode %v", d.model().mode)
	}
	key(tea.KeyTab) // start -> end is hidden -> note
	d.keys("standup")
	save()
	if got := timer(); !got.Start.Equal(started) || got.Note != "standup" {
		t.Fatalf("after note-only edit timer = %+v; want start %v and note", got, started)
	}
	if !strings.Contains(d.model().statusBar(), "standup") {
		t.Error("status bar does not show the timer's note")
	}

	// A start in the future is refused and the editor stays open.
	d.keys("E")
	key(tea.KeyCtrlU)
	d.keys("23:00")
	save()
	if d.model().mode != modeEditor || d.model().editor.err == "" {
		t.Fatal("a future start was accepted")
	}
	if got := timer(); !got.Start.Equal(started) {
		t.Fatalf("start moved to %v despite the error", got.Start)
	}

	// Backdating takes effect, and the entry keeps the note when stopped.
	key(tea.KeyCtrlU)
	d.keys("08:55")
	save()
	want := time.Date(2026, 3, 2, 8, 55, 0, 0, time.UTC)
	if got := timer(); !got.Start.Equal(want) || got.Note != "standup" {
		t.Fatalf("after backdating timer = %+v; want start %v", got, want)
	}
	d.keys("x")
	entries, err := st.Entries(time.Time{}, time.Time{})
	must(t, err)
	if len(entries) != 1 || !entries[0].Start.Equal(want) || entries[0].Duration != 15*time.Minute+30*time.Second || entries[0].Note != "standup" {
		t.Fatalf("entries = %+v", entries)
	}

	// With no timer there is nothing to edit.
	d.keys("E")
	if d.model().mode != modeEntries {
		t.Error("E opened an editor with no timer running")
	}
}

func TestEditorSaveRow(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	now := time.Date(2026, 3, 2, 12, 0, 0, 0, time.UTC)
	m := New(st, nil, nil, time.UTC)
	m.now = func() time.Time { return now }
	d := &driver{t: t, m: m}
	d.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	d.send(load(st)())

	// Enter walks the fields; on the Save row it saves. No ctrl chords needed.
	d.keys("a")
	d.send(tea.KeyMsg{Type: tea.KeyDown}) // date -> start
	d.send(tea.KeyMsg{Type: tea.KeyDown}) // start -> end
	d.send(tea.KeyMsg{Type: tea.KeyDown}) // end -> note
	d.keys("by hand")
	d.keys("enter") // note -> task
	d.keys("enter") // opens the picker
	if d.model().mode != modePicker {
		t.Fatalf("enter on the task row did not open the picker: mode %v", d.model().mode)
	}
	d.keys("enter")                       // (no task); back on the task row
	d.send(tea.KeyMsg{Type: tea.KeyDown}) // task -> Save
	if d.model().editor.focus != fieldSave {
		t.Fatalf("focus = %d; want the Save row", d.model().editor.focus)
	}
	d.keys("enter")
	entries, err := st.Entries(time.Time{}, time.Time{})
	must(t, err)
	if d.model().mode != modeEntries || len(entries) != 1 || entries[0].Note != "by hand" || entries[0].Duration != time.Hour {
		t.Fatalf("mode %v, entries %+v", d.model().mode, entries)
	}

	// An uploaded entry is read-only and has no Save row to reach.
	ed := newEditor(core.TimeEntry{Start: now, Duration: time.Hour, Upload: &core.UploadState{RecordID: "r"}}, data{}, time.UTC)
	ed.move(-1)
	if ed.focus == fieldSave || strings.Contains(ed.view(data{}), "Save") {
		t.Error("a locked entry offers Save")
	}
}

func TestEditorShortcuts(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	started := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	now := started.Add(10 * time.Minute)
	m := New(st, nil, nil, time.UTC)
	m.now = func() time.Time { return now }
	d := &driver{t: t, m: m}
	d.send(tea.WindowSizeMsg{Width: 120, Height: 30})
	d.send(load(st)())
	key := func(t tea.KeyType) { d.send(tea.KeyMsg{Type: t}) }

	// ctrl+s saves an entry straight from a text field.
	d.keys("a")
	for i := 0; i < 3; i++ { // date -> start -> end -> note
		key(tea.KeyDown)
	}
	d.keys("by hand")
	key(tea.KeyCtrlS)
	entries, err := st.Entries(time.Time{}, time.Time{})
	must(t, err)
	if d.model().mode != modeEntries || len(entries) != 1 || entries[0].Note != "by hand" {
		t.Fatalf("ctrl+s on an entry: mode %v, entries %+v", d.model().mode, entries)
	}

	// ctrl+t opens the task picker from a text field, and ctrl+s saves the timer.
	must(t, func() error { _, err := st.StartTimer("", started); return err }())
	d.send(load(st)())
	d.keys("E")
	key(tea.KeyTab) // start -> note
	d.keys("standup")
	key(tea.KeyCtrlT)
	if d.model().mode != modePicker {
		t.Fatalf("ctrl+t: mode %v; want the picker", d.model().mode)
	}
	key(tea.KeyEsc)
	if d.model().mode != modeEditor || d.model().editor.value(fieldNote) != "standup" {
		t.Fatalf("returning from the picker lost the form: mode %v", d.model().mode)
	}
	key(tea.KeyCtrlS)
	if got, err := st.Timer(); err != nil || got.Note != "standup" || d.model().mode != modeEntries {
		t.Fatalf("ctrl+s on the timer: %+v, %v, mode %v", got, err, d.model().mode)
	}

	// A validation error from ctrl+s returns focus to the field at fault.
	d.keys("E")
	key(tea.KeyCtrlU)
	d.keys("23:00")
	key(tea.KeyCtrlS)
	if ed := d.model().editor; d.model().mode != modeEditor || ed.err == "" || ed.focus != fieldStart {
		t.Fatalf("future start: mode %v, err %q, focus %d", d.model().mode, ed.err, ed.focus)
	}
}
