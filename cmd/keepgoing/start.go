package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/daemoninfo"
	"github.com/connorburgess/keepgoing-agents/internal/health"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

type startReady func(control.File) error

func startCommand(e env, args []string, ready startReady) int {
	fs := flag.NewFlagSet("start", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	path := fs.String("c", "keepgoing.yaml", "path to the config file")
	foreground := fs.Bool("foreground", false, "run in this terminal (default)")
	background := fs.Bool("background", false, "detach the daemon; return after startup succeeds")
	allowShare := fs.Bool("allow-second-loop", false, "start this loop even though another enabled loop supervises the same session")
	asJSON := fs.Bool("json", false, "with --background: print the result as one JSON object")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() != 0 || (*foreground && *background) || (ready != nil && *background) {
		e.errf("start: choose --foreground or --background, with no positional arguments")
		return exitUsage
	}
	if *asJSON && !*background {
		e.errf("start: --json is supported with --background only")
		return exitUsage
	}
	if *asJSON {
		// Every outcome is one object on stdout; people still get stderr.
		out := &startJSONWriter{}
		code := startResolved(env{e.ctx, out, e.stderr}, *path, true, *allowShare, nil)
		if out.written {
			_, _ = e.stdout.Write(out.buf.Bytes())
		} else if code != exitOK {
			_ = json.NewEncoder(e.stdout).Encode(startJSON{SchemaVersion: 1, Outcome: "failed", ExitCode: code})
		}
		return code
	}
	return startResolved(e, *path, *background, *allowShare, ready)
}

// startJSON is start --json's result. A refusal for a second loop on a
// session is a refusedResult instead.
type startJSON struct {
	SchemaVersion int    `json:"schemaVersion"`
	Outcome       string `json:"outcome"` // started or failed
	PID           int    `json:"pid,omitempty"`
	Control       string `json:"control,omitempty"`
	ExitCode      int    `json:"exitCode,omitempty"`
}

// startJSONWriter holds what --json prints until the command ends, so a
// failure that printed nothing still gets an object.
type startJSONWriter struct {
	buf     bytes.Buffer
	written bool
}

func (w *startJSONWriter) Write(p []byte) (int, error) {
	w.written = true
	return w.buf.Write(p)
}

func startResolved(e env, path string, background, allowShare bool, ready startReady) int {
	abs, err := filepath.Abs(path)
	if err != nil {
		e.errf("start: cannot resolve config path")
		return exitUsage
	}
	cfg, err := config.Load(abs)
	if err != nil {
		e.errf("start: configuration or fallbacks cannot be read or validated; run doctor for diagnostics")
		return exitUsage
	}
	_, asJSON := e.stdout.(*startJSONWriter)
	if background {
		// Checked here as well as in the daemon it starts, so the refusal
		// can be reported in the caller's own words and as JSON.
		if !checkSecondLoop(e, cfg, "start", allowShare, asJSON) {
			return exitFailed
		}
		executable, err := os.Executable()
		if err != nil {
			e.errf("start: cannot locate the keepgoing executable")
			return exitFailed
		}
		return startBackground(e, executable, abs, allowShare)
	}
	return startForeground(e, cfg, abs, ready, allowShare)
}

func startForeground(e env, cfg *config.Config, path string, ready startReady, allowShare bool) int {
	l, err := lock.AcquireNamed(cfg.Loop.StateDir, lock.Daemon)
	if errors.Is(err, lock.ErrHeld) {
		e.errf("start: another daemon is running for %s; use status to inspect it", cfg.Loop.Name)
		return exitSkipped
	}
	if err != nil {
		e.errf("start: cannot acquire daemon lock; run doctor for diagnostics")
		return exitFailed
	}
	defer l.Release()
	tick, err := lock.Acquire(cfg.Loop.StateDir)
	if err != nil {
		return ownershipError(e, err)
	}
	defer tick.Release()
	if err := checkTransferSources(cfg, false); err != nil {
		return ownershipError(e, err)
	}
	if err := validateOwner(cfg.Loop.StateDir, false); err != nil {
		return ownershipError(e, err)
	}
	if !checkSecondLoop(e, cfg, "start", allowShare) {
		return exitFailed
	}
	if err := startPreflight(e.ctx, cfg); err != nil {
		e.errf("start: %v; run doctor for diagnostics", err)
		return exitFailed
	}
	monitor, err := daemoninfo.Start(cfg.Loop.StateDir, version, false)
	if err != nil {
		e.errf("start: cannot open live daemon diagnostics; supervision was not enabled")
		return exitFailed
	}
	defer monitor.Close()
	f, err := updateControlWithHistory(cfg.Loop.StateDir, "start", func(f *control.File) error {
		if err := e.ctx.Err(); err != nil {
			return err
		}
		if err := control.ClaimOwner(f, os.Getenv("BB_THREAD_ID"), false); err != nil {
			return err
		}
		f.Enabled = true // All holds and the existing note survive.
		return nil
	})
	if err != nil {
		if errors.Is(err, errControlHistory) {
			e.errf("start: %v; daemon was not started", err)
			return exitFailed
		}
		return ownershipError(e, err)
	}
	noteSupervisor(cfg)
	// Nothing can run a tick until setup and the owner claim are committed.
	_ = tick.Release()
	if ready != nil {
		if err := ready(f); err != nil {
			e.errf("start: startup acknowledgement failed; daemon is exiting")
			return exitFailed
		}
		e.stderr = io.Discard // Do not accumulate daemon errors in the startup buffer.
	} else {
		fmt.Fprintf(e.stdout, "started %s in foreground; control: %s (Ctrl-C ends the daemon)\n", cfg.Loop.Name, f.Summary())
	}
	return daemonLoop(e, cfg, path, false, leaseOf(f), monitor)
}

// Local checks only: no login commands, provider queries, probes, or task text
// in diagnostics. The ordinary tick still verifies live state before acting.
func startPreflight(ctx context.Context, cfg *config.Config) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		return errors.New("recorded tick state cannot be read")
	}
	if st.Target() != state.Primary {
		c, ok := cfg.FallbackSet.Find(st.Target())
		if !ok || c.Disabled {
			return errors.New("recorded target is missing or disabled; retarget explicitly")
		}
	}
	if cfg.FallbackSet != nil {
		if _, err := health.Load(cfg.Loop.StateDir); err != nil {
			return errors.New("recorded fallback health cannot be read")
		}
	}
	r := doctorReport{}
	if cfg.Loop.PromptFile != "" {
		doctorFile(&r, "task", cfg.Loop.PromptFile, false)
	}
	check := func(id string, a config.Agent) {
		doctorAgent(ctx, &r, id, a, true, 0, nil)
		var workdir string
		if a.Cursor != nil {
			workdir = a.Cursor.Workdir
		} else if a.Claude != nil {
			workdir = a.Claude.Workdir
		} else if a.Codex != nil {
			workdir = a.Codex.Workdir
		}
		if workdir != "" {
			info, err := os.Stat(workdir)
			if err != nil || !info.IsDir() {
				r.add(id+".workdir", "error", "Agent working directory is missing or inaccessible.", "")
			}
		}
		if p := a.Process; p != nil {
			for _, argv := range [][]string{p.StatusCommand, p.ResumeCommand, p.StopCommand} {
				if len(argv) > 0 {
					if _, err := exec.LookPath(argv[0]); err != nil {
						r.add(id+".command", "error", "Process command is missing or not executable.", "")
					}
				}
			}
		}
	}
	check("primary", cfg.Loop.Agent)
	if cfg.FallbackSet != nil {
		for _, c := range cfg.FallbackSet.Candidates {
			if !c.Disabled {
				check(c.ID, c.Agent)
			}
		}
	}
	for _, c := range r.Checks {
		if c.Status == "error" {
			return fmt.Errorf("%s: %s", c.ID, c.Message)
		}
	}
	return nil
}

