package main

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/session"
	"github.com/connorburgess/keepgoing-agents/internal/state"
)

// Two loops on one session would both prompt it. A check's session claim
// stops them acting at the same moment (see internal/session); this file is
// the other half: enable, start and init refuse to turn on a second loop on a
// session another enabled loop supervises, and status and validate say when
// loops share a session, a working directory or a repository.
//
// Other loops are found in the registry and in the session directory's
// supervisors records, which every enable and every check writes, so loops
// that were never registered are found too once they have been enabled or
// checked. A loop counts only while its configuration still loads, names the
// state directory it was found with, and its control record says enabled.

// maxOtherLoops bounds how many other loops are looked at.
const maxOtherLoops = 192

// otherLoop is another enabled loop and what it shares with this one.
type otherLoop struct {
	Loop     string
	Config   string
	StateDir string
	// Primary is set when its primary session is this loop's primary.
	Primary bool
	// Shares lists, in words, what else is shared.
	Shares []string
}

// loopSessions are the sessions a loop acts on: its primary and each
// fallback with a fixed session.
func loopSessions(cfg *config.Config) (primary session.Identity, havePrimary bool, all map[session.Identity]string) {
	all = map[session.Identity]string{}
	primary, havePrimary = session.Of(cfg.Loop.Agent)
	if havePrimary {
		all[primary] = "primary"
	}
	if fb := cfg.FallbackSet; fb != nil {
		for _, c := range fb.Candidates {
			if c.Disabled {
				continue
			}
			if id, ok := session.Of(c.Agent); ok {
				if _, seen := all[id]; !seen {
					all[id] = "fallback " + c.ID
				}
			}
		}
	}
	return primary, havePrimary, all
}

// activeAgent is the agent a loop's records name as current.
func activeAgent(cfg *config.Config) config.Agent {
	st, err := state.Load(cfg.Loop.StateDir)
	if err != nil || st.Target() == state.Primary || cfg.FallbackSet == nil {
		return cfg.Loop.Agent
	}
	c, ok := cfg.FallbackSet.Find(st.Target())
	if !ok || c.Disabled {
		return cfg.Loop.Agent
	}
	if a, known := st.CandidateAgent(c); known {
		return a
	}
	return c.Agent
}

// findOtherLoops returns the other enabled loops that share anything with
// this one. It reads only; nothing it finds is changed.
func findOtherLoops(cfg *config.Config) []otherLoop {
	self := session.CleanPath(cfg.Loop.StateDir)
	type found struct{ loop, config, stateDir string }
	var candidates []found
	seen := map[string]bool{self: true}
	add := func(loop, configPath, stateDir string) {
		key := session.CleanPath(stateDir)
		if key == "" || seen[key] || len(candidates) >= maxOtherLoops {
			return
		}
		seen[key] = true
		candidates = append(candidates, found{loop, configPath, key})
	}
	if dir, err := registryDirectory(""); err == nil {
		if f, err := registry.Load(dir); err == nil {
			for _, e := range f.Entries {
				add(e.Loop, e.ConfigPath, e.StateDir)
			}
		}
	}
	if dir, err := session.Dir(); err == nil {
		for _, s := range session.All(dir, maxOtherLoops) {
			add(s.Loop, s.Config, s.StateDir)
		}
	}
	primary, havePrimary, mine := loopSessions(cfg)
	myDir := session.Workdir(activeAgent(cfg))
	myRepos := map[string]bool{}
	for _, r := range cfg.Loop.Progress.Repositories {
		if p := session.CleanPath(r); p != "" {
			myRepos[p] = true
		}
	}
	var out []otherLoop
	for _, c := range candidates {
		other, err := config.Load(c.config)
		if err != nil || session.CleanPath(other.Loop.StateDir) != c.stateDir {
			continue
		}
		ctl, err := control.Load(c.stateDir)
		if err != nil || !ctl.Enabled {
			continue
		}
		o := otherLoop{Loop: other.Loop.Name, Config: other.Path(), StateDir: c.stateDir}
		theirPrimary, theirHave, theirs := loopSessions(other)
		if havePrimary && theirHave && theirPrimary == primary {
			o.Primary = true
		}
		for id, where := range theirs {
			mineWhere, ok := mine[id]
			if !ok || (o.Primary && id == primary) {
				continue
			}
			o.Shares = append(o.Shares, fmt.Sprintf("its %s and this loop's %s are the same %s", where, mineWhere, id.Describe()))
		}
		if myDir != "" {
			if theirDir := session.Workdir(activeAgent(other)); theirDir == myDir {
				o.Shares = append(o.Shares, "its current agent works in the same directory, "+myDir)
			}
		}
		for _, r := range other.Loop.Progress.Repositories {
			if p := session.CleanPath(r); p != "" && myRepos[p] {
				o.Shares = append(o.Shares, "it tracks the same repository, "+p)
				break
			}
		}
		if o.Primary || len(o.Shares) > 0 {
			out = append(out, o)
		}
	}
	return out
}

