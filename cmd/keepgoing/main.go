// Command keepgoing supervises a long-running coding-agent loop.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/bb"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/process"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"github.com/connorburgess/keepgoing-agents/internal/supervise"
	"github.com/connorburgess/keepgoing-agents/internal/window"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes. 75 matches the convention that a skipped run is not a failure.
const (
	exitOK     = 0
	exitFailed = 1
	exitUsage  = 2
	exitLocked = 75
)

const usage = `keepgoing keeps a coding-agent loop going.

Usage:
  keepgoing <command> [-c keepgoing.yaml] [flags]

Commands:
  tick        Run one supervision pass and exit (for cron, launchd, CI, BB automations)
  status      Show control state, last observation, and window
  validate    Check the config file and exit
  enable      Enable supervision (creates control.json)
  disable     Disable supervision; nothing is sent until enabled again
  pause       Pause; honored on the next tick
  resume      Clear pause, stop, and awaiting-user
  stop        Stop the running session on the next tick and send nothing more
  await-user  Mark the loop as waiting for a person
  version     Print the version

Exit codes: 0 ok, 1 failed, 2 usage or config error, 75 another tick holds the lock.
`

func main() {
	os.Exit(run(os.Args[1:]))
}

func run(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(os.Stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	if cmd == "version" {
		fmt.Println(version)
		return exitOK
	}
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	cfgPath := fs.String("c", "keepgoing.yaml", "path to the config file")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	dryRun := fs.Bool("dry-run", false, "tick: decide and record but send nothing")
	nowFlag := fs.String("now", "", "tick/status: RFC 3339 time to use instead of the clock")
	note := fs.String("note", "", "control commands: a short note stored in control.json")
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	cfg, err := config.Load(*cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, "config:", err)
		return exitUsage
	}
	now := time.Now()
	if *nowFlag != "" {
		if now, err = time.Parse(time.RFC3339, *nowFlag); err != nil {
			fmt.Fprintln(os.Stderr, "-now:", err)
			return exitUsage
		}
	}
	switch cmd {
	case "validate":
		fmt.Printf("ok: %s (loop %s, adapter %s)\n", *cfgPath, cfg.Loop.Name, cfg.Loop.Adapter)
		return exitOK
	case "tick":
		return tick(cfg, now, *dryRun)
	case "status":
		return status(cfg, now, *asJSON)
	case "enable":
		return update(cfg, *note, func(f *control.File) { f.Enabled = true })
	case "disable":
		return update(cfg, *note, func(f *control.File) { f.Enabled = false })
	case "pause":
		return update(cfg, *note, func(f *control.File) { f.Paused = true })
	case "resume":
		return update(cfg, *note, func(f *control.File) { f.Paused, f.Stop, f.AwaitingUser = false, false, false })
	case "stop":
		return update(cfg, *note, func(f *control.File) { f.Stop = true })
	case "await-user":
		return update(cfg, *note, func(f *control.File) { f.AwaitingUser = true })
	}
	fmt.Fprintf(os.Stderr, "unknown command %q\n\n%s", cmd, usage)
	return exitUsage
}

func newAdapter(cfg *config.Config) (adapter.Adapter, error) {
	switch cfg.Loop.Adapter {
	case "bb":
		return bb.New(cfg.Loop.BB.CLI, cfg.Loop.BB.Thread), nil
	case "process":
		return process.New(*cfg.Loop.Process), nil
	}
	return nil, fmt.Errorf("unknown adapter %q", cfg.Loop.Adapter)
}

func tick(cfg *config.Config, now time.Time, dryRun bool) int {
	l, err := lock.Acquire(cfg.Loop.StateDir)
	if errors.Is(err, lock.ErrHeld) {
		fmt.Fprintln(os.Stderr, "another tick holds the lock; skipping")
		return exitLocked
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "lock:", err)
		return exitFailed
	}
	defer l.Release()
	a, err := newAdapter(cfg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitUsage
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	res, err := supervise.Tick(ctx, cfg, a, supervise.Options{Now: now, DryRun: dryRun})
	out, _ := json.Marshal(res)
	fmt.Println(string(out))
	if err != nil {
		fmt.Fprintln(os.Stderr, "tick:", err)
		return exitFailed
	}
	return exitOK
}

type statusOut struct {
	Loop         string         `json:"loop"`
	Adapter      string         `json:"adapter"`
	Control      control.File   `json:"control"`
	ControlState string         `json:"controlState"`
	Window       string         `json:"window"`
	InWindow     bool           `json:"inWindow"`
	State        state.File     `json:"state"`
	StateDir     string         `json:"stateDir"`
	Version      string         `json:"version"`
	Extra        map[string]any `json:"extra,omitempty"`
}

func status(cfg *config.Config, now time.Time, asJSON bool) int {
	ctl, err := control.Load(cfg.Loop.StateDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFailed
	}
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFailed
	}
	in, why, err := window.Inside(cfg.Loop.Window, now)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFailed
	}
	out := statusOut{Loop: cfg.Loop.Name, Adapter: cfg.Loop.Adapter, Control: ctl, ControlState: ctl.Summary(), Window: why, InWindow: in, State: st, StateDir: cfg.Loop.StateDir, Version: version}
	if asJSON {
		b, _ := json.MarshalIndent(out, "", "  ")
		fmt.Println(string(b))
		return exitOK
	}
	fmt.Printf("loop:      %s (%s adapter)\n", out.Loop, out.Adapter)
	fmt.Printf("control:   %s", out.ControlState)
	if ctl.Note != "" {
		fmt.Printf(" (%s)", ctl.Note)
	}
	fmt.Println()
	fmt.Printf("window:    %s\n", out.Window)
	fmt.Printf("last seen: %s\n", orNever(st.CheckedAt, st.LastState))
	fmt.Printf("progress:  %s\n", orNever(st.LastProgressAt, ""))
	fmt.Printf("last sent: %s\n", orNever(st.LastWakeAt, ""))
	if st.Reprompts > 0 {
		fmt.Printf("reprompts: %d (last %s)\n", st.Reprompts, orNever(st.LastRepromptAt, ""))
	}
	fmt.Printf("state dir: %s\n", out.StateDir)
	return exitOK
}

func orNever(t time.Time, label string) string {
	if t.IsZero() {
		return "never"
	}
	s := t.UTC().Format(time.RFC3339)
	if label != "" {
		s += " (" + label + ")"
	}
	return s
}

func update(cfg *config.Config, note string, fn func(*control.File)) int {
	f, err := control.Update(cfg.Loop.StateDir, func(f *control.File) {
		fn(f)
		if note != "" {
			f.Note = note
		}
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFailed
	}
	if err := state.Append(cfg.Loop.StateDir, state.Event{Loop: cfg.Loop.Name, Event: "control", Reason: f.Summary()}); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return exitFailed
	}
	fmt.Printf("%s: %s\n", cfg.Loop.Name, f.Summary())
	return exitOK
}
