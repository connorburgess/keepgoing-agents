package main

import (
	"path/filepath"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// Caps on the account route lists, so the largest registry stays inside the
// response bound. The rest are counted.
const (
	dockMaxAccountUses   = 32  // accounts listed with their routes
	dockMaxAccountRoutes = 8   // routes listed under one account
	dockMaxAccountLoops  = 8   // loops listed under one route of an account
	dockMaxAccountDir    = 512 // bytes of a directory sent with identifiers
)

// dockAccountFacts is what a route's configuration says about its account
// context, kept for the account route lists and never sent: the directory is
// a path.
type dockAccountFacts struct {
	provider, selection, dir, group string
}

func dockAccountFactsOf(a config.Agent) dockAccountFacts {
	selection, dir := accounts.RouteSelection(a)
	return dockAccountFacts{provider: a.Provider, selection: selection, dir: dir, group: a.Group}
}

// dockAccountUse is one account with the routes that use it: a catalog entry
// (Card), or an account context that routes use with no catalog entry
// (Context, unnamed). It is built from loop configurations and the catalog
// only: nothing on the computer is searched and nothing is adopted.
type dockAccountUse struct {
	Card    string `json:"card,omitempty"`
	Context string `json:"context,omitempty"`
	// For an unnamed context: what its routes state. Selection is
	// inherited, explicit_directory, bb_thread or externally_managed.
	// Directory is sent only with identifiers. Add is the accounts add
	// command that would name it; it changes no route.
	Provider  string `json:"provider,omitempty"`
	Selection string `json:"selection,omitempty"`
	Group     string `json:"group,omitempty"`
	Directory string `json:"directory,omitempty"`
	Add       string `json:"add,omitempty"`
	Title     string `json:"title,omitempty"`
	// MayBeSame are catalog entries whose provider and selected directory
	// (or inherited selection) are the same as this unnamed context's. It
	// never says they are the same account.
	MayBeSame []string           `json:"mayBeSame,omitempty"`
	Routes    []dockAccountRoute `json:"routes"`
	Omitted   int                `json:"omitted,omitempty"`
	// Reading is from the newest recorded reading among its routes only;
	// Out is true when that reading has a window used up.
	Reading dockText `json:"reading"`
	Out     bool     `json:"out,omitempty"`
}

// dockAccountRoute is one route of an account, by its key in the report's
// routes, with the loops that use it by their keys.
type dockAccountRoute struct {
	Route    string   `json:"route"`
	Title    string   `json:"title"`
	BBThread bool     `json:"bbThread,omitempty"`
	Loops    []string `json:"loops"`
	Omitted  int      `json:"omitted,omitempty"`
}

// dockAccountUses lists each catalog card with the routes that name it, then
// each account context that routes use without naming an entry. f is the
// catalog, nil when unread. Usage comes from the report's routes, falling
// back to a chain entry's own reading for a route past the routes cap.
func dockAccountUses(r dockReport, v accounts.View, f *accounts.File, identifiers bool, now time.Time) ([]dockAccountUse, int) {
	type acc struct {
		use    dockAccountUse
		facts  dockAccountFacts
		routes map[string]int
	}
	byKey := map[string]*acc{}
	order := []string{}
	for _, c := range v.Cards {
		k := "card:" + c.ID
		byKey[k] = &acc{use: dockAccountUse{Card: c.ID, Routes: []dockAccountRoute{}}, routes: map[string]int{}}
		order = append(order, k)
	}
	usage := map[string]dockUsageTarget{}
	for _, x := range r.Routes {
		usage[x.Key] = x.Usage
	}
	var unnamed []string
	for _, l := range r.Loops {
		for _, e := range l.Chain.Entries {
			a := e.CatalogAccount
			if a == nil {
				continue
			}
			var k string
			switch {
			case a.Assigned == "by_you" && a.Account != "":
				k = "card:" + a.Account
			case a.Context != "":
				k = "context:" + a.Context
			default:
				continue
			}
			x := byKey[k]
			if x == nil {
				if a.Assigned == "by_you" {
					// A stated entry the catalog does not hold: the route's
					// own check says so.
					continue
				}
				x = &acc{use: dockAccountUse{Context: a.Context, Routes: []dockAccountRoute{}}, facts: e.acct, routes: map[string]int{}}
				byKey[k] = x
				unnamed = append(unnamed, k)
			}
			if _, ok := usage[e.Route]; !ok {
				usage[e.Route] = e.Usage
			}
			i, ok := x.routes[e.Route]
			if !ok {
				if len(x.use.Routes) >= dockMaxAccountRoutes {
					x.use.Omitted++
					x.routes[e.Route] = -1
					continue
				}
				title := e.Title
				if e.TitleAccount != "" {
					title += " on " + e.TitleAccount
				}
				x.use.Routes = append(x.use.Routes, dockAccountRoute{Route: e.Route, Title: clip(title, 80), BBThread: e.acct.selection == "bb_thread", Loops: []string{}})
				i = len(x.use.Routes) - 1
				x.routes[e.Route] = i
			}
			if i < 0 {
				continue
			}
			rt := &x.use.Routes[i]
			if n := len(rt.Loops); n > 0 && rt.Loops[n-1] == l.Key {
				continue
			}
			if len(rt.Loops) >= dockMaxAccountLoops {
				rt.Omitted++
				continue
			}
			rt.Loops = append(rt.Loops, l.Key)
		}
	}
	order = append(order, unnamed...)
	omitted := 0
	if len(order) > dockMaxAccountUses {
		omitted, order = len(order)-dockMaxAccountUses, order[:dockMaxAccountUses]
	}
	out := make([]dockAccountUse, 0, len(order))
	for _, k := range order {
		x := byKey[k]
		u := x.use
		var newest *dockUsageTarget
		for _, rt := range u.Routes {
			if t, ok := usage[rt.Route]; ok && t.Availability != "unavailable" && t.ObservedAt != nil && (newest == nil || t.ObservedAt.After(*newest.ObservedAt)) {
				newest = &t
			}
		}
		u.Reading, u.Out = dockAccountReading(newest, len(u.Routes)+u.Omitted, now)
		if u.Context != "" {
			u.Provider, u.Selection, u.Group = clip(x.facts.provider, dockMaxText), x.facts.selection, clip(x.facts.group, dockMaxText)
			// A directory too long to send is left as the placeholder.
			show := identifiers && len(x.facts.dir) <= dockMaxAccountDir
			if show {
				u.Directory = x.facts.dir
			}
			u.Title = dockAccountTitle(u.Provider)
			u.Add = dockAccountAddCommand(x.facts, show)
			u.MayBeSame = dockMayBeSame(f, x.facts)
		}
		out = append(out, u)
	}
	return out, omitted
}

// dockAccountReading says whether the newest reading among an account's
// routes had a window used up, when it resets and how old the reading is, in
// the words the route readings use. Readings are never merged.
func dockAccountReading(u *dockUsageTarget, routes int, now time.Time) (dockText, bool) {
	switch {
	case routes == 0:
		return nil, false
	case u == nil:
		if routes == 1 {
			return txt("No usage reading is recorded for its route."), false
		}
		return txt("No usage reading is recorded for its routes."), false
	}
	read := dockAgo(now, *u.ObservedAt)
	for _, w := range u.Windows {
		if w.Stale || w.UsedPercent < 100 {
			continue
		}
		s := txt(dockCapital(w.Name) + " allowance out since read " + read)
		if w.ResetsAt != nil {
			return txt(s, " · resets ", dockUntil(now, *w.ResetsAt)), true
		}
		return txt(s, " · no reset time was reported"), true
	}
	if len(u.UsedUp) > 0 {
		// A used-up window past the cap of windows shown.
		return u.UsedUp, true
	}
	if u.Availability == "stale" {
		return txt("Not out when read " + read + "; every window it describes has ended since"), false
	}
	return txt("Not out when read " + read), false
}

// dockAccountAddCommand is the accounts add command that would name an
// unnamed context, from the facts its routes state. The label is the
// person's to choose. The directory is filled in only with identifiers.
func dockAccountAddCommand(f dockAccountFacts, identifiers bool) string {
	s := "looptender accounts add --label ACCOUNT_LABEL"
	switch {
	case f.selection == "bb_thread":
		return s + " --provider bb --selection externally_managed"
	case f.provider != "claude-code" && f.provider != "codex":
		p := f.provider
		if p != "cursor" {
			p = "other"
		}
		return s + " --provider " + p + " --selection externally_managed"
	}
	s += " --provider " + f.provider + " --selection " + f.selection
	if f.selection == "explicit_directory" {
		dir := "ACCOUNT_DIRECTORY"
		if identifiers {
			dir = shellQuote(f.dir)
		}
		s += " --account-dir " + dir
	}
	return s
}

// dockMayBeSame lists the catalog entries with the same provider as an
// unnamed headless context and the same selection: the same directory, or
// both inherited. This is the comparison CheckRoute makes for a stated
// entry. A BB thread context is never compared: BB has no account per
// thread.
func dockMayBeSame(f *accounts.File, x dockAccountFacts) []string {
	if f == nil || (x.provider != "claude-code" && x.provider != "codex") {
		return nil
	}
	var ids []string
	for _, e := range f.Entries {
		if e.Provider != x.provider || e.Mode != x.selection {
			continue
		}
		if x.selection == "explicit_directory" && filepath.Clean(e.Directory) != x.dir {
			continue
		}
		if x.selection != "inherited" && x.selection != "explicit_directory" {
			continue
		}
		ids = append(ids, e.ID)
	}
	return ids
}

// dockAccountTitle is "Unnamed Codex account" for an unnamed context.
func dockAccountTitle(provider string) string {
	switch provider {
	case "claude-code":
		provider = "Claude Code"
	case "codex":
		provider = "Codex"
	case "cursor":
		provider = "Cursor"
	}
	return strings.TrimSpace("Unnamed " + provider + " account")
}
