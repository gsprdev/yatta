package core

import (
	"fmt"
	"sort"
	"strings"
	"time"
)

// A Unit is what one remote record will represent: one entry when aggregation
// is off, several when it is on.
type Unit struct {
	TaskID   string
	Entries  []TimeEntry   // in start order
	Start    time.Time     // earliest member's start; not rounded
	Raw      time.Duration // sum of member durations
	Duration time.Duration // Raw with the policy applied
	Note     string        // member notes combined; see CombineNotes
}

// Group forms units from entries and applies the policy to each unit's summed
// duration. Units excluded by the policy — below the minimum under Exclude, or
// rounded to zero — are returned separately. loc is the device timezone, used
// only for the task_day grouping boundary. p must be valid.
//
// Units are returned in start order.
func Group(entries []TimeEntry, p Policy, loc *time.Location) (upload, excluded []Unit) {
	type key struct {
		task string
		day  string
		id   string
	}
	byKey := map[key][]TimeEntry{}
	var order []key
	for _, e := range entries {
		var k key
		switch p.Aggregate {
		case AggTaskDay:
			k = key{task: e.TaskID, day: e.Start.In(loc).Format(time.DateOnly)}
		case "", AggNone:
			k = key{id: e.ID}
		default:
			panic(fmt.Sprintf("core: invalid policy: aggregation %q", p.Aggregate))
		}
		if _, ok := byKey[k]; !ok {
			order = append(order, k)
		}
		byKey[k] = append(byKey[k], e)
	}

	units := make([]Unit, 0, len(order))
	for _, k := range order {
		members := byKey[k]
		sortEntries(members)
		u := Unit{TaskID: members[0].TaskID, Entries: members, Start: members[0].Start}
		notes := make([]string, len(members))
		for i, e := range members {
			u.Raw += e.Duration
			notes[i] = e.Note
		}
		u.Note = CombineNotes(notes)
		units = append(units, u)
	}
	sort.SliceStable(units, func(i, j int) bool {
		if !units[i].Start.Equal(units[j].Start) {
			return units[i].Start.Before(units[j].Start)
		}
		return units[i].TaskID < units[j].TaskID
	})

	for _, u := range units {
		var skip bool
		u.Duration, skip = p.apply(u.Raw)
		if skip {
			excluded = append(excluded, u)
		} else {
			upload = append(upload, u)
		}
	}
	return upload, excluded
}

func sortEntries(es []TimeEntry) {
	sort.SliceStable(es, func(i, j int) bool {
		if !es[i].Start.Equal(es[j].Start) {
			return es[i].Start.Before(es[j].Start)
		}
		return es[i].ID < es[j].ID
	})
}

// CombineNotes builds a remote record's note from its members' notes, given in
// start order: each is trimmed, empty and duplicate notes are dropped, every
// note but the last gets a trailing period unless it already ends with one,
// and the notes are joined with a single space.
func CombineNotes(notes []string) string {
	seen := map[string]bool{}
	var kept []string
	for _, n := range notes {
		n = strings.TrimSpace(n)
		if n == "" || seen[n] {
			continue
		}
		seen[n] = true
		kept = append(kept, n)
	}
	for i := 0; i < len(kept)-1; i++ {
		if !strings.HasSuffix(kept[i], ".") {
			kept[i] += "."
		}
	}
	return strings.Join(kept, " ")
}
