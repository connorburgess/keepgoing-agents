package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/snapshot"
	"github.com/looptender/looptender/internal/state"
)

// A registry publication is reference evidence only. In particular, registered
// does not establish a running daemon, successful check or provider access.
type registryPublication struct {
	State   string `json:"state"`
	ID      string `json:"id,omitempty"`
	Binding string `json:"binding,omitempty"`
	Source  string `json:"source"`
}

type registryBinding struct {
	cfg                           *config.Config
	path, runtime, target, digest string
	configInfo, runtimeInfo       os.FileInfo
}

var errRegistryBinding = errors.New("loop registration binding unavailable or changed; inspect the private configuration and current owner")

func captureRegistryBinding(path, expectedThread, caller string) (registryBinding, error) {
	var b registryBinding
	abs, err := filepath.Abs(path)
	if err != nil {
		return b, errRegistryBinding
	}
	canonical, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return b, errRegistryBinding
	}
	before, err := snapshot.Lstat(abs)
	if err != nil || before.Mode()&os.ModeSymlink != 0 {
		return b, errRegistryBinding
	}
	abs = canonical
	info, err := snapshot.Lstat(abs)
	if err != nil || !info.Mode().IsRegular() || info.Size() > 1<<20 {
		return b, errRegistryBinding
	}
	cfg, err := config.Load(abs)
	if err != nil || cfg.Loop.BB == nil || !transferThreadID.MatchString(expectedThread) {
		return b, errRegistryBinding
	}
	runtime, err := canonicalFuturePath(cfg.Loop.StateDir)
	if err != nil || runtime != cfg.Loop.StateDir {
		return b, errRegistryBinding
	}
	var ctl control.File
	var st state.File
	if readTransferRecord(runtime, control.FileName, &ctl) != nil || readTransferRecord(runtime, state.FileName, &st) != nil || ctl.PendingTransfer != nil || control.CheckOwner(ctl, caller, false) != nil {
		return b, errRegistryBinding
	}
	if ctl.Ownership == nil && (ctl.Enabled || (caller != "" && caller != expectedThread)) {
		return b, errRegistryBinding
	}
	if ctl.BBTarget != "" && ctl.BBTarget != cfg.Loop.BB.Thread {
		return b, errRegistryBinding
	}
	agent, ok := cfg.Loop.Agent, st.Target() == state.Primary
	if !ok {
		if candidate, found := effectiveRoutes(cfg, st).Find(st.Target()); found && !candidate.Disabled {
			agent, ok = st.CandidateAgent(candidate)
		}
	}
	if !ok || agent.BB == nil || agent.BB.Thread != expectedThread {
		return b, errRegistryBinding
	}
	runtimeInfo, err := snapshot.Lstat(runtime)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return b, errRegistryBinding
	}
	if err == nil && (!runtimeInfo.IsDir() || runtimeInfo.Mode()&os.ModeSymlink != 0) {
		return b, errRegistryBinding
	}
	if cfg.CheckSources() != nil {
		return b, errRegistryBinding
	}
	configID, err := registryNativeIdentity(abs, info)
	if err != nil {
		return b, errRegistryBinding
	}
	runtimeID := "missing"
	if runtimeInfo != nil {
		runtimeID, err = registryNativeIdentity(runtime, runtimeInfo)
		if err != nil {
			return b, errRegistryBinding
		}
	}
	// Only an opaque digest leaves this command. Private config/owner/target
	// values are neither registry storage fields nor ordinary client output.
	raw, _ := json.Marshal(struct {
		Path, Runtime, Loop, Sources, Target, Caller, ConfigID, RuntimeID string
		Control                                                           control.File
		Size, Modified                                                    int64
	}{abs, runtime, cfg.Loop.Name, cfg.SourcesDigest(), expectedThread, caller, configID, runtimeID, ctl, info.Size(), info.ModTime().UnixNano()})
	agentBytes, err := json.Marshal(struct {
		Alias string
		Agent config.Agent
	}{st.Target(), agent})
	if err != nil {
		return b, errRegistryBinding
	}
	raw = append(raw, agentBytes...)
	sum := sha256.Sum256(raw)
	return registryBinding{cfg: cfg, path: abs, runtime: runtime, target: expectedThread, digest: hex.EncodeToString(sum[:]), configInfo: info, runtimeInfo: runtimeInfo}, nil
}

// registerBound keeps registry -> tick -> control lock order, all nonwaiting.
// No provider/daemon/BB query, control write or runtime repair is performed.
func registerBound(dir string, b registryBinding, caller string) (registry.Entry, error) {
	return registerBoundWith(dir, b, caller, registry.AddChecked)
}

