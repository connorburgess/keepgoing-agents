package main

import "time"

// dockNearLimitPercent is the share of a window at which a reading counts as
// near its limit, the same level the page draws a meter in warning colour.
const dockNearLimitPercent = 70

// dockMarkOldUsage marks each usage reading in the report as old when its age
// is over staleAfter, the same threshold a check is called stale at. This is
// separate from availability "stale", which says every window of the reading
// has reset since it was taken: an old reading can still describe current
// windows, and a recent one can describe windows that have all ended.
//
// Age alone is information, not something for a person to act on. An old
// reading needs a person only when it was near its limit: a window that had
// not ended was at dockNearLimitPercent or more when read, or the route or
// account it is shown on is at a usage limit. Then it carries NeedsPerson and
// AgeNote, as what it said may since have changed either way.
//
// It also records the age of the oldest old reading the report shows, so the
// page can say how old its data is and not only when it read its records.
// It runs after usage is shared across loops, routes and accounts, so each
// copy of a reading is marked the same way, with the limit taken from where
// that copy is shown.
func dockMarkOldUsage(r *dockReport, staleAfter time.Duration) {
	limit := int64(staleAfter / time.Second)
	atLimit := map[string]bool{}
	for _, x := range r.Routes {
		if x.Attention == "limit" || dockNotesAtLimit(x.Notes) {
			atLimit[x.Key] = true
		}
	}
	mark := func(u *dockUsageTarget, limited bool) {
		if u.Availability == "unavailable" || u.AgeSeconds == nil || *u.AgeSeconds <= limit {
			return
		}
		u.Old = true
		if limited || dockUsageNearLimit(*u) {
			u.NeedsPerson = true
			u.AgeNote = "This reading is " + dockSpan(time.Duration(*u.AgeSeconds)*time.Second) + " old and was near its limit; it may have changed."
		}
		if r.OldestUsageAgeSeconds == nil || *u.AgeSeconds > *r.OldestUsageAgeSeconds {
			age := *u.AgeSeconds
			r.OldestUsageAgeSeconds = &age
		}
	}
	for i := range r.Loops {
		for j := range r.Loops[i].Chain.Entries {
			e := &r.Loops[i].Chain.Entries[j]
			mark(&e.Usage, atLimit[e.Route] || dockNotesAtLimit(e.Notes))
		}
	}
	for i := range r.Routes {
		mark(&r.Routes[i].Usage, atLimit[r.Routes[i].Key])
	}
	for i := range r.Allowances {
		mark(&r.Allowances[i].Usage, dockNotesAtLimit(r.Allowances[i].Notes))
	}
}

// dockUsageNearLimit: a window of the reading that had not ended was at
// dockNearLimitPercent or more when it was read.
func dockUsageNearLimit(u dockUsageTarget) bool {
	for _, w := range u.Windows {
		if !w.Stale && w.UsedPercent >= dockNearLimitPercent {
			return true
		}
	}
	return false
}

func dockNotesAtLimit(notes []dockNote) bool {
	for _, n := range notes {
		if n.Tone == "limit" {
			return true
		}
	}
	return false
}
