package supervise

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// stopAgent is agent as a stop carried out by a command, or a lane count,
// runs it. Such a command may run with a shorter PATH than the loop's checks
// (the panel and the menu run with the system's). When the configured bb is
// a bare name not found on this PATH, the path the latest check recorded
// running (state.BBPath) is used instead, if it is still a safe executable.
// reachable is false when bb cannot be found from here at all.
func stopAgent(a config.Agent, st state.File) (agent config.Agent, reachable bool) {
	if a.Adapter != "bb" || a.BB == nil {
		return a, true
	}
	cli := a.BB.CLI
	if cli == "" {
		cli = config.DefaultBBCLI
	}
	if strings.ContainsRune(cli, '/') || strings.ContainsRune(cli, os.PathSeparator) || filepath.IsAbs(cli) {
		return a, true // a path the configuration names: used as it is
	}
	if _, err := exec.LookPath(cli); err == nil {
		return a, true
	}
	if !usableCLIPath(st.BBPath) {
		return a, false
	}
	bb := *a.BB
	bb.CLI = st.BBPath
	a.BB = &bb
	return a, true
}

// usableCLIPath reports whether a recorded executable may be run: an
// absolute path to a regular, executable file, owned by this user or root,
// and not writable by everyone. Its link, if it is one, is followed.
func usableCLIPath(p string) bool {
	if p == "" || !filepath.IsAbs(p) {
		return false
	}
	real, err := filepath.EvalSymlinks(p)
	if err != nil {
		return false
	}
	info, err := os.Stat(real)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0o002 != 0 {
		return false
	}
	return executableByOwner(info)
}

// StopReachable reports whether a stop carried out now, from this process,
// can reach the loop's active target: for a BB target, whether bb can be
// found here, configured or as the latest check recorded it.
func StopReachable(cfg *config.Config) bool {
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return true // the stop check says what is wrong
	}
	fb, _ := EffectiveRoutes(cfg, st)
	t := &tick{cfg: cfg, loop: cfg.Loop, fb: fb, st: st, res: &Result{}}
	_, agent := t.resolve(st.Target())
	_, ok := stopAgent(agent, st)
	return ok
}
