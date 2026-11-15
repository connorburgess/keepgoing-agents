package main

import "time"

// dockMarkOldUsage marks each usage reading in the report as old when its age
// is over staleAfter, the same threshold a check is called stale at. This is
// separate from availability "stale", which says every window of the reading
// has reset since it was taken: an old reading can still describe current
// windows, and a recent one can describe windows that have all ended.
//
// It also records the age of the oldest old reading the report shows, so the
// page can say how old its data is and not only when it read its records.
// It runs after usage is shared across loops, routes and accounts, so each
// copy of a reading is marked the same way.
func dockMarkOldUsage(r *dockReport, staleAfter time.Duration) {
	limit := int64(staleAfter / time.Second)
	mark := func(u *dockUsageTarget) {
		if u.Availability == "unavailable" || u.AgeSeconds == nil || *u.AgeSeconds <= limit {
			return
		}
		u.Old = true
		if r.OldestUsageAgeSeconds == nil || *u.AgeSeconds > *r.OldestUsageAgeSeconds {
			age := *u.AgeSeconds
			r.OldestUsageAgeSeconds = &age
		}
	}
	for i := range r.Loops {
		for j := range r.Loops[i].Chain.Entries {
			mark(&r.Loops[i].Chain.Entries[j].Usage)
		}
	}
	for i := range r.Routes {
		mark(&r.Routes[i].Usage)
	}
	for i := range r.Allowances {
		mark(&r.Allowances[i].Usage)
	}
}
