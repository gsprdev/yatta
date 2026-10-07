package core

import (
	"testing"
	"time"
)

func TestPlanUpload(t *testing.T) {
	failed := entry("f", "T", at(2, 13, 0), 30*m, "")
	failed.Upload = &UploadState{Phase: Failed, Err: "boom"}
	uploaded := entry("u", "T", at(2, 8, 0), 30*m, "")
	uploaded.Upload = &UploadState{Phase: Uploaded, RecordID: "r1"}
	excluded := entry("x", "T", at(2, 7, 0), 30*m, "")
	excluded.Upload = &UploadState{Phase: Excluded, RecordID: "r2"}
	local := entry("l", "L", at(2, 9, 0), 30*m, "")
	local.Upload = nil
	unassigned := entry("n", "", at(2, 9, 0), 30*m, "")
	unassigned.Upload = nil

	all := []TimeEntry{
		uploaded, excluded, local, unassigned, failed,
		entry("a", "T", at(2, 10, 0), 30*m, ""),
		entry("z", "T", at(2, 23, 59), 30*m, ""), // last minute of the 2nd, local
		entry("t", "T", at(3, 9, 0), 30*m, ""),
	}
	agg := Policy{Aggregate: AggTaskDay}

	tests := []struct {
		name    string
		p       Policy
		through time.Time
		want    []unitSummary
	}{
		{
			name: "no bound: every unlocked remote entry, grouped",
			p:    agg,
			want: []unitSummary{
				{"T", "afz", at(2, 10, 0), 90 * m, ""},
				{"T", "t", at(3, 9, 0), 30 * m, ""},
			},
		},
		{
			name:    "bound is an inclusive local date and keeps the day whole",
			p:       agg,
			through: EndOfDay(at(2, 12, 0), ny),
			want:    []unitSummary{{"T", "afz", at(2, 10, 0), 90 * m, ""}},
		},
		{
			name:    "locked, local, and unassigned entries are never planned",
			p:       Policy{},
			through: EndOfDay(at(2, 0, 0), ny),
			want: []unitSummary{
				{"T", "a", at(2, 10, 0), 30 * m, ""},
				{"T", "f", at(2, 13, 0), 30 * m, ""},
				{"T", "z", at(2, 23, 59), 30 * m, ""},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanUpload(all, tt.p, ny, tt.through)
			if got := summarize(plan.Upload); !equalSummaries(got, tt.want) {
				t.Errorf("upload = %+v\nwant     %+v", got, tt.want)
			}
			if len(plan.Excluded) != 0 {
				t.Errorf("excluded = %+v; want none", plan.Excluded)
			}
		})
	}
}

func TestPlanUploadExcluded(t *testing.T) {
	p := Policy{Minimum: 15 * m, BelowMin: Exclude}
	plan := PlanUpload([]TimeEntry{entry("a", "T", at(2, 9, 0), 5*m, "")}, p, ny, time.Time{})
	if len(plan.Upload) != 0 || len(plan.Excluded) != 1 {
		t.Fatalf("plan = %+v; want one excluded unit", plan)
	}
}

func TestEndOfDay(t *testing.T) {
	tests := []struct {
		name string
		in   time.Time
		want time.Time
	}{
		{"ordinary day", at(2, 12, 0), time.Date(2026, 3, 3, 0, 0, 0, 0, ny).Add(-time.Nanosecond)},
		// 8 March 2026 is 23 hours long in New York.
		{"spring-forward day", at(8, 12, 0), time.Date(2026, 3, 9, 0, 0, 0, 0, ny).Add(-time.Nanosecond)},
		{"just after midnight", at(2, 0, 0), time.Date(2026, 3, 3, 0, 0, 0, 0, ny).Add(-time.Nanosecond)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := EndOfDay(tt.in, ny); !got.Equal(tt.want) {
				t.Errorf("EndOfDay(%v) = %v; want %v", tt.in, got, tt.want)
			}
		})
	}
}

func TestDayTotal(t *testing.T) {
	now := at(3, 10, 0)
	tests := []struct {
		name    string
		day     time.Time // zero means today
		entries []TimeEntry
		timer   *ActiveTimer
		want    time.Duration
	}{
		{"empty", time.Time{}, nil, nil, 0},
		{"today only", time.Time{}, []TimeEntry{
			entry("a", "T", at(3, 8, 0), 30*m, ""),
			entry("b", "T", at(2, 8, 0), 30*m, ""),
		}, nil, 30 * m},
		{"entry crossing midnight counts its part in today", time.Time{}, []TimeEntry{
			entry("a", "T", at(2, 23, 0), 2*time.Hour, ""),
		}, nil, time.Hour},
		{"running timer counts to now", time.Time{}, nil, &ActiveTimer{Start: at(3, 9, 15)}, 45 * m},
		{"timer started yesterday counts from midnight", time.Time{}, nil, &ActiveTimer{Start: at(2, 22, 0)}, 10 * time.Hour},
		{"an earlier day", at(2, 0, 0), []TimeEntry{
			entry("a", "T", at(3, 8, 0), 30*m, ""),
			entry("b", "T", at(2, 8, 0), 20*m, ""),
			entry("c", "T", at(1, 23, 30), time.Hour, ""),
		}, nil, 50 * m},
		{"a timer running since an earlier day counts to its midnight", at(2, 12, 0), nil, &ActiveTimer{Start: at(2, 22, 0)}, 2 * time.Hour},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			day := tt.day
			if day.IsZero() {
				day = now
			}
			if got := DayTotal(tt.entries, tt.timer, day, now, ny); got != tt.want {
				t.Errorf("DayTotal = %v; want %v", got, tt.want)
			}
		})
	}
}
