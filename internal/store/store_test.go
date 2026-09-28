package store

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/gsprdev/yatta/internal/core"
)

var t0 = time.Date(2026, 3, 2, 14, 0, 0, 0, time.UTC)

func open(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	s.now = func() time.Time { return t0 }
	t.Cleanup(func() { s.Close() })
	return s
}

func must[T any](t *testing.T) func(T, error) T {
	return func(v T, err error) T {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
		return v
	}
}

// remoteTask reconciles a single Jira issue into the store and returns it.
func remoteTask(t *testing.T, s *Store, native string) core.Task {
	t.Helper()
	if err := s.ReconcileRemoteTasks("jira", []core.FetchedTask{{NativeID: native, Name: native, Label: native}}); err != nil {
		t.Fatal(err)
	}
	return findNative(t, s, native)
}

func findNative(t *testing.T, s *Store, native string) core.Task {
	t.Helper()
	for _, task := range must[[]core.Task](t)(s.Tasks()) {
		if task.Remote != nil && task.Remote.NativeID == native {
			return task
		}
	}
	t.Fatalf("no task with native ID %s", native)
	return core.Task{}
}

func newEntry(t *testing.T, s *Store, task string, start time.Time, d time.Duration, note string) core.TimeEntry {
	t.Helper()
	return must[core.TimeEntry](t)(s.SaveEntry(core.TimeEntry{TaskID: task, Start: start, Duration: d, Note: note}))
}

func unitOf(es ...core.TimeEntry) core.Unit {
	u := core.Unit{TaskID: es[0].TaskID, Entries: es, Start: es[0].Start}
	for _, e := range es {
		u.Duration += e.Duration
	}
	return u
}

func TestOpenMigratesOnceAndReopens(t *testing.T) {
	path := filepath.Join(t.TempDir(), "yatta.db")
	for i := 0; i < 2; i++ {
		s, err := Open(path)
		if err != nil {
			t.Fatalf("open %d: %v", i, err)
		}
		var v int
		if err := s.db.QueryRow("PRAGMA user_version").Scan(&v); err != nil || v != 2 {
			t.Fatalf("user_version = %d, %v; want 2", v, err)
		}
		var fk int
		if err := s.db.QueryRow("PRAGMA foreign_keys").Scan(&fk); err != nil || fk != 1 {
			t.Fatalf("foreign_keys = %d, %v; want 1", fk, err)
		}
		s.Close()
	}
}

func TestUploadStateByTaskKind(t *testing.T) {
	s := open(t)
	local := must[core.Task](t)(s.SaveLocalTask(core.Task{Name: "Admin"}))
	remote := remoteTask(t, s, "PROJ-1")

	tests := []struct {
		name  string
		task  string
		phase core.Phase // "" => nil Upload
	}{
		{"local task", local.ID, ""},
		{"unassigned", "", ""},
		{"remote task", remote.ID, core.Pending},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			e := newEntry(t, s, tt.task, t0, time.Hour, "")
			switch {
			case tt.phase == "" && e.Upload != nil:
				t.Errorf("Upload = %+v; want nil", e.Upload)
			case tt.phase != "" && (e.Upload == nil || e.Upload.Phase != tt.phase):
				t.Errorf("Upload = %+v; want phase %s", e.Upload, tt.phase)
			}
		})
	}
}

func TestUploadLifecycle(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	a := newEntry(t, s, task.ID, t0, 20*time.Minute, "a")
	b := newEntry(t, s, task.ID, t0.Add(time.Hour), 10*time.Minute, "b")
	u := unitOf(a, b)

	if err := s.RecordFailure(u, errors.New("503 from Jira")); err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{a.ID, b.ID} {
		e := must[core.TimeEntry](t)(s.Entry(id))
		if e.Upload.Phase != core.Failed || e.Upload.Err != "503 from Jira" {
			t.Fatalf("after failure: %+v", e.Upload)
		}
	}
	if c := must[Counts](t)(s.Counts()); c.Failed != 2 || c.Pending != 0 {
		t.Fatalf("counts after failure = %+v", c)
	}

	if err := s.RecordUpload(u, "jira", "10042"); err != nil {
		t.Fatal(err)
	}
	e := must[core.TimeEntry](t)(s.Entry(a.ID))
	if e.Upload.Phase != core.Uploaded || e.Upload.Err != "" || !e.Locked() {
		t.Fatalf("after upload: %+v", e.Upload)
	}
	r := must[core.RemoteRecord](t)(s.Record(e.Upload.RecordID))
	if r.RemoteID != "10042" || r.Duration != 30*time.Minute || r.TaskID != task.ID || r.Excluded() {
		t.Fatalf("record = %+v", r)
	}
	if got := must[[]core.TimeEntry](t)(s.UnlockedRemoteEntries(time.Time{})); len(got) != 0 {
		t.Fatalf("unlocked after upload = %d entries; want 0", len(got))
	}
	if err := s.RecordUpload(u, "jira", "10043"); err == nil {
		t.Fatal("uploading locked entries again succeeded")
	}
}