// secondLoopConflict returns the other enabled loop whose primary session is
// this loop's, or nil.
func secondLoopConflict(cfg *config.Config, others []otherLoop) *otherLoop {
	if _, ok := session.Of(cfg.Loop.Agent); !ok {
		return nil
	}
	for i := range others {
		if others[i].Primary {
			return &others[i]
		}
	}
	return nil
}

// refuseSecondLoop returns the refusal for turning on this loop, or "" when
// no other enabled loop has the same primary session. The message is for a
// person: it names the other loop, its configuration and how to proceed.
func refuseSecondLoop(cfg *config.Config, others []otherLoop, command string) string {
	o := secondLoopConflict(cfg, others)
	if o == nil {
		return ""
	}
	primary, _ := session.Of(cfg.Loop.Agent)
	return fmt.Sprintf("%s: loop %s (%s) already supervises %s. Two loops on one session would both prompt it. Disable that loop first, or pass --allow-second-loop to %s this one anyway", command, o.Loop, o.Config, primary.Describe(), command)
}

// refusedResult is what init --json and start --json print when they refuse:
// the reason as a code and the other loop's name, never its path, so a
// program can show it without parsing prose.
type refusedResult struct {
	SchemaVersion int            `json:"schemaVersion"`
	Outcome       string         `json:"outcome"`
	Refusal       sessionRefusal `json:"refusal"`
}

type sessionRefusal struct {
	Code      string `json:"code"`
	OtherLoop string `json:"otherLoop"`
}

const refusalSecondLoop = "second_loop_on_session"

func printSecondLoopRefusal(e env, other *otherLoop) {
	_ = json.NewEncoder(e.stdout).Encode(refusedResult{SchemaVersion: 1, Outcome: "refused", Refusal: sessionRefusal{Code: refusalSecondLoop, OtherLoop: other.Loop}})
}

// sharedLoopWarnings are the warnings status, validate and enable print.
func sharedLoopWarnings(cfg *config.Config, others []otherLoop) []string {
	primary, _ := session.Of(cfg.Loop.Agent)
	var out []string
	for _, o := range others {
		if o.Primary {
			out = append(out, fmt.Sprintf("loop %s (%s) also supervises %s; the two can both prompt it, though never at the same moment", o.Loop, o.Config, primary.Describe()))
		}
		for _, s := range o.Shares {
			out = append(out, fmt.Sprintf("loop %s (%s) is enabled and %s", o.Loop, o.Config, s))
		}
	}
	return out
}

// noteSupervisor records this loop as a supervisor of its primary session,
// so other loops' enable and status can name it. Best effort.
func noteSupervisor(cfg *config.Config) {
	id, ok := session.Of(cfg.Loop.Agent)
	if !ok || cfg.Path() == "" {
		return
	}
	dir, err := session.Dir()
	if err != nil {
		return
	}
	_ = session.Note(dir, id, session.Supervisor{Loop: cfg.Loop.Name, Config: cfg.Path(), StateDir: cfg.Loop.StateDir, SeenAt: time.Now().UTC()})
}

// checkSecondLoop is run by enable and start before they turn a loop on. It
// prints the warnings, and returns false after printing the refusal: always
// for people on stderr, and with asJSON also as a refusedResult on stdout.
func checkSecondLoop(e env, cfg *config.Config, command string, allow bool, asJSON ...bool) bool {
	others := findOtherLoops(cfg)
	if msg := refuseSecondLoop(cfg, others, command); msg != "" && !allow {
		e.errf("%s", msg)
		if len(asJSON) > 0 && asJSON[0] {
			printSecondLoopRefusal(e, secondLoopConflict(cfg, others))
		}
		return false
	}
	for _, w := range sharedLoopWarnings(cfg, others) {
		e.errf("warning: %s", w)
	}
	return true
}

// sessionClaimText says who holds this loop's primary session claim, when it
// is not this process. Status reads the holder's details and never takes the
// claim itself.
func sessionClaimText(cfg *config.Config, now time.Time) string {
	id, ok := session.Of(cfg.Loop.Agent)
	if !ok {
		return ""
	}
	dir, err := session.Dir()
	if err != nil {
		return ""
	}
	h := session.Peek(dir, id)
	if h == nil || h.PID == os.Getpid() {
		return ""
	}
	if !h.Until.IsZero() && now.After(h.Until) {
		return fmt.Sprintf("claimed by loop %s (pid %d) past its own time limit at %s: that process is stuck, and checks of every loop on this session skip until it ends. Find process %d and stop it; keepgoing does not break the claim", h.Loop, h.PID, h.Until.Local().Format("15:04"), h.PID)
	}
	return fmt.Sprintf("being checked by loop %s (pid %d) since %s", h.Loop, h.PID, h.Since.Local().Format("15:04:05"))
}
