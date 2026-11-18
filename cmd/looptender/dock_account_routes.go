package main

import (
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

// dockAccountUse is a selected catalog entry (Card), or a configuration
// context for routes with no explicit catalog binding (Context). A context
// does not identify an account or establish a separate subscription. It is
// built from loop configurations and the catalog only: nothing on the computer is searched and nothing is adopted.
type dockAccountUse struct {
	Card    string `json:"card,omitempty"`
	Context string `json:"context,omitempty"`
	// For an unnamed context: what its routes state. Selection is
	// inherited, explicit_directory, bb_thread or externally_managed.
	// Directory is sent only with identifiers. Add is the accounts add
	// command that would name it; it changes no route.
	Provider  string             `json:"provider,omitempty"`
	Selection string             `json:"selection,omitempty"`
	Group     string             `json:"group,omitempty"`
	Directory string             `json:"directory,omitempty"`
	Add       string             `json:"add,omitempty"`
	Title     string             `json:"title,omitempty"`
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
	Route    string `json:"route"`
	Title    string `json:"title"`
	BBThread bool   `json:"bbThread,omitempty"`
	// CheckText is set when the route names this entry but its check found
	// a mismatch (accounts.RouteCheckText). Its reading is then left out of
	// the account's summary.
	CheckText string   `json:"checkText,omitempty"`
	Loops     []string `json:"loops"`
	Omitted   int      `json:"omitted,omitempty"`
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
				rt := dockAccountRoute{Route: e.Route, Title: clip(title, 80), BBThread: e.acct.selection == "bb_thread", Loops: []string{}}
				if a.Check == accounts.RouteProviderMismatch || a.Check == accounts.RouteDirectoryMismatch {
					rt.CheckText = accounts.RouteCheckText(a.Check)
				}
				x.use.Routes = append(x.use.Routes, rt)
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
		var readings []dockRouteReading
		for _, rt := range u.Routes {
			if t, ok := usage[rt.Route]; ok && rt.CheckText == "" {
				readings = append(readings, dockRouteReading{title: rt.Title, usage: t})
			}
		}
		u.Reading, u.Out = dockAccountReading(readings, len(u.Routes)+u.Omitted, now)
		if u.Context != "" {
			u.Provider, u.Selection, u.Group = clip(x.facts.provider, dockMaxText), x.facts.selection, clip(x.facts.group, dockMaxText)
			// A directory too long to send is left as the placeholder.
			show := identifiers && len(x.facts.dir) <= dockMaxAccountDir
			if show {
				u.Directory = x.facts.dir
			}
			u.Title = dockAccountTitle(u.Provider)
			u.Add = dockAccountAddCommand(x.facts, show)
		}
		out = append(out, u)
	}
	return out, omitted
}

// dockRouteReading is one route's own reading, with the route's title.
type dockRouteReading struct {
	title string
	usage dockUsageTarget
}

// dockAccountReading is an account's summary line. If any route's reading
// has a window used up that has not ended, it says the newest such reading,
// naming the route when the account has more than one; otherwise whether
// the newest reading was out. Readings are never merged. routes counts
// every route of the account, listed or not.
func dockAccountReading(rs []dockRouteReading, routes int, now time.Time) (dockText, bool) {
	if routes == 0 {
		return nil, false
	}
	var newest, out *dockRouteReading
	var outWin *dockUsageWindow
	for i := range rs {
		r := &rs[i]
		if r.usage.Availability == "unavailable" || r.usage.ObservedAt == nil {
			continue
		}
		if newest == nil || r.usage.ObservedAt.After(*newest.usage.ObservedAt) {
			newest = r
		}
		if w := dockUsedUpWindow(r.usage); (w != nil || len(r.usage.UsedUp) > 0) && (out == nil || r.usage.ObservedAt.After(*out.usage.ObservedAt)) {
			out, outWin = r, w
		}
	}
	switch {
	case out != nil:
		read := dockAgo(now, *out.usage.ObservedAt)
		var s dockText
		if routes > 1 {
			s = txt(out.title + ": ")
		}
		if outWin == nil {
			// The used-up window is past the cap of windows shown.
			return txt(s, out.usage.UsedUp), true
		}
		s = txt(s, dockCapital(outWin.Name)+" allowance was out when read "+read)
		if outWin.ResetsAt != nil {
			return txt(s, " · resets ", dockUntil(now, *outWin.ResetsAt)), true
		}
		return txt(s, " · no reset time was reported"), true
	case newest == nil:
		if routes == 1 {
			return txt("No usage reading is recorded for its route."), false
		}
		return txt("No usage reading is recorded for its routes."), false
	case newest.usage.Availability == "stale":
		return txt("Not out when read " + dockAgo(now, *newest.usage.ObservedAt) + "; every window it describes has ended since"), false
	}
	return txt("Not out when read " + dockAgo(now, *newest.usage.ObservedAt)), false
}

// dockUsedUpWindow is the first window of a reading that was used up and
// has not ended since, or nil.
func dockUsedUpWindow(u dockUsageTarget) *dockUsageWindow {
	for i, w := range u.Windows {
		if !w.Stale && w.UsedPercent >= 100 {
			return &u.Windows[i]
		}
	}
	return nil
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

// dockAccountTitle describes routes without asserting an account identity.
func dockAccountTitle(provider string) string {
	switch provider {
	case "claude-code":
		provider = "Claude Code"
	case "codex":
		provider = "Codex"
	case "cursor":
		provider = "Cursor"
	}
	return strings.TrimSpace(provider + " routes (account unknown)")
}