func registerBoundWith(dir string, b registryBinding, caller string, add func(string, string, string, string, func() error) (registry.Entry, error)) (registry.Entry, error) {
	// These releases belong to the whole publication, not just its check:
	// AddChecked still has to atomically save after the callback returns.
	var tick, ctl *lock.Lock
	defer func() {
		if ctl != nil {
			_ = ctl.Release()
		}
		if tick != nil {
			_ = tick.Release()
		}
	}()
	return add(dir, b.cfg.Loop.Name, b.path, b.runtime, func() error {
		if b.runtimeInfo != nil {
			var err error
			tick, err = lock.Acquire(b.runtime)
			if err != nil {
				return err
			}
			ctl, err = lock.AcquireNamed(b.runtime, lock.Control)
			if err != nil {
				return err
			}
		}
		fresh, err := captureRegistryBinding(b.path, b.target, caller)
		if err != nil || fresh.digest != b.digest || !os.SameFile(b.configInfo, fresh.configInfo) || (b.runtimeInfo == nil) != (fresh.runtimeInfo == nil) {
			return errRegistryBinding
		}
		if b.runtimeInfo != nil && !os.SameFile(b.runtimeInfo, fresh.runtimeInfo) {
			return errRegistryBinding
		}
		return nil
	})
}

func registryPublicationBinding(b registryBinding, dir string) string {
	sum := sha256.Sum256([]byte(b.digest + "\x00" + dir))
	return hex.EncodeToString(sum[:])
}

func registryReconcile(e env, args []string) int {
	fs := flag.NewFlagSet("registry reconcile", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "", "explicit existing BB loop configuration")
	directory := fs.String("directory", "", "private local registry")
	thread := fs.String("thread", "", "expected current BB target")
	provider := fs.String("provider", "", "expected current configured provider label")
	binding := fs.String("binding", "", "apply only the opaque binding returned by preview")
	asJSON := fs.Bool("json", false, "versioned minimized reference report")
	if fs.Parse(args) != nil || fs.NArg() != 0 || *path == "" || !transferThreadID.MatchString(*thread) || (*binding != "" && !validRegistryBinding(*binding)) {
		return exitUsage
	}
	dir, err := registryDirectory(*directory)
	if err != nil {
		e.errf("registry reconcile: private registry unavailable")
		return exitFailed
	}
	if e.ctx.Err() != nil {
		return exitFailed
	}
	caller := os.Getenv("BB_THREAD_ID")
	b, err := captureRegistryBinding(*path, *thread, caller)
	if err != nil || (*provider != "" && b.cfg.Loop.Provider != *provider) {
		e.errf("registry reconcile: %v; nothing published", errRegistryBinding)
		return exitFailed
	}
	for _, repo := range b.cfg.Loop.Progress.Repositories {
		root, err := canonicalFuturePath(repo)
		if err != nil || pathWithin(root, dir) {
			e.errf("registry reconcile: registry must be outside progress repositories")
			return exitFailed
		}
	}
	report := registryPublication{State: "preview", Binding: registryPublicationBinding(b, dir), Source: "cli_registry"}
	if *binding != "" {
		if *binding != report.Binding {
			e.errf("registry reconcile: preview binding changed; nothing published")
			return exitFailed
		}
		if e.ctx.Err() != nil {
			return exitFailed
		}
		entry, err := registerBound(dir, b, caller)
		if err != nil {
			e.errf("registry reconcile: publication unavailable; inspect reference status before retrying; loop controls unchanged")
			return exitFailed
		}
		report.State, report.ID = "registered", entry.ID
	}
	if *asJSON {
		return encodeRegistryPublication(e, report)
	}
	if report.State == "registered" {
		fmt.Fprintf(e.stdout, "Loop reference registered; supervision and controls unchanged. Registration does not establish operational health.\n")
	} else {
		fmt.Fprintf(e.stdout, "Preview only; no reference or loop changed. Apply with --binding %s.\n", report.Binding)
	}
	return exitOK
}
func validRegistryBinding(s string) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == sha256.Size
}
func encodeRegistryPublication(e env, p registryPublication) int {
	if json.NewEncoder(e.stdout).Encode(struct {
		SchemaVersion int                 `json:"schemaVersion"`
		Registration  registryPublication `json:"registration"`
	}{1, p}) != nil {
		return exitFailed
	}
	return exitOK
}
