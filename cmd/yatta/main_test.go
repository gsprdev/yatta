package main

import (
	"bytes"
	"errors"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/gsprdev/yatta/internal/core"
	"github.com/gsprdev/yatta/internal/store"
)

// syncBuffer is the program's terminal: the renderer writes from its own
// goroutine.
type syncBuffer struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.b.String()
}

// TestSmoke runs the program as the binary wires it, with the real renderer
// and the local timezone, against a fresh database. The renderer draws a
// frame before any command has returned, so this catches what the model
// tests cannot: a View that only works once data has loaded, or wiring that
// only main does. Bubble Tea recovers a panic and returns ErrProgramPanic.
func TestSmoke(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "yatta.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()

	// Entries today, yesterday, and across the local midnight, plus a
	// running timer, so the day-sensitive views have something to place.
	now := time.Now()
	task, err := st.SaveLocalTask(core.Task{Name: "Smoke task"})
	if err != nil {
		t.Fatal(err)
	}
	midnight := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.Local)
	for _, e := range []core.TimeEntry{
		{TaskID: task.ID, Start: now.Add(-3 * time.Hour), Duration: 30 * time.Minute, Note: "today"},
		{TaskID: task.ID, Start: now.Add(-27 * time.Hour), Duration: time.Hour, Note: "yesterday"},
		{TaskID: task.ID, Start: midnight.Add(-30 * time.Minute), Duration: time.Hour, Note: "across midnight"},
		{Start: now.Add(-5 * time.Hour), Duration: 15 * time.Minute, Note: "unassigned"},
	} {
		if _, err := st.SaveEntry(e); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := st.StartTimer(task.ID, now.Add(-10*time.Minute)); err != nil {
		t.Fatal(err)
	}

	out := &syncBuffer{}
	p := newProgram(st, tea.WithInput(nil), tea.WithOutput(out), tea.WithoutSignalHandler())

	done := make(chan error, 1)
	go func() {
		_, err := p.Run()
		done <- err
	}()

	go func() {
		key := func(k string) tea.KeyMsg {
			switch k {
			case "esc":
				return tea.KeyMsg{Type: tea.KeyEsc}
			case "enter":
				return tea.KeyMsg{Type: tea.KeyEnter}
			case "up":
				return tea.KeyMsg{Type: tea.KeyUp}
			case "down":
				return tea.KeyMsg{Type: tea.KeyDown}
			}
			return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(k)}
		}
		p.Send(tea.WindowSizeMsg{Width: 100, Height: 30})
		// Let the load land; keys sent before it would find no entries.
		time.Sleep(300 * time.Millisecond)
		// Visit every view reachable from the entry list, and back.
		for _, k := range []string{
			"?", "esc",
			"down", "up",
			"enter", "esc", // editor on the selected entry
			"a", "esc", // new entry
			"E", "esc", // running timer
			"n", "esc", // picker
			"T", "esc",
			"t", "esc", // tasks
			"!", "esc", // attention
			",", "esc", // settings
			"u", "esc", // upload, with no integration
			"/", "s", "m", "o", "esc", // search
		} {
			p.Send(key(k))
		}
		time.Sleep(100 * time.Millisecond)
		p.Quit()
	}()

	select {
	case err := <-done:
		if errors.Is(err, tea.ErrProgramPanic) {
			t.Fatalf("program panicked; output:\n%s", out)
		}
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatalf("program did not exit; output:\n%s", out)
	}
	if s := out.String(); !strings.Contains(s, "Smoke task") || !strings.Contains(s, "today") {
		t.Errorf("rendered output lacks the seeded data:\n%s", s)
	}
}
