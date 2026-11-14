// Package envvar names the tool's environment variables. Each has two names:
// LOOPTENDER_X, and KEEPGOING_X from before the tool was renamed. The new
// name is read first and the old one is still honoured. A variable set to
// the empty string counts as unset, under either name.
package envvar

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
)

const (
	// Prefix starts every variable's name.
	Prefix = "LOOPTENDER_"
	// LegacyPrefix starts the name each variable had before the rename.
	LegacyPrefix = "KEEPGOING_"
)

// Var is one variable, written as the part of its name after the prefix.
type Var string

// Variables a person sets.
const (
	SessionDir    Var = "SESSION_DIR"    // where session claims are kept
	RegistryDir   Var = "REGISTRY_DIR"   // the loop registry
	RoutesFile    Var = "ROUTES_FILE"    // the standard route list
	PanelOpener   Var = "PANEL_OPENER"   // a program that opens the panel's page
	NetProbe      Var = "NET_PROBE"      // a command whose exit 0 means online
	NetProbeURL   Var = "NET_PROBE_URL"  // an address that must answer
	WebhookURL    Var = "WEBHOOK_URL"    // the default report.url_env
	WebhookSecret Var = "WEBHOOK_SECRET" // the default report.secret_env
)

// Variables the tool sets for the commands it runs.
const (
	Mode       Var = "MODE"        // auto or queue, for a resume command
	ProbeTier  Var = "PROBE_TIER"  // scheduled, exact or manual, for a probe command
	ProbeModel Var = "PROBE_MODEL" // the model a probe should ask
	Candidate  Var = "CANDIDATE"   // the fallback being probed
)

// Set lists the variables a person sets, in the order doctor reports them.
var Set = []Var{SessionDir, RegistryDir, RoutesFile, PanelOpener, NetProbe, NetProbeURL, WebhookURL, WebhookSecret}

// Locations are the variables that name a file or directory every command
// must agree on. Their two names may not disagree; see Path.
var Locations = []Var{SessionDir, RegistryDir, RoutesFile}

// Name is the variable's name.
func (v Var) Name() string { return Prefix + string(v) }

// Legacy is the name the variable had before the rename.
func (v Var) Legacy() string { return LegacyPrefix + string(v) }

// Lookup returns the variable's value and the name it was read from: the new
// name when it is set, otherwise the old one. Both are empty when neither is
// set.
func (v Var) Lookup() (value, name string) {
	if s := os.Getenv(v.Name()); s != "" {
		return s, v.Name()
	}
	if s := os.Getenv(v.Legacy()); s != "" {
		return s, v.Legacy()
	}
	return "", ""
}

// Get returns the variable's value, or "" when it is not set.
func (v Var) Get() string {
	s, _ := v.Lookup()
	return s
}

// Disagrees reports whether both names are set, to different values.
func (v Var) Disagrees() bool {
	a, b := os.Getenv(v.Name()), os.Getenv(v.Legacy())
	return a != "" && b != "" && a != b
}

// ErrDisagree is returned by Path when a location's two names are set to
// different places.
var ErrDisagree = errors.New("set to different places")

// Path is Lookup for a variable that names a file or directory. Two names
// set to different places are an error wrapping ErrDisagree rather than a
// choice: two commands that each honoured one of them would work in
// different places without saying so. Paths that differ only in how they are
// written are the same place.
func (v Var) Path() (value, name string, err error) {
	a, b := os.Getenv(v.Name()), os.Getenv(v.Legacy())
	if a != "" && b != "" && filepath.Clean(a) != filepath.Clean(b) {
		return "", "", &disagreement{v}
	}
	value, name = v.Lookup()
	return value, name, nil
}

type disagreement struct{ v Var }

func (d *disagreement) Error() string {
	return d.v.Name() + " and " + d.v.Legacy() + " are " + ErrDisagree.Error() + "; unset one of them"
}

func (d *disagreement) Unwrap() error { return ErrDisagree }

// Pairs returns NAME=value under both names, for the environment of a
// command the tool runs: a script written for either name reads it.
func (v Var) Pairs(value string) []string {
	return []string{v.Name() + "=" + value, v.Legacy() + "=" + value}
}

// Both sets value under both names in a map of variables for a command.
func (v Var) Both(into map[string]string, value string) map[string]string {
	if into == nil {
		into = map[string]string{}
	}
	into[v.Name()], into[v.Legacy()] = value, value
	return into
}

// LegacyOf returns the old name for the new name of a variable a person
// sets. A name that is not one of those, including an old name, has none.
func LegacyOf(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, Prefix)
	if !ok {
		return "", false
	}
	for _, v := range Set {
		if string(v) == rest {
			return v.Legacy(), true
		}
	}
	return "", false
}
