package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/envvar"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/userdir"
)

type registrySummary struct {
	ID         string `json:"id"`
	Loop       string `json:"loop"`
	ConfigPath string `json:"configPath,omitempty"`
	StateDir   string `json:"stateDir,omitempty"`
}
type registryOutput struct {
	SchemaVersion int               `json:"schemaVersion"`
	Operation     string            `json:"operation"`
	Entries       []registrySummary `json:"entries"`
	RemovedID     string            `json:"removedId,omitempty"`
}

func registryCommand(e env, args []string) int {
	if len(args) == 0 {
		e.errf("registry needs add, list, or remove")
		return exitUsage
	}
	op := args[0]
	if op != "add" && op != "list" && op != "remove" {
		e.errf("registry needs add, list, or remove")
		return exitUsage
	}
	fs := flag.NewFlagSet("registry "+op, flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	directory := fs.String("directory", "", "private registry directory (default: the registry in the user configuration directory)")
	cfgPath := fs.String("c", "", "add: existing loop config")
	id := fs.String("id", "", "remove: registration ID")
	private := fs.Bool("private", false, "list: include private config and runtime paths")
	asJSON := fs.Bool("json", false, "print versioned JSON")
	if fs.Parse(args[1:]) != nil {
		return exitUsage
	}
	if fs.NArg() != 0 || (op == "add" && (*cfgPath == "" || *id != "" || *private)) || (op == "list" && (*cfgPath != "" || *id != "")) || (op == "remove" && (*id == "" || *cfgPath != "" || *private)) {
		e.errf("registry: invalid flags for this operation")
		return exitUsage
	}
	fail := func(err error) int {
		e.errf("registry: %v", err)
		if errors.Is(err, lock.ErrHeld) {
			return exitSkipped
		}
		return exitFailed
	}
	dir, err := registryDirectory(*directory)
	if err != nil {
		return fail(err)
	}
	output := registryOutput{SchemaVersion: 1, Operation: op, Entries: []registrySummary{}}
	switch op {
	case "list":
		f, err := registry.Load(dir)
		if err != nil {
			return fail(err)
		}
		for _, entry := range f.Entries {
			summary := registrySummary{ID: entry.ID, Loop: entry.Loop}
			if *private {
				summary.ConfigPath, summary.StateDir = entry.ConfigPath, entry.StateDir
			}
			output.Entries = append(output.Entries, summary)
		}
	case "add":
		// Preserve the config spelling so relative paths keep the same meaning
		// as ordinary CLI commands, including through aliased parent directories.
		path, err := filepath.Abs(*cfgPath)
		if err != nil {
			return fail(errors.New("config unavailable; inspect it privately"))
		}
		info, err := os.Lstat(path)
		if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
			return fail(errors.New("config must be a regular non-symlink file of at most 1 MiB"))
		}
		cfg, err := config.Load(path)
		if err != nil {
			return fail(errors.New("config unavailable or invalid; inspect it privately"))
		}
		stateDir, err := canonicalFuturePath(cfg.Loop.StateDir)
		if err != nil {
			return fail(errors.New("runtime path unavailable"))
		}
		// Progress roots can be projects without .git yet. Do not write registry
		// metadata into any explicitly configured repository, either.
		for _, repo := range cfg.Loop.Progress.Repositories {
			root, err := canonicalFuturePath(repo)
			if err != nil || pathWithin(root, dir) {
				return fail(errors.New("registry must be outside the loop's progress repositories"))
			}
		}
		if err := cfg.CheckSources(); err != nil {
			return fail(errors.New("config changed while registering; retry with the current file"))
		}
		entry, err := registry.Add(dir, cfg.Loop.Name, path, stateDir)
		if err != nil {
			return fail(err)
		}
		recordRegistrationInExistingGraph(entry.StateDir, entry.ID)
		output.Entries = append(output.Entries, registrySummary{ID: entry.ID, Loop: entry.Loop})
	case "remove":
		if err := registry.Remove(dir, *id); err != nil {
			return fail(err)
		}
		output.RemovedID = *id
	}
	if *asJSON {
		if json.NewEncoder(e.stdout).Encode(output) != nil {
			return exitFailed
		}
	} else if op == "remove" {
		fmt.Fprintln(e.stdout, "Reference removed; supervision and loop files are unchanged.")
	} else {
		fmt.Fprintln(e.stdout, "Local loop references (registration does not establish supervision):")
		for _, entry := range output.Entries {
			fmt.Fprintf(e.stdout, "%s  %s\n", entry.ID, entry.Loop)
			if *private {
				fmt.Fprintf(e.stdout, "  config: %s\n  state: %s\n", entry.ConfigPath, entry.StateDir)
			}
		}
	}
	return exitOK
}

// registryDirEnv names a private registry to use when no --directory is
// given. It is how tests keep every command away from the person's own
// registry; it changes nothing else. The variable's earlier name,
// KEEPGOING_REGISTRY_DIR, is still read; see envvar.
const registryDirEnv = envvar.Prefix + string(envvar.RegistryDir)

func registryDirectory(path string) (string, error) {
	bad := errors.New("registry directory must be a private path outside Git checkouts")
	if path == "" {
		// The two names set to different directories are refused: a command
		// that honoured one would not see the loops registered in the other.
		var err error
		if path, _, err = envvar.RegistryDir.Path(); err != nil {
			return "", err
		}
	}
	if path == "" {
		var err error
		if path, err = userdir.Config("registry"); err != nil {
			return "", bad
		}
	}
	dir, err := canonicalFuturePath(path)
	if err != nil {
		return "", bad
	}
	for p := dir; ; p = filepath.Dir(p) {
		if _, err := os.Lstat(filepath.Join(p, ".git")); err == nil || !errors.Is(err, os.ErrNotExist) {
			return "", bad
		}
		if filepath.Dir(p) == p {
			break
		}
	}
	if registry.CheckDirectory(dir) != nil {
		return "", bad
	}
	return dir, nil
}

// registriesUnderBothNames returns the registry commands use and, when that
// is the default one, the registry under the tool's other directory name if
// it exists. The second is only ever read, to find loops: see userdir for
// when the name in use can change.
func registriesUnderBothNames() []string {
	dir, err := registryDirectory("")
	if err != nil {
		return nil
	}
	dirs := []string{dir}
	if named, _, err := envvar.RegistryDir.Path(); err != nil || named != "" {
		return dirs // a registry a person chose has no other name
	}
	base, err := userdir.ConfigHome()
	if err != nil {
		return dirs
	}
	for _, name := range []string{userdir.Name, userdir.LegacyName} {
		other, err := canonicalFuturePath(filepath.Join(base, name, "registry"))
		if err != nil || other == dir {
			continue
		}
		if info, err := os.Lstat(other); err == nil && info.IsDir() && registry.CheckDirectory(other) == nil {
			dirs = append(dirs, other)
		}
	}
	return dirs
}