// A one-shot pipe handshake belongs to this child, unlike a stale PID or
// endpoint file. No shell, config contents, or task text is passed in argv.
type startReply struct {
	Code    int    `json:"code"`
	PID     int    `json:"pid,omitempty"`
	Control string `json:"control,omitempty"`
	Message string `json:"message,omitempty"`
}

func startChild(e env, args []string) int {
	var errs bytes.Buffer
	acknowledged := false
	code := startCommand(env{e.ctx, io.Discard, &errs}, args, func(f control.File) error {
		err := json.NewEncoder(e.stdout).Encode(startReply{PID: os.Getpid(), Control: f.Summary()})
		acknowledged = err == nil
		return err
	})
	if !acknowledged {
		_ = json.NewEncoder(e.stdout).Encode(startReply{Code: code, Message: errs.String()})
	}
	return code
}

func startBackground(e env, executable, path string, allowShare bool) int {
	if e.ctx.Err() != nil {
		e.errf("start: cancelled before launching daemon")
		return exitFailed
	}
	args := []string{"__start-background", "-c", path}
	if allowShare {
		args = append(args, "--allow-second-loop")
	}
	cmd := exec.Command(executable, args...)
	if err := detachDaemon(cmd); err != nil {
		e.errf("start: %v", err)
		return exitFailed
	}
	pipe, err := cmd.StdoutPipe()
	if err != nil {
		e.errf("start: cannot create startup pipe")
		return exitFailed
	}
	defer pipe.Close()
	// Unset stdin/stderr are connected to the null device. The child uses the
	// pipe only for startup; later decisions remain in private runtime records.
	if err := cmd.Start(); err != nil {
		e.errf("start: cannot launch background daemon")
		return exitFailed
	}
	type result struct {
		reply startReply
		err   error
	}
	replies := make(chan result, 1)
	go func() {
		var r result
		r.err = json.NewDecoder(io.LimitReader(pipe, 8192)).Decode(&r.reply)
		replies <- r
	}()
	ctx, cancel := context.WithTimeout(e.ctx, 10*time.Second)
	defer cancel()
	started := false
	defer func() {
		if !started {
			_ = cmd.Process.Kill()
		}
		// Reap if this caller stays alive; otherwise the OS adopts the daemon.
		go cmd.Wait()
	}()
	select {
	case <-ctx.Done():
		e.errf("start: startup cancelled or timed out; child terminated; inspect status before retrying")
		return exitFailed
	case r := <-replies:
		if r.err != nil || ctx.Err() != nil {
			e.errf("start: startup acknowledgement unavailable; child terminated; inspect status before retrying")
			return exitFailed
		}
		if r.reply.Code != exitOK {
			e.errf("%s", r.reply.Message)
			return r.reply.Code
		}
		if r.reply.PID != cmd.Process.Pid || r.reply.Control == "" {
			e.errf("start: invalid startup acknowledgement; child terminated")
			return exitFailed
		}
		started = true
		if _, asJSON := e.stdout.(*startJSONWriter); asJSON {
			_ = json.NewEncoder(e.stdout).Encode(startJSON{SchemaVersion: 1, Outcome: "started", PID: r.reply.PID, Control: r.reply.Control})
			return exitOK
		}
		fmt.Fprintf(e.stdout, "started background daemon (PID %d); control: %s\nUse status to inspect, pause to hold, or stop to stop the agent. No service was installed.\n", r.reply.PID, r.reply.Control)
		return exitOK
	}
}
