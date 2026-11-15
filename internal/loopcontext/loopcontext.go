// Package loopcontext builds the supervision context of a loop: what a
// supervised session may be told about the loop that supervises it, built
// only from the loop's configuration, its control file and its state.
//
// Build is pure. The caller reads the records; nothing here reads a file,
// queries a provider or writes. The wording is fixed so the context can be
// compared, versioned and later sent unchanged.
package loopcontext

import (
	"fmt"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

// SchemaVersion is the version of View's JSON form. Fields may be added
// within a version; a field's meaning never changes within one.
const SchemaVersion = 1

// NotSetByLooptender is what a permission or sandbox setting reads when the
// loop's configuration does not set it. looptender does not know the
// effective value then and does not guess it.
const NotSetByLooptender = "not set by looptender"

// NoOwnerRecorded is the owner claim of a loop with no recorded owner.
const NoOwnerRecorded = "none recorded"

// IdentityEvidence says how the target's identity is known: from the loop's
// configuration only. Nothing here asks the provider which session it is.
const IdentityEvidence = "configuration_only"

// CallerIdentity is said in every context. No caller identity is verified:
// BB_THREAD_ID is taken as given.
const CallerIdentity = "Caller identity is taken from BB_THREAD_ID, which is checked only for its form and not with BB. A command run without it has no caller identity and is treated as an ordinary terminal."

// RetainedAuthority is said in every context, in this order. Each sentence
// must hold in every configuration; anything conditional belongs with the
// setting it depends on.
var RetainedAuthority = []string{
	"Supervision grants no permission.",
	"Supervision adds no permission beyond the permission settings this context lists.",
	"A session must not clear a hold; resume is for a person. looptender does not enforce this.",
	"looptender never merges or edits a repository, and never reads transcript text to make a decision.",
	"Lanes are not supervised by implication.",
}

// Input is everything Build reads. The caller loads the records; a nil
// record could not be read.
type Input struct {
	Loop config.Loop
	// CommandPrefix runs this build of the command, already quoted for the
	// shell; ConfigArg is the loop's configuration path, already quoted.
	CommandPrefix string
	ConfigArg     string
	Control       *control.File
	State         *state.File
	// TargetAgent is the active target's agent as it can be used now, or nil
	// when it is not configured or the state could not be read.
	TargetAgent *config.Agent
	// TargetCreated: the active target is a BB thread looptender created at
	// a handoff to a bb.new_thread candidate.
	TargetCreated bool
	// TargetPending: TargetAgent is nil because the active route starts a
	// new BB thread and no thread has been recorded for it yet.
	TargetPending bool
	// SendsUsed is the send count against loop.limits.max_sends, or nil with
	// SendsError when it could not be read. Read only when max_sends is set.
	SendsUsed  *int64
	SendsError string
	// Progress is the verified no-progress baseline, nil before the first.
	// ProgressError says why it cannot be used. Read only when
	// no_progress_timeout is set.
	Progress      *state.Progress
	ProgressError string
	Window        Window
	Now           time.Time
}

// Window is the operating window as evaluated at Now.
type Window struct {
	Set    bool   `json:"set"`
	Inside bool   `json:"inside"`
	Text   string `json:"text"`
	Error  string `json:"error,omitempty"`
}

// View is the supervision context.
type View struct {
	SchemaVersion int    `json:"schemaVersion"`
	Loop          string `json:"loop"`
	// Adapter is the adapter kind of the loop's first route (loop.adapter).
	Adapter           string      `json:"adapter"`
	Target            *Target     `json:"target,omitempty"`
	TargetUnavailable string      `json:"targetUnavailable,omitempty"`
	Owner             Owner       `json:"owner"`
	CallerIdentity    string      `json:"callerIdentity"`
	ControlState      string      `json:"controlState"`
	Holds             []string    `json:"holds"`
	Note              string      `json:"note,omitempty"`
	Bounds            Bounds      `json:"bounds"`
	Permissions       Permissions `json:"permissions"`
	Lanes             LanePolicy  `json:"lanes"`
	SessionCommands   []Command   `json:"sessionCommands"`
	RetainedAuthority []string    `json:"retainedAuthority"`
	Unavailable       []string    `json:"unavailable"`
}

// Target is the route the loop is on now and the session it reaches.
type Target struct {
	Route            string `json:"route"`
	Adapter          string `json:"adapter"`
	Provider         string `json:"provider,omitempty"`
	BBThread         string `json:"bbThread,omitempty"`
	Workdir          string `json:"workdir,omitempty"`
	IdentityEvidence string `json:"identityEvidence"`
}

// Owner is the loop's recorded owner claim.
type Owner struct {
	Recorded   bool   `json:"recorded"`
	BBThread   string `json:"bbThread,omitempty"`
	Generation uint64 `json:"generation,omitempty"`
	Text       string `json:"text"`
}

// Bounds are the limits on the loop's repeated work, set or not.
type Bounds struct {
	// Unbounded: no loop-wide limit (deadline, max_sends,
	// no_progress_timeout) is set.
	Unbounded bool    `json:"unbounded"`
	Warning   string  `json:"warning,omitempty"`
	Limits    []Bound `json:"limits"`
	Window    Window  `json:"window"`
}

// Bound is one limit. Counts use Max, Used and Remaining; time limits use
// Limit and RemainingTime. Unavailable says why a used amount is unknown.
type Bound struct {
	Key           string `json:"key"`
	Scope         string `json:"scope"`
	Set           bool   `json:"set"`
	Max           *int64 `json:"max,omitempty"`
	Used          *int64 `json:"used,omitempty"`
	Remaining     *int64 `json:"remaining,omitempty"`
	Limit         string `json:"limit,omitempty"`
	RemainingTime string `json:"remainingTime,omitempty"`
	Reached       bool   `json:"reached"`
	Unavailable   string `json:"unavailable,omitempty"`
	Text          string `json:"text"`
}

// Scopes of a bound.
const (
	ScopeLoop    = "loop"
	ScopeEpisode = "error_episode"
	ScopeStall   = "stalled_turn"
)

// Permissions are the permission settings looptender passes to the active
// target, only where the configuration sets them.
type Permissions struct {
	PermissionMode       string `json:"permissionMode"`
	PermissionModeSource string `json:"permissionModeSource,omitempty"`
	Sandbox              string `json:"sandbox"`
	SandboxSource        string `json:"sandboxSource,omitempty"`
	ExtraArgs            string `json:"extraArgs,omitempty"`
}

// LanePolicy is what looptender does about the session's lanes.
type LanePolicy struct {
	Supervised    bool     `json:"supervised"`
	RetryErrored  bool     `json:"retryErrored"`
	MaxPerCheck   int      `json:"maxPerCheck,omitempty"`
	MaxPerEpisode int      `json:"maxPerEpisode,omitempty"`
	Text          []string `json:"text"`
}

// Command is a command the supervised session may run.
type Command struct {
	Name    string `json:"name"`
	Command string `json:"command"`
	Use     string `json:"use"`
}

// Fixed lane sentences.
const (
	laneNotSupervised = "Lanes, the conversations this session starts, are not supervised by implication: looptender does not message or create them, and this context does not cover them."
	laneRetryOff      = "Lane recovery is off: looptender does not retry a lane's failed turn."
	laneStop          = "looptender stops a lane only for stop --lanes, which is for a person."
)

// Build returns the supervision context. It is pure.
func Build(in Input) View {
	l := in.Loop
	v := View{
		SchemaVersion:     SchemaVersion,
		Loop:              l.Name,
		Adapter:           l.Adapter,
		CallerIdentity:    CallerIdentity,
		Holds:             []string{},
		RetainedAuthority: append([]string(nil), RetainedAuthority...),
		Unavailable:       []string{},
	}
	if in.Control == nil {
		v.Unavailable = append(v.Unavailable, control.FileName)
		v.ControlState = "unavailable"
		v.Owner = Owner{Text: "unavailable: " + control.FileName + " could not be read"}
	} else {
		v.ControlState = in.Control.Summary()
		v.Holds = holds(*in.Control)
		v.Note = in.Control.Note
		v.Owner = owner(*in.Control)
	}
	if in.State == nil {
		v.Unavailable = append(v.Unavailable, state.FileName)
	}
	v.Target, v.TargetUnavailable = target(in)
	v.Bounds = bounds(in)
	v.Permissions = permissions(in)
	v.Lanes = lanes(in.TargetAgent)
	v.SessionCommands = commands(in.CommandPrefix, in.ConfigArg)
	return v
}

func holds(c control.File) []string {
	h := []string{}
	if c.PendingTransfer != nil {
		h = append(h, "transfer pending")
	}
	if c.Stop {
		h = append(h, "stop requested")
	}
	if c.Paused {
		h = append(h, "paused")
	}
	if c.AwaitingUser {
		h = append(h, "awaiting a person")
	}
	if c.Completed {
		h = append(h, "completed")
	}
	return h
}

func owner(c control.File) Owner {
	o := c.Ownership
	if o == nil {
		return Owner{Text: NoOwnerRecorded}
	}
	if control.CheckOwner(c, "", false) != nil {
		return Owner{Recorded: true, Text: "invalid ownership record; a person must restore control.json"}
	}
	return Owner{Recorded: true, BBThread: o.ThreadID, Generation: o.Generation,
		Text: fmt.Sprintf("BB thread %s, generation %d", o.ThreadID, o.Generation)}
}

func target(in Input) (*Target, string) {
	if in.State == nil {
		return nil, state.FileName + " could not be read, so the active route is unknown"
	}
	route := in.State.Target()
	a := in.TargetAgent
	if a == nil {
		if in.TargetPending {
			return nil, fmt.Sprintf("the active route %q starts a new BB thread, and its thread has not been recorded yet", route)
		}
		return nil, fmt.Sprintf("the active route %q is missing or switched off in the configuration", route)
	}
	t := &Target{Route: route, Adapter: a.Adapter, Provider: a.Provider, IdentityEvidence: IdentityEvidence}
	switch {
	case a.BB != nil:
		t.BBThread = a.BB.Thread
	case a.Claude != nil:
		t.Workdir = a.Claude.Workdir
	case a.Codex != nil:
		t.Workdir = a.Codex.Workdir
	case a.Cursor != nil:
		t.Workdir = a.Cursor.Workdir
	}
	return t, ""
}

func count(n int64) *int64 { return &n }

func countBound(key, scope string, max, used int64, usedKnown bool, unavailable, what string) Bound {
	b := Bound{Key: key, Scope: scope, Set: true, Max: count(max)}
	if !usedKnown {
		b.Unavailable = unavailable
		b.Text = fmt.Sprintf("at most %d %s; used unavailable (%s)", max, what, unavailable)
		return b
	}
	remaining := max - used
	if remaining < 0 {
		remaining = 0
	}
	b.Used, b.Remaining, b.Reached = count(used), count(remaining), used >= max
	b.Text = fmt.Sprintf("%d of %d %s used; %d remaining", used, max, what, remaining)
	return b
}

func unset(key string) Bound {
	return Bound{Key: key, Scope: ScopeLoop, Text: "unset"}
}

func bounds(in Input) Bounds {
	l := in.Loop
	lim := l.Limits
	b := Bounds{Limits: []Bound{}, Window: in.Window}
	if len(lim.Configured()) == 0 {
		b.Unbounded, b.Warning = true, config.UnboundedWarning
	}
	stateGone := state.FileName + " could not be read"

	// Loop-wide limits, in the order the decision engine applies them.
	if lim.Deadline == "" {
		b.Limits = append(b.Limits, unset("loop.limits.deadline"))
	} else {
		d := Bound{Key: "loop.limits.deadline", Scope: ScopeLoop, Set: true, Limit: lim.Deadline, Reached: lim.DeadlineReached(in.Now)}
		if at, err := time.Parse(time.RFC3339, lim.Deadline); err == nil && !d.Reached {
			d.RemainingTime = at.Sub(in.Now).Round(time.Second).String()
			d.Text = fmt.Sprintf("%s; %s remaining", lim.Deadline, d.RemainingTime)
		} else {
			d.Text = lim.Deadline + "; reached"
		}
		b.Limits = append(b.Limits, d)
	}
	if lim.MaxSends <= 0 {
		b.Limits = append(b.Limits, unset("loop.limits.max_sends"))
	} else {
		var used int64
		if in.SendsUsed != nil {
			used = *in.SendsUsed
		}
		why := in.SendsError
		if why == "" {
			why = "sends.json could not be read"
		}
		b.Limits = append(b.Limits, countBound("loop.limits.max_sends", ScopeLoop, lim.MaxSends, used, in.SendsUsed != nil, why, "sends"))
	}
	if timeout := lim.NoProgressTimeout.Duration; timeout <= 0 {
		b.Limits = append(b.Limits, unset("loop.limits.no_progress_timeout"))
	} else {
		p := Bound{Key: "loop.limits.no_progress_timeout", Scope: ScopeLoop, Set: true, Limit: timeout.String()}
		switch {
		case in.ProgressError != "":
			p.Unavailable = in.ProgressError
			p.Text = fmt.Sprintf("%s without a verified change; remaining unavailable (%s)", p.Limit, p.Unavailable)
		case in.Progress == nil:
			p.RemainingTime = p.Limit
			p.Text = fmt.Sprintf("%s without a verified change; awaiting the first verified baseline", p.Limit)
		default:
			p.Reached = in.Progress.Reached(in.Now, timeout)
			left := in.Progress.ChangedAt.Add(timeout).Sub(in.Now)
			if left < 0 {
				left = 0
			}
			p.RemainingTime = left.Round(time.Second).String()
			p.Text = fmt.Sprintf("%s without a verified change; %s remaining", p.Limit, p.RemainingTime)
		}
		b.Limits = append(b.Limits, p)
	}

	// Per-loop and per-episode caps. These always have a value: config
	// loading applies their defaults.
	st := in.State
	var newThreads, reprompts, nudges int64
	if st != nil {
		newThreads, reprompts, nudges = int64(st.NewThreadsCreated), int64(st.Reprompts), int64(st.Nudges)
	}
	b.Limits = append(b.Limits,
		countBound("loop.limits.max_new_threads", ScopeLoop, int64(lim.MaxNewThreads), newThreads, st != nil, stateGone, "BB thread creations for handoffs"),
		countBound("loop.errors.max_reprompts", ScopeEpisode, int64(l.Errors.MaxReprompts), reprompts, st != nil, stateGone, "re-prompts in this error episode"),
		countBound("loop.progress.max_nudges", ScopeStall, int64(l.Progress.MaxNudges), nudges, st != nil, stateGone, "nudges since progress or the last turn end"),
	)
	return b
}

func permissions(in Input) Permissions {
	p := Permissions{PermissionMode: NotSetByLooptender, Sandbox: NotSetByLooptender}
	a := in.TargetAgent
	if a == nil {
		p.PermissionMode, p.Sandbox = "unknown: the active target is unavailable", "unknown: the active target is unavailable"
		return p
	}
	var extra []string
	switch {
	case a.Claude != nil:
		if a.Claude.PermissionMode != "" {
			p.PermissionMode, p.PermissionModeSource = a.Claude.PermissionMode, "claude.permission_mode"
		}
		extra = a.Claude.ExtraArgs
	case a.Codex != nil:
		if a.Codex.Sandbox != "" {
			p.Sandbox, p.SandboxSource = a.Codex.Sandbox, "codex.sandbox"
		}
		extra = a.Codex.ExtraArgs
	case a.BB != nil && in.TargetCreated:
		// looptender always passes a permission mode when it creates a
		// thread, so a created thread never inherits a project default.
		p.PermissionMode = a.BB.NewThreadPermission()
		if a.BB.PermissionMode != "" {
			p.PermissionModeSource = "bb.permission_mode, set when looptender created this thread; it may have changed since"
		} else {
			p.PermissionModeSource = "looptender's default, set when it created this thread; it may have changed since"
		}
	}
	if len(extra) > 0 {
		p.ExtraArgs = "extra_args are passed to the agent unchanged and may override the settings above; looptender does not interpret them"
	}
	return p
}

func lanes(a *config.Agent) LanePolicy {
	lp := LanePolicy{Text: []string{laneNotSupervised}}
	var bb *config.BB
	if a != nil {
		bb = a.BB
	}
	if bb.RetryErroredLanes() {
		lp.RetryErrored, lp.MaxPerCheck, lp.MaxPerEpisode = true, bb.LanesPerCheck(), bb.LanesPerEpisode()
		lp.Text = append(lp.Text, fmt.Sprintf("Lane recovery is on (bb.children.retry_errored): looptender may ask BB to retry a lane's failed turn by reference, at most %d per check and %d per episode, and never sends a lane any text.", lp.MaxPerCheck, lp.MaxPerEpisode))
	} else {
		lp.Text = append(lp.Text, laneRetryOff)
	}
	lp.Text = append(lp.Text, laneStop)
	return lp
}

func commands(prefix, cfg string) []Command {
	run := func(args string) string { return prefix + " " + args + " -c " + cfg }
	return []Command{
		{Name: "context", Command: run("context"), Use: "show this context again; read-only"},
		{Name: "status", Command: run("status"), Use: "show the loop's recorded status; read-only"},
		{Name: "complete", Command: run("complete") + " --note 'Task completed and verified'", Use: "declare the entire task completed and verified; the loop holds until it is resumed, which is for a person"},
		{Name: "await-user", Command: run("await-user") + " --note 'Human input required; see the conversation'", Use: "ask for a person when all remaining work needs one; the loop holds until it is resumed, which is for a person"},
	}
}

// Text is the context as plain lines, in a fixed order.
func (v View) Text() string {
	var b strings.Builder
	line := func(label, format string, a ...any) {
		fmt.Fprintf(&b, "%-10s %s\n", label+":", fmt.Sprintf(format, a...))
	}
	line("context", "schema version %d", v.SchemaVersion)
	line("loop", "%s", v.Loop)
	line("adapter", "%s", v.Adapter)
	if t := v.Target; t != nil {
		id := ""
		switch {
		case t.BBThread != "":
			id = ", BB thread " + t.BBThread
		case t.Workdir != "":
			id = ", working directory " + t.Workdir
		}
		line("target", "%s (%s%s; identity from configuration only, not verified with the provider)", t.Route, t.Adapter, id)
	} else {
		line("target", "unavailable (%s)", v.TargetUnavailable)
	}
	line("owner", "%s", v.Owner.Text)
	line("caller", "%s", v.CallerIdentity)
	line("control", "%s", v.ControlState)
	switch {
	case v.ControlState == "unavailable":
		line("holds", "unavailable (%s could not be read)", control.FileName)
	case len(v.Holds) == 0:
		line("holds", "none")
	default:
		line("holds", "%s", strings.Join(v.Holds, ", "))
	}
	if v.Note != "" {
		line("note", "%s", v.Note)
	}
	if v.Bounds.Unbounded {
		line("bounds", "warning: %s", v.Bounds.Warning)
	}
	for _, x := range v.Bounds.Limits {
		fmt.Fprintf(&b, "  %s: %s\n", x.Key, x.Text)
	}
	w := v.Bounds.Window
	switch {
	case w.Error != "":
		line("window", "unavailable (%s)", w.Error)
	case !w.Set:
		line("window", "%s", w.Text)
	default:
		state := "outside"
		if w.Inside {
			state = "inside"
		}
		line("window", "%s now (%s)", state, w.Text)
	}
	pm := v.Permissions.PermissionMode
	if v.Permissions.PermissionModeSource != "" {
		pm += " (" + v.Permissions.PermissionModeSource + ")"
	}
	line("permission", "%s", pm)
	sb := v.Permissions.Sandbox
	if v.Permissions.SandboxSource != "" {
		sb += " (" + v.Permissions.SandboxSource + ")"
	}
	line("sandbox", "%s", sb)
	if v.Permissions.ExtraArgs != "" {
		line("extra args", "%s", v.Permissions.ExtraArgs)
	}
	for _, s := range v.Lanes.Text {
		line("lanes", "%s", s)
	}
	b.WriteString("commands:\n")
	for _, c := range v.SessionCommands {
		fmt.Fprintf(&b, "  %s\n    %s\n", c.Command, c.Use)
	}
	b.WriteString("authority:\n")
	for _, s := range v.RetainedAuthority {
		fmt.Fprintf(&b, "  %s\n", s)
	}
	if len(v.Unavailable) > 0 {
		line("unreadable", "%s", strings.Join(v.Unavailable, ", "))
	}
	return b.String()
}