func TestRecordExcluded(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	a := newEntry(t, s, task.ID, t0, 5*time.Minute, "")
	if err := s.RecordExcluded(unitOf(a), "jira"); err != nil {
		t.Fatal(err)
	}
	e := must[core.TimeEntry](t)(s.Entry(a.ID))
	if e.Upload.Phase != core.Excluded || !e.Locked() {
		t.Fatalf("Upload = %+v; want excluded and locked", e.Upload)
	}
	if r := must[core.RemoteRecord](t)(s.Record(e.Upload.RecordID)); !r.Excluded() {
		t.Fatalf("record %+v is not a sentinel", r)
	}
}

func TestLockedEntriesCannotChange(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	a := newEntry(t, s, task.ID, t0, time.Hour, "")
	if err := s.RecordUpload(unitOf(a), "jira", "1"); err != nil {
		t.Fatal(err)
	}
	a.Note = "changed"
	if _, err := s.SaveEntry(a); !errors.Is(err, ErrLocked) {
		t.Errorf("SaveEntry on locked = %v; want ErrLocked", err)
	}
	if err := s.DiscardEntry(a.ID); !errors.Is(err, ErrLocked) {
		t.Errorf("DiscardEntry on locked = %v; want ErrLocked", err)
	}
	// The trigger holds even against direct SQL.
	if _, err := s.db.Exec("UPDATE time_entries SET note = 'x' WHERE id = ?", a.ID); err == nil {
		t.Error("direct UPDATE of a locked entry succeeded")
	}
}

