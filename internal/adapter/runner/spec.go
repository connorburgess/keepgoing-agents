package runner

import (
	"context"
	"fmt"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// A Spec is everything keepgoing needs to know about one command-line agent
// that runs one turn per process. The mechanics every such agent shares (the
// detached turn, the prompt file, per-target session storage, output bounds
// and the owned stop) are in Dir and Headless; a Spec holds only what differs
// between agents. See "Runner contract" in docs/architecture.md.
type Spec struct {
	// Name is the adapter name in configuration and the probe kind.
	Name string
	// Provider is the provider label used when a loop sets none.
	Provider string
	// DefaultCLI is the executable used when the settings name none.
	DefaultCLI string
	// AccountEnv is the environment variable that selects the agent's account
	// directory, or "" when it has none.
	AccountEnv string
	// SettingNames are the configuration keys the agent accepts under its
	// own name, for documentation and diagnostics.
	SettingNames []string
	// Prompt says how a turn receives its prompt.
	Prompt PromptVia
	// Capabilities declares what the agent does and reports.
	Capabilities Capabilities

	// MaxOutput bounds the turn output that is read, in bytes. Zero keeps
	// the built-in runners' unbounded read.
	MaxOutput int64
	// Argv builds the command for one turn from the agent's settings.
	// session is the saved session to resume, or "" to start one. With
	// PromptFile, promptFile is the private file holding the prompt.
	Argv func(set Settings, session, promptFile string) []string
	// Read interprets a finished turn from structured output and the exit
	// code only, never from prose. It must not have side effects.
	Read func(turn Status) Reading
	// Limits, when set, is a structured account-limit lookup used when a
	// failed turn or probe could not be classified.
	Limits *LimitLookup
	// Probe is how the agent's account and model are checked.
	Probe ProbeSpec
}

// PromptVia is how a turn receives its prompt.
type PromptVia string

// A prompt is never put in argv.
const (
	// PromptStdin passes the prompt on standard input, from a private file.
	PromptStdin PromptVia = "stdin"
	// PromptFile gives the agent the path of the private prompt file and an
	// empty standard input.
	PromptFile PromptVia = "file"
)

// Capabilities declares what an agent does itself and what it reports in a
// structured form. False means "not established", not "known absent":
// keepgoing must not rely on anything that is false.
type Capabilities struct {
	// RetriesItself, FailsOverItself and SchedulesItself describe an agent
	// that supervises itself: it retries a failed request before the turn
	// ends, moves between providers or accounts, or starts its own later
	// turns. Nothing acts on them yet and no registered agent may declare
	// them (a test enforces that) until something does.
	RetriesItself   bool
	FailsOverItself bool
	SchedulesItself bool
	// ChildWork: the agent runs work that outlives the turn's process. It
	// must agree with the child rows of the capability matrix.
	ChildWork bool
	// ExactResume: a later turn continues exactly the saved session. The
	// resume argv must carry the saved session.
	ExactResume bool
	// Stoppable: a running turn can be stopped through the owned wrapper.
	// The shared runner refuses to stop a turn without it.
	Stoppable bool
	// StructuredErrors: a failure carries a machine-readable code or status.
	// The reader must classify a coded failure and return evidence for it.
	StructuredErrors bool
	// StructuredWaiting: the agent reports waiting on a person in a
	// machine-readable form. A reading's Waiting is ignored without it.
	StructuredWaiting bool
}

// Settings are an agent's settings as a specification sees them, whatever
// the agent: the four every runner needs, and the agent's own by name.
type Settings struct {
	CLI     string
	Workdir string
	Model   string
	Env     map[string]string
	// Options are the agent's other settings by their configuration key,
	// as text. An unset setting is absent.
	Options map[string]string
	// ExtraArgs are passed through as they are.
	ExtraArgs []string
}

// Reading is what a finished turn's output says.
type Reading struct {
	// Session is the session identity the turn reported, or "".
	Session string
	// Completed: the turn ended healthy, by the agent's report and its exit
	// code together.
	Completed bool
	// Waiting: the agent reported, in a structured form, that it is waiting
	// on a person.
	Waiting bool
	// Class classifies a failed turn; ErrUnknown when nothing structured
	// says.
	Class decide.ErrorClass
	// Evidence is the structured error evidence. An empty Source means the
	// output held none, and the runner's own exit evidence is used.
	Evidence evidence.Detail
}

// Limits is what an agent's own interface says about its account's usage
// limit.
type Limits struct {
	// Blocked: ordinary usage is not allowed, or a limit is reported reached.
	Blocked     bool
	ReachedType string
	// ResetsAt is when the reached window lifts, when reported.
	ResetsAt time.Time
	// Windows are the usage windows reported, blocked or not. They are
	// readings only and never decide Blocked.
	Windows []LimitWindow
}

// LimitWindow is one usage window. Name is the agent's own name for it.
// ResetsAt is zero when none was given.
type LimitWindow struct {
	Name        string
	UsedPercent int
	ResetsAt    time.Time
}

// UsageReading is one usage window read from a probe's own output. Used is a
// fraction from 0 to 1.
type UsageReading struct {
	Key      string
	Used     float64
	ResetsAt *time.Time
	Status   string
}

// LimitLookup asks the agent's own interface whether the account is blocked
// by a usage limit.
type LimitLookup struct {
	Read func(ctx context.Context, cli string, env []string) (Limits, error)
	// UsageSource labels a usage reading taken from the lookup's windows, or
	// "" when they are not kept as a reading.
	UsageSource string
	// EvidenceSource and EvidenceCode label a turn classified by the lookup.
	EvidenceSource, EvidenceCode string
	// ProbeDetail prefixes the reached limit type in a probe result.
	ProbeDetail string
}

// ProbeSpec is one tiny real request that shows the account and model work.
type ProbeSpec struct {
	// Command builds the probe command and its standard input. Model may be
	// "". A built-in probe passes its fixed prompt as an argument; a defined
	// agent receives it as its turns do.
	Command func(p ProbeRequest) (argv []string, stdin string)
	// Usage, when set, reads usage windows from the probe's own output, and
	// UsageSource labels the reading.
	Usage       func(stdout []byte) []UsageReading
	UsageSource string
	// Judge reads the probe's output and exit code.
	Judge func(stdout []byte, exitCode int) ProbeVerdict
	// Trim, when set, drops output that describes the local installation
	// before a failed probe's output is kept.
	Trim func(stdout []byte) []byte
}

// ProbeRequest is what a probe command is built from.
type ProbeRequest struct {
	// CLI is the executable, already resolved: the candidate's own, or the
	// specification's default.
	CLI    string
	Model  string
	Prompt string
	// PromptFile is a private file holding Prompt, for PromptFile agents.
	PromptFile string
}

// ProbeVerdict is what a probe's output says.
type ProbeVerdict struct {
	OK bool
	// Class is set when the probe failed.
	Class decide.ErrorClass
	// Detail is a short code, never model output.
	Detail              string
	CredentialExpiresAt *time.Time
}

// ObservationInputs lists the decide.Observation fields a runner built from
// the specification can set, sorted. The shared runner sets the same three
// for every agent; the rest follow from what the specification declares.
func (s *Spec) ObservationInputs() []string {
	in := []string{"ErrorClass", "Evidence", "State"}
	if s.Capabilities.StructuredWaiting {
		in = append([]string{"DeferredInteraction"}, in...)
	}
	if s.Limits != nil {
		in = append(in[:len(in)-1], "ResetsAt", "State")
	}
	return in
}

// Abilities says in plain words what keepgoing can and cannot do with an
// agent, from its specification alone. Whatever is not declared is listed as
// cannot: keepgoing treats it as unknown.
func (s *Spec) Abilities() (can, cannot []string) {
	add := func(ok bool, yes, no string) {
		if ok {
			can = append(can, yes)
		} else {
			cannot = append(cannot, no)
		}
	}
	can = append(can, "start a turn with the prompt passed by "+string(s.Prompt))
	add(s.Capabilities.ExactResume, "continue the same session on later turns", "continue a session: every turn starts a new one")
	add(s.Capabilities.Stoppable, "stop a running turn", "stop a running turn")
	add(s.Capabilities.StructuredErrors, "classify a failed turn from its declared codes", "classify a failed turn: every failure is unknown")
	add(s.Capabilities.StructuredWaiting, "hold for a person when a turn ends waiting on one", "tell that a turn is waiting on a person")
	add(s.Probe.Command != nil, "check the account and model with a probe, so it can be a fallback", "check the account and model: it cannot be a fallback without a command probe")
	add(s.Limits != nil, "ask whether an unclassified failure is a usage limit", "tell a usage limit from another failure unless a declared code says so")
	add(s.MaxOutput > 0, "bound the output it reads", "bound the output it reads")
	add(s.Capabilities.ChildWork, "see work that outlives a turn", "see work that outlives a turn")
	add(s.Capabilities.RetriesItself || s.Capabilities.FailsOverItself || s.Capabilities.SchedulesItself, "defer to the agent's own retries, failover or scheduling", "know whether the agent retries, fails over or schedules itself")
	return can, cannot
}

// specs is the registry, in the order agents are listed to people.
var specs = []*Spec{claudeSpec, codexSpec}

// Specs returns the registered agent specifications.
func Specs() []*Spec { return append([]*Spec(nil), specs...) }

// SpecFor returns the specification registered under an adapter name.
func SpecFor(name string) (*Spec, bool) {
	for _, s := range specs {
		if s.Name == name {
			return s, true
		}
	}
	return nil, false
}

// SpecForProvider returns the specification whose default provider label is
// provider.
func SpecForProvider(provider string) (*Spec, bool) {
	for _, s := range specs {
		if s.Provider == provider {
			return s, true
		}
	}
	return nil, false
}

// KeepsSessionFile reports whether an adapter saves a resume identifier in
// its target directory that diagnostics and the run graph may show. Cursor
// does and is not on the contract yet. A defined agent's identifier is used
// to resume but its shape is the agent's own, so it is not shown.
func KeepsSessionFile(adapterName string) bool {
	_, ok := SpecFor(adapterName)
	return ok || adapterName == "cursor"
}

// Headless runs one agent from its Spec: one turn per Send, resuming the same
// session. It implements adapter.Adapter.
type Headless struct {
	spec *Spec
	set  Settings
	dir  Dir
}

// New returns the runner for a's adapter, storing turn files in dir. ok is
// false when no specification is registered for it or a carries no settings
// for it.
func New(a config.Agent, dir string) (h *Headless, ok bool) {
	spec, set, ok := Resolve(a)
	if !ok || (spec.Prompt != PromptStdin && spec.Prompt != PromptFile) {
		// No settings for this agent, or a way of passing the prompt the
		// shared runner does not implement.
		return nil, false
	}
	return &Headless{spec: spec, set: set, dir: Dir(dir)}, true
}

// Resolve returns the specification and settings for an agent definition: a
// registered agent with its typed settings, or one defined in configuration.
// A specification never sees the configuration; it is handed Settings.
func Resolve(a config.Agent) (*Spec, Settings, bool) {
	if a.Adapter == config.CustomAdapter {
		if a.Custom == nil {
			return nil, Settings{}, false
		}
		spec, set := specFromDefinition(*a.Custom)
		return spec, set, true
	}
	spec, ok := SpecFor(a.Adapter)
	if !ok {
		return nil, Settings{}, false
	}
	read, ok := builtinSettings[spec.Name]
	if !ok {
		return nil, Settings{}, false
	}
	set, ok := read(a)
	return spec, set, ok
}

// builtinSettings reads each registered agent's typed settings into Settings.
var builtinSettings = map[string]func(config.Agent) (Settings, bool){
	"claude": claudeSettings,
	"codex":  codexSettings,
}

// Name implements adapter.Adapter.
func (h *Headless) Name() string { return h.spec.Name }

// Spec returns the specification the runner was built from.
func (h *Headless) Spec() *Spec { return h.spec }

// Args builds the argv for the next turn.
func (h *Headless) Args() []string {
	return h.spec.Argv(h.set, h.dir.Session(), h.dir.path("prompt"))
}

// status reads the latest turn within the specification's output bound.
func (h *Headless) status() (Status, error) { return h.dir.statusWithOutputLimit(h.spec.MaxOutput) }

// Observe implements adapter.Adapter.
func (h *Headless) Observe(ctx context.Context) (obs decide.Observation, err error) {
	st, err := h.status()
	if err != nil {
		return decide.Observation{State: decide.Unknown}, err
	}
	defer func() {
		if obs.Evidence.Source == "" {
			obs.Evidence = runnerEvidence(st)
		}
	}()
	switch st.Phase {
	case NeverRun:
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone}, nil
	case Running:
		return decide.Observation{State: decide.Active, ErrorClass: decide.ErrNone}, nil
	case Crashed:
		return decide.Observation{State: decide.Error, ErrorClass: decide.ErrUnknown}, nil
	}
	r := h.spec.Read(st)
	h.dir.SaveSession(r.Session)
	// A turn that ended waiting on a person raises a hold once: it is
	// reported until the supervisor has saved the hold (AdoptInteraction).
	waiting := h.waiting(r) && !h.dir.waitAdopted(st.Record)
	if r.Completed {
		return decide.Observation{State: decide.Idle, ErrorClass: decide.ErrNone, DeferredInteraction: waiting, Evidence: r.Evidence}, nil
	}
	obs = decide.Observation{State: decide.Error, ErrorClass: r.Class, DeferredInteraction: waiting, Evidence: r.Evidence}
	// Some agents report a usage limit only as prose. Ask the agent's own
	// structured interface instead of reading it.
	if l := h.spec.Limits; l != nil && obs.ErrorClass == decide.ErrUnknown {
		if limits, err := l.Read(ctx, h.set.CLI, envList(h.set.Env)); err == nil && limits.Blocked {
			obs.ErrorClass, obs.ResetsAt = decide.ErrRateLimit, limits.ResetsAt
			obs.Evidence = evidence.Clean(evidence.Detail{Source: l.EvidenceSource, Code: l.EvidenceCode, ExitCode: runnerEvidence(st).ExitCode})
		}
	}
	return obs, nil
}

