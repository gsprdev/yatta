package core

import (
	"testing"
	"time"
)

func TestCombineNotes(t *testing.T) {
	tests := []struct {
		name  string
		notes []string
		want  string
	}{
		{"none", nil, ""},
		{"one, no period added", []string{"Wrote tests"}, "Wrote tests"},
		{"period between", []string{"Fixed login bug", "Wrote tests."}, "Fixed login bug. Wrote tests."},
		{"existing period kept", []string{"Fixed bug.", "Tests"}, "Fixed bug. Tests"},
		{"trimmed", []string{"  a \n", "\tb "}, "a. b"},
		{"empty dropped", []string{"a", "  ", "", "b"}, "a. b"},
		{"duplicates dropped", []string{"a", "b", "a"}, "a. b"},
		{"duplicate after trim", []string{"a", " a "}, "a"},
		{"all empty", []string{" ", ""}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := CombineNotes(tt.notes); got != tt.want {
				t.Errorf("CombineNotes(%q) = %q; want %q", tt.notes, got, tt.want)
			}
		})
	}
}

var ny = mustLoad("America/New_York")

func mustLoad(name string) *time.Location {
	loc, err := time.LoadLocation(name)
	if err != nil {
		panic(err)
	}
	return loc
}

// at builds a UTC instant from a New York wall-clock time.
func at(day, hour, min int) time.Time {
	return time.Date(2026, 3, day, hour, min, 0, 0, ny).UTC()
}

func entry(id, task string, start time.Time, d time.Duration, note string) TimeEntry {
	return TimeEntry{ID: id, TaskID: task, Start: start, Duration: d, Note: note, Upload: &UploadState{Phase: Pending}}
}

type unitSummary struct {
	task     string
	ids      string
	start    time.Time
	duration time.Duration
	note     string
}

func summarize(us []Unit) []unitSummary {
	var out []unitSummary
	for _, u := range us {
		ids := ""
		for _, e := range u.Entries {
			ids += e.ID
		}
		out = append(out, unitSummary{u.TaskID, ids, u.Start, u.Duration, u.Note})
	}
	return out
}

func equalSummaries(a, b []unitSummary) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].task != b[i].task || a[i].ids != b[i].ids || !a[i].start.Equal(b[i].start) ||
			a[i].duration != b[i].duration || a[i].note != b[i].note {
			return false
		}
	}
	return true
}

func TestGroup(t *testing.T) {
	q := 15 * m
	daily := Policy{Increment: q, Minimum: q, BelowMin: Exclude, Aggregate: AggTaskDay}
	tests := []struct {
		name     string
		entries  []TimeEntry
		p        Policy
		upload   []unitSummary
		excluded []unitSummary
	}{
		{
			name: "no aggregation: one unit per entry",
			entries: []TimeEntry{
				entry("b", "T", at(2, 10, 0), 20*m, "two"),
				entry("a", "T", at(2, 9, 0), 10*m, "one"),
			},
			p: Policy{},
			upload: []unitSummary{
				{"T", "a", at(2, 9, 0), 10 * m, "one"},
				{"T", "b", at(2, 10, 0), 20 * m, "two"},
			},
		},
		{
			name: "sum before rounding: three 5m entries make one 15m record",
			entries: []TimeEntry{
				entry("a", "T", at(2, 9, 0), 5*m, "x"),
				entry("b", "T", at(2, 11, 0), 5*m, "y"),
				entry("c", "T", at(2, 14, 0), 5*m, "z"),
			},
			p:      Policy{Increment: q, Aggregate: AggTaskDay},
			upload: []unitSummary{{"T", "abc", at(2, 9, 0), 15 * m, "x. y. z"}},
		},
		{
			name: "different tasks and days stay apart",
			entries: []TimeEntry{
				entry("a", "T", at(2, 9, 0), 30*m, ""),
				entry("b", "U", at(2, 9, 30), 30*m, ""),
				entry("c", "T", at(3, 9, 0), 30*m, ""),
			},
			p: daily,
			upload: []unitSummary{
				{"T", "a", at(2, 9, 0), 30 * m, ""},
				{"U", "b", at(2, 9, 30), 30 * m, ""},
				{"T", "c", at(3, 9, 0), 30 * m, ""},
			},
		},
		{
			// 23:30 on the 2nd and 00:30 on the 3rd in New York are both on
			// the 3rd in UTC; grouping must follow the local day.
			name: "day boundary is local, not UTC",
			entries: []TimeEntry{
				entry("a", "T", at(2, 23, 30), 30*m, ""),
				entry("b", "T", at(3, 0, 30), 30*m, ""),
			},
			p: daily,
			upload: []unitSummary{
				{"T", "a", at(2, 23, 30), 30 * m, ""},
				{"T", "b", at(3, 0, 30), 30 * m, ""},
			},
		},
		{
			name: "same local day across a UTC date change is one group",
			entries: []TimeEntry{
				entry("a", "T", at(2, 18, 0), 30*m, ""),
				entry("b", "T", at(2, 21, 0), 30*m, ""),
			},
			p:      daily,
			upload: []unitSummary{{"T", "ab", at(2, 18, 0), time.Hour, ""}},
		},
		{
			name: "group below minimum is excluded whole",
			entries: []TimeEntry{
				entry("a", "T", at(2, 9, 0), 3*m, "a"),
				entry("b", "T", at(2, 10, 0), 3*m, "b"),
				entry("c", "U", at(2, 11, 0), 20*m, "c"),
			},
			p:        daily,
			upload:   []unitSummary{{"U", "c", at(2, 11, 0), 15 * m, "c"}},
			excluded: []unitSummary{{"T", "ab", at(2, 9, 0), 0, "a. b"}},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upload, excluded := Group(tt.entries, tt.p, ny)
			if got := summarize(upload); !equalSummaries(got, tt.upload) {
				t.Errorf("upload = %+v\nwant     %+v", got, tt.upload)
			}
			if got := summarize(excluded); !equalSummaries(got, tt.excluded) {
				t.Errorf("excluded = %+v\nwant       %+v", got, tt.excluded)
			}
		})
	}
}

func TestGroupRawIsUnrounded(t *testing.T) {
	up, _ := Group([]TimeEntry{
		entry("a", "T", at(2, 9, 0), 7*m, ""),
		entry("b", "T", at(2, 10, 0), 9*m, ""),
	}, Policy{Increment: 15 * m, Aggregate: AggTaskDay}, ny)
	if len(up) != 1 || up[0].Raw != 16*m || up[0].Duration != 15*m {
		t.Fatalf("got %+v; want one unit, Raw 16m, Duration 15m", up)
	}
}
