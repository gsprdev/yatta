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

// DayTotal is the time recorded on the local calendar day containing day,
// including the running timer up to now. Entries and a timer crossing
// midnight count only their portion inside the day.
func DayTotal(entries []TimeEntry, timer *ActiveTimer, day, now time.Time, loc *time.Location) time.Duration {
	l := day.In(loc)
	dayStart := time.Date(l.Year(), l.Month(), l.Day(), 0, 0, 0, 0, loc)
	dayEnd := time.Date(l.Year(), l.Month(), l.Day()+1, 0, 0, 0, 0, loc)
	var total time.Duration
	for _, d := range TaskTotals(entries, timer, dayStart, dayEnd, now) {
		total += d
	}
	return total
}

// TaskTotals is the time recorded per task ID ("" for no task) inside
// [from, to), including the running timer up to now. Entries and a timer
// crossing a bound count only their portion inside. A zero from or to leaves
// that side unbounded.
func TaskTotals(entries []TimeEntry, timer *ActiveTimer, from, to, now time.Time) map[string]time.Duration {
	totals := map[string]time.Duration{}
	add := func(taskID string, start, end time.Time) {
		if !from.IsZero() && start.Before(from) {
			start = from
		}
		if !to.IsZero() && end.After(to) {
			end = to
		}
		if end.After(start) {
			totals[taskID] += end.Sub(start)
		}
	}
	for _, e := range entries {
		add(e.TaskID, e.Start, e.End())
	}
	if timer != nil {
		add(timer.TaskID, timer.Start, now)
	}
	return totals
}