// waiting honours a reading's Waiting only for an agent that declares it
// reports that in a structured form.
func (h *Headless) waiting(r Reading) bool {
	return r.Waiting && h.spec.Capabilities.StructuredWaiting
}

// Send starts a turn. A running turn cannot be steered or queued into.
func (h *Headless) Send(ctx context.Context, prompt string, mode adapter.Mode) error {
	st, err := h.status()
	if err != nil {
		return err
	}
	if st.Phase == Running {
		return adapter.ErrUnsupported{Op: fmt.Sprintf("sending to a running headless turn (%s mode)", mode)}
	}
	if st.Phase == Finished {
		h.dir.SaveSession(h.spec.Read(st).Session)
	}
	if h.spec.Prompt == PromptFile {
		return h.dir.StartWithPromptFile(h.Args(), h.set.Workdir, prompt, envList(h.set.Env))
	}
	return h.dir.Start(h.Args(), h.set.Workdir, prompt, envList(h.set.Env))
}

// AdoptInteraction implements adapter.InteractionAdopter: the hold for the
// latest turn's wait is saved, so the turn stops reporting it. Nothing is
// sent or approved. After a person resumes the loop the turn is read by its
// other rules, and the next send continues the session.
func (h *Headless) AdoptInteraction(ctx context.Context) error {
	st, err := h.status()
	if err != nil || st.Phase != Finished {
		return err
	}
	return h.dir.adoptWait(st.Record)
}

// Stop implements adapter.Adapter. An agent that does not declare it can be
// stopped is never signalled.
func (h *Headless) Stop(ctx context.Context) error {
	if !h.spec.Capabilities.Stoppable {
		return adapter.ErrUnsupported{Op: "stopping a " + h.spec.Name + " turn"}
	}
	return h.dir.Stop()
}
