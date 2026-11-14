package main

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// The panel's Versions view: every place a keepgoing build runs on this
// computer, as the records say, compared with the newest build seen here. It
// is read only, and it never runs a binary to ask its version: a label comes
// from what a check, a daemon or a client recorded, and a service's file is
// compared with this panel's by content.

// productName is the one place the Versions view's words take the tool's
// name from, so the rename changes one line.
const productName = "keepgoing"

// The versions of the plugin and the menu that this build was released with.
// TestBundledComponentVersions keeps them equal to the plugin's package.json
// and the menu's VERSION file.
const (
	bundledPluginVersion = "0.3.1"
	bundledMenuVersion   = "0.1.0"
)

// dockVersions is the Versions view.
type dockVersions struct {
	// Newest is the newest plain release label among the places seen now
	// (this panel, each loop's last check and daemon) and the clients'
	// reports of the last seven days; or this panel's own label when none is
	// a plain release. Reference says which.
	Newest    string `json:"newest"`
	Reference string `json:"reference"` // newest_release, this_panel
	// Summary is the picker's line; Behind counts the rows that differ from
	// Newest: older, a different build, or a service on a different file.
	Summary string           `json:"summary"`
	Behind  int              `json:"behind"`
	Lede    string           `json:"lede"`
	Rows    []dockVersionRow `json:"rows"`
	// Unrecorded are the loops with nothing recorded about their builds: no
	// label from their last check, no daemon that answered and no service.
	// They are one line after the rows, not a row each.
	Unrecorded []dockVersionLoop `json:"unrecorded"`
	// Clients says whether the plugin's and menu's reports could be read.
	Clients string `json:"clients"` // available, none, unavailable
}

// dockVersionRow is one place a build runs. It carries no path.
type dockVersionRow struct {
	// Kind is panel, check, daemon, service, client or client_cli.
	Kind  string `json:"kind"`
	Title string `json:"title"`
	// Key is the loop's page key for check, daemon and service rows.
	Key   string `json:"key,omitempty"`
	Label string `json:"label,omitempty"`
	// Availability is current, older, different (which is newer is not
	// known), newer (a plugin or menu newer than this build expects),
	// unknown or not_recorded; for a service, same_file, different_file or
	// unknown; for a client not seen for seven days, stale.
	Availability string `json:"availability"`
	Text         string `json:"text"`
	// Update says how to bring it up to the newest build. Only for an older
	// row, and for a service on a different file when this panel is the
	// newest build: never advice to move to an older or unordered build.
	Update string `json:"update,omitempty"`
	// Seen is when a client last reported, in words.
	Seen string `json:"seen,omitempty"`
}

// dockVersionLoop names a loop in the Versions view.
type dockVersionLoop struct {
	Key  string `json:"key"`
	Name string `json:"name"`
}

// dockLoopVersions is what one loop's read found about the builds it runs,
// beyond its last check's label (dockLoop.Build).
type dockLoopVersions struct {
	// Daemon is the label a responding daemon reported; empty when none
	// answered.
	Daemon string `json:"daemon,omitempty"`
	// Service is the comparison of its service's recorded file with this
	// panel's binary: same_file, different_file or unknown; empty when no
	// service is recorded or the view was not asked for.
	Service string `json:"service,omitempty"`
}

// dockVersionAgainst words a label against the reference build.
func dockVersionAgainst(label, ref, refKind string) (string, string) {
	label = dockBuildLabel(label)
	refWords := "the newest build here (" + ref + ")"
	if refKind == "this_panel" {
		refWords = "this panel's build (" + ref + ")"
	}
	switch {
	case label == "":
		return "not_recorded", "Not recorded."
	case !dockBuildKnown(label):
		return "unknown", "A " + productName + " build with no version recorded (dev), so whether it is " + refWords + " is unknown."
	case !dockBuildKnown(ref):
		return "unknown", productName + " " + label + "; this panel has no version recorded (dev), so there is nothing to compare it with."
	case label == ref && strings.HasSuffix(label, "-dirty"):
		return "unknown", productName + " " + label + ", built with uncommitted changes, so whether it is the same build as " + refWords + " is unknown."
	case label == ref:
		if refKind == "this_panel" {
			return "current", productName + " " + label + ", the same build as this panel."
		}
		return "current", productName + " " + label + ", the newest build here."
	}
	if order, ok := dockReleaseOrder(label, ref); ok && order < 0 {
		return "older", productName + " " + label + ", older than " + refWords + "."
	}
	// A dev or dirty build on either side: different, and which is newer is
	// not known, so no advice follows.
	return "different", productName + " " + label + ", a different build from " + refWords + "; which of them is newer is not known."
}

