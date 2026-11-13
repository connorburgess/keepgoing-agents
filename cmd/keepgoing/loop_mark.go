package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

type loopMarkReport struct {
	SchemaVersion int    `json:"schemaVersion"`
	Type          string `json:"type"`
	Loop          string `json:"loop"`
	// Applied is false for a preview and for a refusal.
	Applied bool `json:"applied"`
	// Marks are the directories that would be, or were, marked.
	Marks    []loopRemovalItem `json:"marks"`
	Refusals []loopRefusal     `json:"refusals"`
	Recorded bool              `json:"recorded"`
	Message  string            `json:"message"`
}

// loopMarkCommand marks an existing setup as one keepgoing may later remove.
// A setup created before `init` wrote markers, or assembled by hand, carries
// none, and keepgoing does not guess: a person says so here, once, after the
// same structural checks a removal makes. It deletes nothing and changes no
// file but the markers it writes.
func loopMarkCommand(e env, args []string) int {
	fs := flag.NewFlagSet("loop mark", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	cfgPath := fs.String("c", "keepgoing.yaml", "path to the config file")
	yes := fs.Bool("yes", false, "write the markers the preview lists; without it nothing is written")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	private := fs.Bool("private", false, "show private paths, and keep them in the record")
	registryDir := fs.String("registry", "", "private registry directory (default: the user's)")
	if err := fs.Parse(args); err != nil || fs.NArg() != 0 {
		if err == nil {
			e.errf("loop mark takes no arguments; name the loop with -c")
		}
		return exitUsage
	}
	path, err := filepath.Abs(*cfgPath)
	if err != nil {
		e.errf("loop mark: the configuration path cannot be resolved")
		return exitUsage
	}
	cfg, err := config.Load(path)
	if err != nil {
		e.errf("loop mark: the configuration could not be loaded, so what belongs to the loop is not known: %v", err)
		return exitUsage
	}
	report := loopMarkReport{SchemaVersion: 1, Type: "loop_mark", Loop: cfg.Loop.Name, Marks: []loopRemovalItem{}, Refusals: []loopRefusal{}}
	refuse := func(code, format string, a ...any) {
		report.Refusals = append(report.Refusals, loopRefusal{Code: code, Reason: fmt.Sprintf(format, a...)})
	}
	show := func(p string) string {
		if *private {
			return p
		}
		return ""
	}
	setupDir := filepath.Dir(path)
	type mark struct{ dir, kind string }
	var marks []mark

	// The setup directory, by the checks a removal makes.
	info, err := os.Lstat(setupDir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		refuse("not_a_directory", "the setup directory is a symlink or not a directory")
	}
	if insideCheckout(setupDir) {
		refuse("inside_checkout", "the setup directory is inside a Git checkout")
	}
	known := map[string]bool{"state": true}
	for _, name := range loopSetupFiles {
		known[name] = true
	}
	entries, _ := os.ReadDir(setupDir)
	var unexpected []string
	for _, entry := range entries {
		if !known[entry.Name()] || (entry.Name() != "state" && !entry.Type().IsRegular()) {
			unexpected = append(unexpected, entry.Name())
		}
	}
	if len(unexpected) > 0 {
		sort.Strings(unexpected)
		detail := fmt.Sprintf("%d entries", len(unexpected))
		if *private {
			detail = strings.Join(unexpected, ", ")
		}
		refuse("unexpected_entries", "the setup directory holds entries keepgoing did not write (%s); a directory with other content is not marked", detail)
	}
	var none errNoLoopMarker
	switch err := readLoopMarker(setupDir, cfg.Loop.Name); {
	case err == nil:
	case errors.As(err, &none):
		marks = append(marks, mark{setupDir, loopSetupMarker})
	default:
		refuse("marker_conflict", "%v; it is not replaced", err)
	}

	// The state directory: covered by the setup marker when it is the one
	// inside the setup directory, marked itself when it is somewhere else.
	state, nested := cfg.Loop.StateDir, filepath.Join(setupDir, "state")
	if _, err := os.Lstat(state); err == nil && filepath.Clean(state) != nested {
		for _, fault := range separateStateFaults(cfg, setupDir, state) {
			refuse("state_not_markable", "%s", fault)
		}
		switch err := readLoopMarkerOf(state, loopStateMarker, cfg.Loop.Name); {
		case err == nil:
		case errors.As(err, &none):
			marks = append(marks, mark{state, loopStateMarker})
		default:
			refuse("marker_conflict", "%v; it is not replaced", err)
		}
	}
	if _, err := os.Lstat(state); err == nil {
		if err := checkOwnership(state, nil); err != nil {
			refuse("not_owner", "ownership: %v", err)
		}
	}
	for _, m := range marks {
		what := "the setup directory, and the state directory inside it"
		if m.kind == loopStateMarker {
			what = "the state directory the configuration names"
		}
		report.Marks = append(report.Marks, loopRemovalItem{Kind: m.kind, What: what, Path: show(m.dir)})
	}

	emit := func(code int) int {
		if *asJSON {
			b, _ := json.MarshalIndent(report, "", "  ")
			fmt.Fprintln(e.stdout, string(b))
			return code
		}
		fmt.Fprintf(e.stdout, "%s: %s\n", report.Loop, report.Message)
		for _, m := range report.Marks {
			line := "  " + m.What
			if m.Path != "" {
				line += ": " + m.Path
			}
			fmt.Fprintln(e.stdout, line)
		}
		for _, r := range report.Refusals {
			fmt.Fprintf(e.stdout, "  refused: %s\n", r.Reason)
		}
		return code
	}
	switch {
	case len(report.Refusals) > 0:
		report.Marks = []loopRemovalItem{}
		report.Message = "not marked; nothing was changed"
		return emit(exitFailed)
	case len(marks) == 0:
		report.Message = "already marked as keepgoing's; nothing to do"
		return emit(exitOK)
	case !*yes:
		report.Message = "preview only, nothing was written. This would mark as keepgoing's own, so that keepgoing loop remove may later delete it:"
		return emit(exitOK)
	}
	for _, m := range marks {
		// Exclusive: a marker that appeared meanwhile is never overwritten.
		f, err := os.OpenFile(filepath.Join(m.dir, loopMarkerName), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			_, err = f.Write(newLoopMarkerOf(m.kind, cfg.Loop.Name))
			if closeErr := f.Close(); err == nil {
				err = closeErr
			}
		}
		if err != nil {
			report.Message = "a marker could not be written; run the same command again"
			return emit(exitFailed)
		}
	}
	report.Applied = true
	if dir, err := registryDirectory(*registryDir); err == nil {
		kinds := make([]string, 0, len(marks))
		for _, m := range marks {
			kinds = append(kinds, m.kind)
		}
		report.Recorded = appendLoopLog(dir, "mark", cfg.Loop.Name, path, *private, map[string]any{"marked": kinds})
	}
	report.Message = "marked as keepgoing's own. keepgoing loop remove can now delete what is listed, after its own checks:"
	return emit(exitOK)
}
