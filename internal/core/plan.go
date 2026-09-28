package core

import "time"

// Plan is the result of upload planning.
type Plan struct {
	Upload   []Unit // each becomes one Create call
	Excluded []Unit // each becomes one sentinel record; shown on the confirmation screen
}

// PlanUpload selects the unlocked entries on remote tasks that start on or
// before through, and groups them under the policy. Entries on local tasks,
// unassigned entries, and locked entries are ignored: upload is one-way, so an
// entry that has been uploaded or excluded is finished.
//
// through should come from EndOfDay, so that the bound is an inclusive local
// date and never bisects an aggregation group. The zero value means no bound.
// p must be valid; loc must not be nil.
func PlanUpload(entries []TimeEntry, p Policy, loc *time.Location, through time.Time) Plan {
	var eligible []TimeEntry
	for _, e := range entries {
		if e.TaskID == "" || e.Upload == nil || e.Upload.Locked() {
			continue
		}
		if !through.IsZero() && e.Start.After(through) {
			continue
		}
		eligible = append(eligible, e)
	}
	upload, excluded := Group(eligible, p, loc)
	return Plan{Upload: upload, Excluded: excluded}
}

// EndOfDay returns the last instant of the local calendar day containing t.
func EndOfDay(t time.Time, loc *time.Location) time.Time {
	l := t.In(loc)
	next := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
	return next.Add(-time.Nanosecond)
}

// DayTotal is the time recorded on the local calendar day containing now,
// including the running timer. Entries crossing midnight count only their
// portion inside the day.
func DayTotal(entries []TimeEntry, timer *ActiveTimer, now time.Time, loc *time.Location) time.Duration {
	l := now.In(loc)
	dayStart := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	dayEnd := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
	var total time.Duration
	add := func(start, end time.Time) {
		if start.Before(dayStart) {
			start = dayStart
		}
		if end.After(dayEnd) {
			end = dayEnd
		}
		if end.After(start) {
			total += end.Sub(start)
		}
	}
	for _, e := range entries {
		add(e.Start, e.End())
	}
	if timer != nil {
		add(timer.Start, now)
	}
	return total
}