func TestDiscardAndEdit(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	local := must[core.Task](t)(s.SaveLocalTask(core.Task{Name: "Admin"}))
	a := newEntry(t, s, task.ID, t0, time.Hour, "")
	if err := s.RecordFailure(unitOf(a), errors.New("boom")); err != nil {
		t.Fatal(err)
	}
	// Moving a failed entry to a local task drops its upload state.
	a.TaskID = local.ID
	a = must[core.TimeEntry](t)(s.SaveEntry(a))
	if a.Upload != nil {
		t.Fatalf("Upload = %+v after moving to a local task; want nil", a.Upload)
	}
	if err := s.DiscardEntry(a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Entry(a.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("Entry after discard = %v; want ErrNotFound", err)
	}
	if err := s.DiscardEntry("nope"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("DiscardEntry(unknown) = %v; want ErrNotFound", err)
	}
}

func TestReadAssertsLocalTaskInvariant(t *testing.T) {
	s := open(t)
	local := must[core.Task](t)(s.SaveLocalTask(core.Task{Name: "Admin"}))
	a := newEntry(t, s, local.ID, t0, time.Hour, "")
	if _, err := s.db.Exec("UPDATE time_entries SET upload_error = 'x' WHERE id = ?", a.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Entry(a.ID); err == nil {
		t.Fatal("read of a local-task entry with upload state succeeded")
	}
}

func TestUnlockedRemoteEntriesBound(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	local := must[core.Task](t)(s.SaveLocalTask(core.Task{Name: "Admin"}))
	in := newEntry(t, s, task.ID, t0, time.Hour, "")
	newEntry(t, s, task.ID, t0.Add(48*time.Hour), time.Hour, "")
	newEntry(t, s, local.ID, t0, time.Hour, "")
	newEntry(t, s, "", t0, time.Hour, "")

	got := must[[]core.TimeEntry](t)(s.UnlockedRemoteEntries(t0.Add(time.Hour)))
	if len(got) != 1 || got[0].ID != in.ID {
		t.Fatalf("got %d entries; want only %s", len(got), in.ID)
	}
	if all := must[[]core.TimeEntry](t)(s.UnlockedRemoteEntries(time.Time{})); len(all) != 2 {
		t.Fatalf("unbounded = %d entries; want 2", len(all))
	}
}

func TestReconcile(t *testing.T) {
	s := open(t)
	fetch := []core.FetchedTask{
		{NativeID: "p", Name: "Project", NodeType: "project"},
		{NativeID: "e", ParentNativeID: "p", Name: "Epic", NodeType: "epic"},
		{NativeID: "1", ParentNativeID: "e", Name: "Login bug", Label: "PROJ-1", NodeType: "issue"},
		{NativeID: "2", ParentNativeID: "e", Name: "Signup", Label: "PROJ-2", NodeType: "issue"},
	}
	if err := s.ReconcileRemoteTasks("jira", fetch); err != nil {
		t.Fatal(err)
	}
	p, e, one := findNative(t, s, "p"), findNative(t, s, "e"), findNative(t, s, "1")
	if e.ParentID != p.ID || one.ParentID != e.ID || one.Remote.Label != "PROJ-1" {
		t.Fatalf("hierarchy not linked: %+v %+v", e, one)
	}
	newEntry(t, s, one.ID, t0, time.Hour, "")

	// The epic disappears upstream with both issues; PROJ-1 has an entry.
	if err := s.ReconcileRemoteTasks("jira", fetch[:1]); err != nil {
		t.Fatal(err)
	}
	byNative := map[string]core.Task{}
	for _, task := range must[[]core.Task](t)(s.Tasks()) {
		byNative[task.Remote.NativeID] = task
	}
	if _, ok := byNative["2"]; ok {
		t.Error("unreferenced absent task was kept")
	}
	if got := byNative["1"]; got.Remote == nil || got.Remote.Departed == nil || got.Selectable() {
		t.Errorf("referenced task should depart: %+v", got)
	}
	if got := byNative["e"]; got.Remote == nil || got.Remote.Departed == nil {
		t.Errorf("parent of a departed task should be kept, departed: %+v", got)
	}
	if got := byNative["p"]; got.Remote.Departed != nil {
		t.Errorf("fetched task marked departed: %+v", got)
	}
	if c := must[Counts](t)(s.Counts()); c.Departed != 1 {
		t.Errorf("departed count = %d; want 1", c.Departed)
	}

	// PROJ-1 comes back: restored, same ID.
	if err := s.ReconcileRemoteTasks("jira", fetch); err != nil {
		t.Fatal(err)
	}
	back := findNative(t, s, "1")
	if back.ID != one.ID || back.Remote.Departed != nil {
		t.Errorf("reappeared task = %+v; want same ID, not departed", back)
	}
}

func TestClearIntegrationRetiresTasks(t *testing.T) {
	s := open(t)
	if err := s.SetIntegration(IntegrationConfig{Integration: "jira", BaseURL: "https://x", KeyringKey: "k"}); err != nil {
		t.Fatal(err)
	}
	kept := remoteTask(t, s, "PROJ-1")
	if err := s.ReconcileRemoteTasks("jira", []core.FetchedTask{{NativeID: "PROJ-1", Name: "a"}, {NativeID: "PROJ-2", Name: "b"}}); err != nil {
		t.Fatal(err)
	}
	newEntry(t, s, kept.ID, t0, time.Hour, "")
	if err := s.ClearIntegration(); err != nil {
		t.Fatal(err)
	}
	tasks := must[[]core.Task](t)(s.Tasks())
	if len(tasks) != 1 || tasks[0].ID != kept.ID || tasks[0].Remote.Departed == nil {
		t.Fatalf("tasks after clear = %+v; want only the referenced one, departed", tasks)
	}
	if c := must[*IntegrationConfig](t)(s.Integration()); c != nil {
		t.Fatalf("integration still configured: %+v", c)
	}
}

func TestLocalTaskRules(t *testing.T) {
	s := open(t)
	remote := remoteTask(t, s, "PROJ-1")
	root := must[core.Task](t)(s.SaveLocalTask(core.Task{Name: "Root"}))
	child := must[core.Task](t)(s.SaveLocalTask(core.Task{Name: "Child", ParentID: root.ID}))

	if _, err := s.SaveLocalTask(core.Task{Name: "X", ParentID: remote.ID}); err == nil {
		t.Error("local task placed under a remote task")
	}
	root.ParentID = child.ID
	if _, err := s.SaveLocalTask(root); err == nil {
		t.Error("task moved beneath its own descendant")
	}
	now := t0
	child.Archived = &now
	if got := must[core.Task](t)(s.SaveLocalTask(child)); got.Selectable() {
		t.Error("archived task is selectable")
	}
}

func TestTimer(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	if tm := must[*core.ActiveTimer](t)(s.Timer()); tm != nil {
		t.Fatalf("timer running on a new database: %+v", tm)
	}
	if e := must[*core.TimeEntry](t)(s.StartTimer("", t0)); e != nil {
		t.Fatalf("starting the first timer produced an entry: %+v", e)
	}
	if err := s.SetTimerTask(task.ID); err != nil {
		t.Fatal(err)
	}
	// Starting another timer stops the first.
	e := must[*core.TimeEntry](t)(s.StartTimer("", t0.Add(25*time.Minute)))
	if e == nil || e.TaskID != task.ID || e.Duration != 25*time.Minute || e.Upload.Phase != core.Pending {
		t.Fatalf("switched-off entry = %+v", e)
	}
	e = must[*core.TimeEntry](t)(s.StopTimer(t0.Add(40 * time.Minute)))
	if e == nil || e.TaskID != "" || e.Duration != 15*time.Minute {
		t.Fatalf("stopped entry = %+v", e)
	}
	if c := must[Counts](t)(s.Counts()); c.Unassigned != 1 {
		t.Errorf("unassigned = %d; want 1", c.Unassigned)
	}
	if must[*core.TimeEntry](t)(s.StopTimer(t0)) != nil {
		t.Error("stopping with no timer produced an entry")
	}
	must[*core.TimeEntry](t)(s.StartTimer("", t0))
	if must[*core.TimeEntry](t)(s.StopTimer(t0)) != nil {
		t.Error("zero-length timer produced an entry")
	}
}

func TestSaveTimer(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	if err := s.SaveTimer(core.ActiveTimer{Start: t0}); !errors.Is(err, ErrNotFound) {
		t.Fatalf("SaveTimer with no timer = %v; want ErrNotFound", err)
	}
	must[*core.TimeEntry](t)(s.StartTimer("", t0.Add(10*time.Minute)))
	// Backdate the start, add a note, and choose the task in one save.
	want := core.ActiveTimer{Start: t0, TaskID: task.ID, Note: "standup"}
	if err := s.SaveTimer(want); err != nil {
		t.Fatal(err)
	}
	if got := must[*core.ActiveTimer](t)(s.Timer()); got == nil || *got != want {
		t.Fatalf("timer = %+v; want %+v", got, want)
	}
	// The note and the backdated start carry over to the entry on stop.
	e := must[*core.TimeEntry](t)(s.StopTimer(t0.Add(30 * time.Minute)))
	if e == nil || !e.Start.Equal(t0) || e.Duration != 30*time.Minute || e.Note != "standup" || e.TaskID != task.ID {
		t.Fatalf("stopped entry = %+v", e)
	}
	// Clearing the note and task works, and a new timer starts without either.
	must[*core.TimeEntry](t)(s.StartTimer(task.ID, t0))
	if err := s.SaveTimer(core.ActiveTimer{Start: t0}); err != nil {
		t.Fatal(err)
	}
	if got := must[*core.ActiveTimer](t)(s.Timer()); got.TaskID != "" || got.Note != "" {
		t.Fatalf("cleared timer = %+v", got)
	}
}

func TestPolicyRoundTrip(t *testing.T) {
	s := open(t)
	if p := must[core.Policy](t)(s.Policy()); p != (core.Policy{}) {
		t.Fatalf("default policy = %+v; want zero", p)
	}
	want := core.Policy{Increment: 15 * time.Minute, Direction: core.Up, Minimum: 15 * time.Minute,
		BelowMin: core.Exclude, Aggregate: core.AggTaskDay}
	if err := s.SetPolicy(want); err != nil {
		t.Fatal(err)
	}
	if got := must[core.Policy](t)(s.Policy()); got != want {
		t.Fatalf("policy = %+v; want %+v", got, want)
	}
	if err := s.SetPolicy(core.Policy{Increment: 7 * time.Minute}); err == nil {
		t.Error("unsupported increment accepted")
	}
	if err := s.SetPolicy(core.Policy{Minimum: time.Minute}); err == nil {
		t.Error("minimum without behavior accepted")
	}
}

func TestPurge(t *testing.T) {
	s := open(t)
	task := remoteTask(t, s, "PROJ-1")
	old := t0.Add(-72 * time.Hour)
	up := newEntry(t, s, task.ID, old, time.Hour, "")
	if err := s.RecordUpload(unitOf(up), "jira", "1"); err != nil {
		t.Fatal(err)
	}
	failed := newEntry(t, s, task.ID, old, time.Hour, "")
	if err := s.RecordFailure(unitOf(failed), errors.New("x")); err != nil {
		t.Fatal(err)
	}
	newEntry(t, s, task.ID, old, time.Hour, "")
	newEntry(t, s, "", old, time.Hour, "")
	recent := newEntry(t, s, task.ID, t0, time.Hour, "")

	f, p, u, err := s.PurgeCounts(t0.Add(-24 * time.Hour))
	if err != nil || f != 1 || p != 1 || u != 1 {
		t.Fatalf("PurgeCounts = %d, %d, %d, %v; want 1, 1, 1", f, p, u, err)
	}
	if err := s.Purge(t0.Add(-24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	left := must[[]core.TimeEntry](t)(s.Entries(time.Time{}, time.Time{}))
	if len(left) != 1 || left[0].ID != recent.ID {
		t.Fatalf("after purge %d entries left; want only the recent one", len(left))
	}
	var records int
	s.db.QueryRow("SELECT COUNT(*) FROM remote_records").Scan(&records)
	if records != 0 {
		t.Errorf("%d unreferenced records survived purge", records)
	}
}