// dockNewest is the newest plain release among labels, or the panel's own
// label when none is one.
func dockNewest(labels []string, panel string) (string, string) {
	newest := ""
	for _, l := range labels {
		l = dockBuildLabel(l)
		if _, ok := dockReleaseOrder(l, l); !ok {
			continue
		}
		if newest == "" {
			newest = l
		} else if o, _ := dockReleaseOrder(l, newest); o > 0 {
			newest = l
		}
	}
	if newest == "" {
		return dockBuildLabel(panel), "this_panel"
	}
	return newest, "newest_release"
}

// dockComponentAgainst words a plugin or menu version against the one this
// build expects. Their numbers are their own, never keepgoing's.
func dockComponentAgainst(name, v, bundled string) (string, string) {
	switch o, ok := dockReleaseOrder("v"+v, "v"+bundled); {
	case v == bundled:
		return "current", name + " " + v + ", the version this build expects."
	case ok && o < 0:
		return "older", name + " " + v + ", older than the " + bundled + " this build expects."
	case ok && o > 0:
		return "newer", name + " " + v + ", newer than this build expects (" + bundled + ")."
	default:
		return "different", name + " " + v + ", a different version from the " + bundled + " this build expects; which is newer is not known."
	}
}

var dockClientTitles = map[string]string{"bb-plugin": "BB plugin", "macos-menu": "macOS menu"}

// dockVersionsOf builds the view from the loops read and the clients'
// reports.
func dockVersionsOf(loops []dockLoop, clients *clientsFile, clientsErr error, now time.Time) dockVersions {
	fresh := func(c clientReport) bool { return now.Sub(c.SeenAt) < clientFresh }
	labels := []string{version}
	for _, l := range loops {
		labels = append(labels, l.Build.Recorded, l.Versions.Daemon)
	}
	if clients != nil {
		for _, c := range clients.Clients {
			if fresh(c) {
				labels = append(labels, c.CLIVersion)
			}
		}
	}
	ref, refKind := dockNewest(labels, version)
	v := dockVersions{Newest: ref, Reference: refKind, Rows: []dockVersionRow{}, Unrecorded: []dockVersionLoop{}, Clients: "none"}
	add := func(r dockVersionRow) {
		switch r.Availability {
		case "older", "different", "different_file":
			v.Behind++
		}
		if r.Availability != "older" && r.Availability != "different_file" {
			r.Update = ""
		}
		v.Rows = append(v.Rows, r)
	}
	a, t := dockVersionAgainst(version, ref, refKind)
	panelCurrent := a == "current"
	add(dockVersionRow{Kind: "panel", Title: "This panel", Label: dockBuildLabel(version), Availability: a, Text: t,
		Update: "Start the panel again from the newest build: " + productName + " panel stop, then " + productName + " panel, with that binary."})
	for _, l := range loops {
		a, t := dockVersionAgainst(l.Build.Recorded, ref, refKind)
		if a == "not_recorded" && l.Versions.Daemon == "" && l.Versions.Service == "" {
			v.Unrecorded = append(v.Unrecorded, dockVersionLoop{Key: l.Key, Name: l.Name})
			continue
		}
		add(dockVersionRow{Kind: "check", Title: l.Name + ": last check", Key: l.Key, Label: l.Build.Recorded, Availability: a, Text: t,
			Update: "The next check runs whatever binary its scheduler starts: update that binary to the newest build."})
		if l.Versions.Daemon != "" {
			a, t := dockVersionAgainst(l.Versions.Daemon, ref, refKind)
			add(dockVersionRow{Kind: "daemon", Title: l.Name + ": daemon", Key: l.Key, Label: l.Versions.Daemon, Availability: a, Text: t,
				Update: "Restart its daemon from the newest build: it keeps running the binary it started with until then."})
		}
		if l.Versions.Service != "" {
			r := dockVersionRow{Kind: "service", Title: l.Name + ": service", Key: l.Key, Availability: l.Versions.Service}
			switch l.Versions.Service {
			case "same_file":
				r.Text = "Its service runs the same file as this panel."
			case "different_file":
				r.Text = "Its service runs a different file from this panel. Which build that is is not known: " + productName + " does not run it to ask."
				if panelCurrent && refKind == "newest_release" {
					r.Update = "Replace the file its service runs with this panel's build, the newest here, then restart the service."
				}
			default:
				r.Text = "Whether its service runs the same file as this panel is unknown: the recorded file could not be read."
			}
			add(r)
		}
	}
	switch {
	case clientsErr != nil:
		v.Clients = "unavailable"
	case clients != nil && len(clients.Clients) > 0:
		v.Clients = "available"
		for _, c := range clients.Clients {
			name := dockClientTitles[c.Name]
			bundled, update := bundledPluginVersion, "Update the plugin in BB with bb plugin update keepgoing, or install it again from the newest build's source."
			cliUpdate := "Set the plugin's keepgoingPath to the newest build."
			if c.Name == "macos-menu" {
				bundled, update = bundledMenuVersion, "Build the menu again from the newest build's source with integrations/macos/scripts/make-app.sh, then reopen it."
				cliUpdate = "Start the menu with --executable set to the newest build."
			}
			if !fresh(c) {
				// Not seen for a week: shown, counted for nothing.
				add(dockVersionRow{Kind: "client", Title: name, Label: c.ClientVersion, Availability: "stale",
					Text: name + " " + c.ClientVersion + ", not seen since " + c.SeenAt.Format("2 Jan 2006") + "."})
				continue
			}
			seen := "Last reported " + dockAgo(now, c.SeenAt) + "."
			a, t := dockComponentAgainst(name, c.ClientVersion, bundled)
			add(dockVersionRow{Kind: "client", Title: name, Label: c.ClientVersion, Availability: a, Text: t, Update: update, Seen: seen})
			a, t = dockVersionAgainst(c.CLIVersion, ref, refKind)
			add(dockVersionRow{Kind: "client_cli", Title: name + ": the " + productName + " it runs", Label: dockBuildLabel(c.CLIVersion), Availability: a, Text: t, Update: cliUpdate, Seen: seen})
		}
	}
	switch {
	case v.Behind == 0 && refKind == "newest_release":
		v.Summary = "All on " + ref
	case v.Behind == 0:
		v.Summary = "Nothing different found"
	default:
		v.Summary = dockPlural(v.Behind, "place differs", "places differ") + " from " + ref
	}
	v.Lede = "Where each " + productName + " build on this computer runs, as its records say, against " + map[string]string{"newest_release": "the newest release seen here, " + ref, "this_panel": "this panel's own build, " + ref}[refKind] + ". Nothing is run to ask a version, and nothing is fetched from the network."
	if v.Clients == "none" {
		v.Lede += " The BB plugin and the macOS menu are listed once they report, with a build that knows --client."
	}
	return v
}

