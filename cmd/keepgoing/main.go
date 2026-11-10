// Command keepgoing keeps long-running coding-agent loops going.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/report"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes. 75 means "skipped, try later", matching EX_TEMPFAIL.
const (
	exitOK      = 0
	exitFailed  = 1
	exitUsage   = 2
	exitSkipped = 75
)

const usage = `keepgoing keeps a coding-agent loop going.

Usage:
  keepgoing <command> [flags]

Supervision (read -c keepgoing.yaml, default ./keepgoing.yaml):
  tick                     Run one pass and exit (cron, launchd, CI, BB automations)
  daemon                   Run passes on an interval; control changes act within seconds
  status                   Show control, active target, window, and fallback health
  validate                 Check keepgoing.yaml and its fallbacks file
  doctor                   Diagnose setup and recent checks without model calls

Control:
  enable | disable         Turn supervision on or off (nothing is sent while off)
  pause | resume           Pause; resume also clears stop, awaiting-user, and completion
  stop                     Stop the running session on the next pass, then send nothing
  complete                 Mark the task finished; hold until resume
  await-user               Mark the loop as waiting for a person
  retarget <id|primary>    Point the loop at a fallback or back at the primary

Fallbacks:
  fallbacks check          Probe candidates now (--id ID, --exact for the target model)
  fallbacks list           Show recorded health and the redundancy picture

Reporting:
  report test              Send a test webhook (--dry-run prints it instead)

Tools (no config needed):
  init                     Guide setup of a disabled loop outside the checkout
  ci-wait                  Wait for every CI workflow on a PR head or exact commit
  net-retry -- cmd...      Rerun a network command only while the network is down
  version                  Print the version

Exit codes: 0 ok, 1 failed, 2 usage or config error, 75 skipped (lock held or yielded).
`

func main() {
	// On platforms without a POSIX shell, headless turns run through a copy
	// of this binary; this returns immediately in every other case.
	runner.MaybeRunTurn()
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdout, os.Stderr)
	cancel()
	os.Exit(code)
}

type env struct {
	ctx    context.Context
	stdout io.Writer
	stderr io.Writer
}

func (e env) errf(format string, a ...any) { fmt.Fprintf(e.stderr, format+"\n", a...) }

func run(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	e := env{ctx: ctx, stdout: stdout, stderr: stderr}
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		return exitUsage
	}
	cmd, rest := args[0], args[1:]
	switch cmd {
	case "doctor":
		return doctor(e, rest, doctorExec)
	case "init":
		return initLoop(e, rest)
	case "version":
		fmt.Fprintln(stdout, version)
		return exitOK
	case "ci-wait":
		return ciWait(e, rest)
	case "net-retry":
		return netRetry(e, rest)
	case "fallbacks", "report":
		if len(rest) == 0 {
			e.errf("%s needs a subcommand; see keepgoing help", cmd)
			return exitUsage
		}
		cmd, rest = cmd+" "+rest[0], rest[1:]
	}
	positional, rest := splitPositional(rest)
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	f := cliFlags{
		cfgPath:    fs.String("c", "keepgoing.yaml", "path to the config file"),
		asJSON:     fs.Bool("json", false, "print machine-readable JSON"),
		dryRun:     fs.Bool("dry-run", false, "decide and record, send nothing, run no probes"),
		now:        fs.String("now", "", "RFC 3339 time to use instead of the clock"),
		note:       fs.String("note", "", "a short note stored in control.json"),
		id:         fs.String("id", "", "fallbacks check: only this candidate"),
		exact:      fs.Bool("exact", false, "fallbacks check: probe the target model, not the cheap one"),
		staleAfter: fs.Duration("stale-after", defaultStaleAfter, "status: age at which recorded history is stale"),
	}
	if err := fs.Parse(rest); err != nil {
		return exitUsage
	}
	positional = append(positional, fs.Args()...)
	cfg, err := config.Load(*f.cfgPath)
	if err != nil {
		e.errf("config: %v", err)
		return exitUsage
	}
	now := time.Now()
	if *f.now != "" {
		if now, err = time.Parse(time.RFC3339, *f.now); err != nil {
			e.errf("-now: %v", err)
			return exitUsage
		}
	}
	switch cmd {
	case "validate":
		return validate(e, cfg, *f.cfgPath)
	case "tick":
		return tickOnce(e, cfg, now, *f.dryRun, *f.now != "")
	case "daemon":
		return daemon(e, cfg, *f.cfgPath, *f.dryRun)
	case "status":
		if *f.staleAfter <= 0 {
			e.errf("status: stale-after must be positive")
			return exitUsage
		}
		return status(e, cfg, now, *f.asJSON, *f.staleAfter)
	case "enable", "disable", "pause", "resume", "stop", "await-user", "complete":
		return controlCmd(e, cfg, cmd, *f.note)
	case "retarget":
		if len(positional) != 1 {
			e.errf("usage: keepgoing retarget <candidate-id|primary> [-c keepgoing.yaml]")
			return exitUsage
		}
		return retarget(e, cfg, positional[0], *f.note)
	case "fallbacks check":
		return fallbacksCheck(e, cfg, now, *f.id, *f.exact, *f.asJSON)
	case "fallbacks list":
		return fallbacksList(e, cfg, now, *f.asJSON)
	case "report test":
		return reportTest(e, cfg, *f.dryRun)
	}
	e.errf("unknown command %q\n\n%s", cmd, usage)
	return exitUsage
}

type cliFlags struct {
	staleAfter             *time.Duration
	cfgPath, now, note, id *string
	asJSON, dryRun, exact  *bool
}

// splitPositional lets a positional argument come before the flags, as in
// `keepgoing retarget codex-main -c loop.yaml`.
func splitPositional(args []string) ([]string, []string) {
	var pos []string
	for len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		pos = append(pos, args[0])
		args = args[1:]
	}
	return pos, args
}

// reporter builds the configured webhook reporter. A misconfigured report
// is a warning, never a reason to skip supervision.
func reporter(e env, cfg *config.Config) report.Reporter {
	if cfg.Loop.Report == nil {
		return report.Nop{}
	}
	w, err := report.New(*cfg.Loop.Report)
	if err != nil {
		e.errf("warning: reports disabled: %v", err)
		return report.Nop{}
	}
	return w
}
