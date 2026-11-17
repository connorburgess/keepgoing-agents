package main

import (
	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/config"
)

// routeAccountCatalog reads the accounts catalog to compare the accounts
// routes name with it. It is nil when the catalog cannot be read; that never
// stops a loop. A catalog that does not exist yet is read as empty. dir is a
// test or private override; empty means the default directory.
func routeAccountCatalog(dir string) *accounts.File {
	path, err := accountsDirectory(dir)
	if err != nil {
		return nil
	}
	f, err := accounts.Load(path)
	if err != nil {
		return nil
	}
	return &f
}

// routeAccountsNamed reports whether any route of the loop names an account,
// so the catalog is read only when there is something to compare.
func routeAccountsNamed(cfg *config.Config) bool {
	if cfg.Loop.Account != "" {
		return true
	}
	if fb := currentRoutes(cfg); fb != nil {
		for _, c := range fb.Candidates {
			if c.Account != "" {
				return true
			}
		}
	}
	return false
}

// routeAccountOf is what status and the panel show of a route's account. The
// catalog is read only for a route that names an entry.
func routeAccountOf(a config.Agent, catalogDir string) *accounts.RouteAccount {
	var f *accounts.File
	if a.Account != "" {
		f = routeAccountCatalog(catalogDir)
	}
	r := accounts.CheckRoute(f, a)
	return &r
}

// routeAccountChecks compares each route that names an account with the
// catalog: the primary, then every candidate in the loop's routes, switched
// off or not. It returns one result per route that names an account, in
// order, and whether the catalog could be read.
func routeAccountChecks(cfg *config.Config, catalogDir string) (results []routeAccountResult, readable bool) {
	if !routeAccountsNamed(cfg) {
		return nil, true
	}
	f := routeAccountCatalog(catalogDir)
	check := func(id string, a config.Agent) {
		if a.Account != "" {
			results = append(results, routeAccountResult{ID: id, RouteAccount: accounts.CheckRoute(f, a)})
		}
	}
	check("primary", cfg.Loop.Agent)
	if fb := currentRoutes(cfg); fb != nil {
		for _, c := range fb.Candidates {
			check(c.ID, c.Agent)
		}
	}
	return results, f != nil
}

// routeAccountResult is one route's check, with the route's id in its list.
type routeAccountResult struct {
	ID string
	accounts.RouteAccount
}

// dockCatalogAccount is what the panel data carries of a route's account. It
// compares with the catalog read for the report and reads nothing itself.
func dockCatalogAccount(in *dockChainInput, a config.Agent) *accounts.RouteAccount {
	r := accounts.CheckRoute(in.Catalog, a)
	return &r
}

// dockLinkAccountRoutes says on each account card whether a route in the
// report names it, from the catalogAccount of each loop's chain entries. A
// card no route names reads none only when every loop and every route of
// each was read; otherwise unknown.
func dockLinkAccountRoutes(v *accounts.View, r dockReport) {
	named := map[string]bool{}
	complete := r.Complete && r.Registry.Availability == "available" && r.Registry.Omitted == 0
	for _, l := range r.Loops {
		if l.Chain.Availability == "unknown" || l.Chain.Omitted > 0 {
			complete = false
		}
		for _, e := range l.Chain.Entries {
			if a := e.CatalogAccount; a != nil && a.Assigned == "by_you" && a.Account != "" {
				named[a.Account] = true
			}
		}
	}
	accounts.LinkRoutes(v, named, complete)
}
