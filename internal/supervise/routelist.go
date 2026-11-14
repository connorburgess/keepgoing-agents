package supervise

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"gopkg.in/yaml.v3"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// RouteNote says where a loop's other routes come from and whether anything
// about them needs a person's attention. It is absent for a loop with a list
// of its own that needs none.
type RouteNote struct {
	Source string `json:"source"`
	// Unreadable: the loop follows the standard list, which cannot be read, so
	// it has no other routes and will not hand off.
	Unreadable bool `json:"unreadable,omitempty"`
	// CurrentNotInList: the loop is on a route that is no longer in its list.
	// It stays there until the ordinary rules move it.
	CurrentNotInList bool `json:"currentNotInList,omitempty"`
	// ListChanged: the standard list changed since this loop's last check.
	// It is a timeline record only; nothing is reported.
	ListChanged bool `json:"listChanged,omitempty"`
}

// CurrentRouteName is the saved record of the route a following loop is on.
const CurrentRouteName = "route-current.yaml"

const currentRouteLimit = 64 << 10

// currentRoute holds the template of the route only, never the resolved agent:
// the loop's own environment, which may hold secrets, stays in keepgoing.yaml.
type currentRoute struct {
	Version int          `yaml:"version"`
	Route   config.Route `yaml:"route"`
}

// LoadCurrentRoute reads the saved template. A missing, oversized or
// malformed record is simply no record.
func LoadCurrentRoute(stateDir string) (config.Route, bool) {
	f, err := snapshot.OpenRecord(filepath.Join(stateDir, CurrentRouteName))
	if err != nil {
		return config.Route{}, false
	}
	defer f.Close()
	raw, err := io.ReadAll(io.LimitReader(f, currentRouteLimit+1))
	if err != nil || len(raw) > currentRouteLimit {
		return config.Route{}, false
	}
	var rec currentRoute
	dec := yaml.NewDecoder(bytes.NewReader(raw))
	dec.KnownFields(true)
	if dec.Decode(&rec) != nil || rec.Version != 1 || rec.Route.ID == "" || rec.Route.ID == state.Primary {
		return config.Route{}, false
	}
	return rec.Route, true
}

func saveCurrentRoute(stateDir string, rt config.Route) error {
	raw, err := yaml.Marshal(currentRoute{Version: 1, Route: rt})
	if err != nil || len(raw) > currentRouteLimit {
		return errors.New("current route record could not be written")
	}
	path := filepath.Join(stateDir, CurrentRouteName)
	if old, err := os.ReadFile(path); err == nil && bytes.Equal(old, raw) {
		return nil
	}
	if err := os.MkdirAll(stateDir, 0o700); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(stateDir, ".route-*")
	if err != nil {
		return err
	}
	name := tmp.Name()
	defer os.Remove(name)
	if _, err = tmp.Write(raw); err == nil {
		err = tmp.Sync()
	}
	if closeErr := tmp.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	return snapshot.Replace(name, path)
}

// EffectiveRoutes returns the fallbacks a loop works with now and whether its
// current route is outside its list. A loop that follows the standard list and
// is on a route that list no longer resolves (it was removed, switched off, no
// longer applies, or the list cannot be read) keeps that route from its saved
// record, so it can go on observing, stopping and finishing there.
func EffectiveRoutes(cfg *config.Config, st state.File) (*config.Fallbacks, bool) {
	fb := cfg.FallbackSet
	target := st.Target()
	if !cfg.Loop.FollowsStandard() || target == state.Primary {
		return fb, false
	}
	if c, ok := fb.Find(target); ok && !c.Disabled {
		return fb, false
	}
	rt, ok := LoadCurrentRoute(cfg.Loop.StateDir)
	if !ok || rt.ID != target {
		return fb, false
	}
	// The saved template is resolved again for this loop as it is configured
	// now, exactly as the standard list would resolve it: never another
	// adapter, command, directory or executable, whatever the file says.
	c, ok := config.ResolveSaved(cfg.Loop, rt, fb)
	if !ok {
		return fb, false
	}
	return config.WithCurrentRoute(fb, c), true
}

// keepCurrentRoute applies EffectiveRoutes to this check.
func (t *tick) keepCurrentRoute() {
	if !t.loop.FollowsStandard() {
		return
	}
	fb, outside := EffectiveRoutes(t.cfg, t.st)
	t.fb, t.routeOutsideList = fb, outside
	if outside {
		t.warn("current route %q is no longer in this loop's route list; staying on it until the ordinary rules move the loop", t.st.Target())
	}
}

// saveRouteRecord saves the record of the route a following loop ends this
// check on. It runs before state.json is saved, so a target that changed
// during the check (a handoff, a switch-back, a reconciled conversation) has
// its record before any later check can need it.
func (t *tick) saveRouteRecord() {
	if t.opts.DryRun {
		return
	}
	if err := SaveCurrentRoute(t.cfg, t.fb, t.st.Target()); err != nil {
		t.warn("could not save the current route record: %v", err)
	}
}

// SaveCurrentRoute saves the record of target for a loop that follows the
// standard list, from the routes the caller is using. It does nothing for
// another loop, for the primary, or for a target those routes do not hold.
func SaveCurrentRoute(cfg *config.Config, fb *config.Fallbacks, target string) error {
	if !cfg.Loop.FollowsStandard() || target == state.Primary {
		return nil
	}
	c, ok := fb.Find(target)
	if !ok {
		return nil
	}
	rt, ok := config.RouteOf(cfg.Loop, c)
	if !ok {
		return errors.New("the route cannot be described as a standard route")
	}
	return saveCurrentRoute(cfg.Loop.StateDir, rt)
}

// noteRouteList records the route-list picture in the result and reports,
// once each, the start and the end of an unreadable standard list.
func (t *tick) noteRouteList() {
	list := t.cfg.RouteList
	if list.Source != config.RoutesStandard {
		// A loop that no longer follows the list has nothing to report.
		if !t.opts.DryRun {
			t.st.RoutesUnreadable, t.st.RoutesDigest = false, ""
		}
		return
	}
	for _, w := range list.Warnings {
		t.warn("%s", w)
	}
	// A change to a readable list is noted once, against the list this loop
	// last read; the first read, an unreadable list and a dry run note
	// nothing, and a dry run leaves the note to the next real check.
	changed := !t.opts.DryRun && list.Digest != "" && t.st.RoutesDigest != "" && list.Digest != t.st.RoutesDigest
	if list.Digest != "" && !t.opts.DryRun {
		t.st.RoutesDigest = list.Digest
	}
	t.incidentRouteListChanged = changed
	if list.Unreadable || t.routeOutsideList || changed {
		t.res.Routes = &RouteNote{Source: list.Source, Unreadable: list.Unreadable, CurrentNotInList: t.routeOutsideList, ListChanged: changed}
	}
	if list.Unreadable {
		t.warn("%s", config.StandardRoutesUnreadable)
	}
	if list.Unreadable == t.st.RoutesUnreadable || t.opts.DryRun {
		return
	}
	key := "routes-readable"
	if list.Unreadable {
		key = "routes-unreadable"
	}
	if !t.reserveReport(key) {
		return // tried again at the next check
	}
	t.st.RoutesUnreadable = list.Unreadable
	data := map[string]any{"reason": "standard_route_list_unreadable", "target": t.res.Target}
	if list.Unreadable {
		t.report("degraded", fmt.Sprintf("%s: %s; the loop stays on its current route and will not hand off", t.loop.Name, config.StandardRoutesUnreadable), data)
	} else {
		t.report("recovered", fmt.Sprintf("%s: the standard route list is readable again", t.loop.Name), data)
	}
}
