package supervise

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"path/filepath"
	"time"

	"github.com/looptender/looptender/internal/adapter/runner"
	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/loopcontext"
	"github.com/looptender/looptender/internal/prompt"
	"github.com/looptender/looptender/internal/state"
	"github.com/looptender/looptender/internal/window"
)

// DefaultCommandPrefix runs the command when the caller names no build of it.
const DefaultCommandPrefix = "looptender"

// ContextInput is what loopcontext.Build reads, from records the caller has
// loaded: ctl and st are nil when they could not be read. It reads the send
// count and the progress baseline itself, when the limits that use them are
// set. prefix and cfgArg are already quoted for the shell.
func ContextInput(cfg *config.Config, prefix, cfgArg string, ctl *control.File, st *state.File, now time.Time) loopcontext.Input {
	l := cfg.Loop
	in := loopcontext.Input{Loop: l, CommandPrefix: prefix, ConfigArg: cfgArg, Control: ctl, State: st, Now: now}
	if st != nil {
		if st.Target() == state.Primary {
			a := l.Agent
			in.TargetAgent = &a
		} else if fb, _ := EffectiveRoutes(cfg, *st); fb != nil {
			if c, ok := fb.Find(st.Target()); ok && !c.Disabled {
				if a, known := st.CandidateAgent(c); known {
					in.TargetAgent = &a
					in.TargetCreated = c.Agent.BB != nil && c.Agent.BB.NewThread
				} else {
					in.TargetPending = true
				}
			}
		}
	}
	if l.Limits.MaxSends > 0 {
		if used, err := state.LoadSends(l.StateDir); err != nil {
			in.SendsError = "sends.json could not be read"
		} else {
			in.SendsUsed = &used
		}
	}
	if timeout := l.Limits.NoProgressTimeout.Duration; timeout > 0 {
		p, err := state.LoadProgress(l.StateDir)
		switch {
		case err != nil:
			in.ProgressError = "progress.json could not be read"
		case p == nil:
		case now.Before(p.ObservedAt):
			in.ProgressError = "host clock precedes the last verified progress observation"
		case p.Sources != state.ProgressSources(l.Progress.Repositories):
			in.ProgressError = "repository list changed; the next check sets a new baseline"
		default:
			in.Progress = p
		}
	}
	inside, why, err := window.Inside(l.Window, now)
	in.Window = loopcontext.Window{Set: l.Window != nil, Inside: inside, Text: why}
	if err != nil {
		in.Window.Error = "the window could not be evaluated"
	}
	return in
}

// contextDigest is a digest of the context's content without the figures
// that move with every send or with the clock (counts used and remaining,
// time remaining, whether a limit is reached, whether the window is open
// now), and of the time of the last saved control change, so any control
// save (enable, resume, a change of owner, a hold) makes the next send
// carry the context again.
func contextDigest(v loopcontext.View, controlAt time.Time) string {
	v.Bounds.Limits = append([]loopcontext.Bound(nil), v.Bounds.Limits...)
	for i := range v.Bounds.Limits {
		b := &v.Bounds.Limits[i]
		b.Used, b.Remaining, b.RemainingTime, b.Reached, b.Unavailable, b.Text = nil, nil, "", false, "", ""
	}
	v.Bounds.Window.Inside, v.Bounds.Window.Text = false, ""
	b, _ := json.Marshal(struct {
		View      loopcontext.View `json:"view"`
		ControlAt time.Time        `json:"controlAt"`
	}{v, controlAt.UTC()})
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:12])
}

// newConversation says target's next turn starts a new conversation that
// looptender knows of: a headless target whose agent does not continue a
// saved session, so every turn is new, or one with a session reset waiting.
// For BB, the generic process and Cursor it is always false: looptender
// cannot see from here which conversation they reach.
func (t *tick) newConversation(target string) bool {
	agent := t.loop.Agent
	if target != state.Primary {
		c, ok := t.fb.Find(target)
		if !ok {
			return false
		}
		agent = c.Agent
	}
	spec, _, ok := runner.Resolve(agent)
	if !ok {
		return false
	}
	return !spec.Capabilities.ExactResume || runner.Dir(filepath.Join(t.loop.StateDir, "targets", target)).ResetPending()
}

// withContext appends the supervision context to a message for target when
// it is due, and returns the record to save once the adapter call returns
// without error; nil when nothing was added. The caller checks the key and
// leaves out a notice-only message. A brief always carries it: it is the
// first message to that conversation for this move. Any other send carries
// it when no record exists, when the content or the last control save
// differs from the record, or when the target's next turn starts a new
// conversation.
func (t *tick) withContext(m prompt.Message, target, action string) (prompt.Message, *state.ContextSent) {
	st := t.st
	st.ActiveTarget = target // the context of the conversation this goes to
	prefix := t.opts.CommandPrefix
	if prefix == "" {
		prefix = DefaultCommandPrefix
	}
	cfgPath := t.cfg.Path()
	if abs, err := filepath.Abs(cfgPath); err == nil && cfgPath != "" {
		cfgPath = abs
	}
	ctl := t.ctl
	v := loopcontext.Build(ContextInput(t.cfg, prefix, loopcontext.Quote(cfgPath), &ctl, &st, t.now))
	digest := contextDigest(v, ctl.UpdatedAt)
	rec := t.st.ContextSent
	due := action == "handoff" || action == "switch_back" || rec == nil || rec.Digest != digest || t.newConversation(target)
	if !due {
		return m, nil
	}
	m = prompt.WithContext(m, v.SchemaVersion, v.Text())
	return m, &state.ContextSent{Digest: digest, At: t.now, Bytes: m.ContextBytes}
}