// dockServiceFiles compares a loop's service file with this panel's binary
// by content, never running either. A file is hashed again only when its
// size or modification time changes.
type dockServiceFiles struct {
	mu   sync.Mutex
	seen map[string]dockFileDigest
}

type dockFileDigest struct {
	size  int64
	mod   time.Time
	sum   [sha256.Size]byte
	state string
}

func (c *dockServiceFiles) digest(ctx context.Context, path string) ([sha256.Size]byte, string) {
	info, err := os.Stat(path)
	if err != nil {
		return serviceBinaryDigest(ctx, path)
	}
	c.mu.Lock()
	if c.seen == nil {
		c.seen = map[string]dockFileDigest{}
	}
	d, ok := c.seen[path]
	c.mu.Unlock()
	if ok && d.size == info.Size() && d.mod.Equal(info.ModTime()) {
		return d.sum, d.state
	}
	sum, state := serviceBinaryDigest(ctx, path)
	c.mu.Lock()
	c.seen[path] = dockFileDigest{size: info.Size(), mod: info.ModTime(), sum: sum, state: state}
	c.mu.Unlock()
	return sum, state
}

// compare says whether the loop's recorded service file is this panel's.
func (c *dockServiceFiles) compare(ctx context.Context, stateDir string) string {
	h := localServiceHost()
	p, exists, err := readServicePlan(h, stateDir)
	if err != nil {
		if exists {
			return "unknown"
		}
		return ""
	}
	if !exists {
		return ""
	}
	self, err := os.Executable()
	if err != nil {
		return "unknown"
	}
	if resolved, err := filepath.EvalSymlinks(self); err == nil {
		self = resolved
	}
	a, sa := c.digest(ctx, p.Executable)
	b, sb := c.digest(ctx, self)
	switch {
	case sa != "available" || sb != "available":
		return "unknown"
	case a == b:
		return "same_file"
	default:
		return "different_file"
	}
}
