package loopcontext

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/state"
)

var now = time.Date(2030, 1, 1, 12, 0, 0, 0, time.UTC)

func bbLoop() config.Loop {
	l := config.Loop{Name: "example-app", StateDir: "/private/example-loop/state"}
	l.Agent = config.Agent{Adapter: "bb", Provider: "claude-code", Group: "claude-code", BB: &config.BB{Thread: "thr_example1"}}
	l.Errors.MaxReprompts = config.DefaultMaxReprompts
	l.Progress.MaxNudges = config.DefaultMaxNudges
	l.Limits.MaxNewThreads = config.DefaultMaxNewThreads
	return l
}

func base() Input {
	l := bbLoop()
	a := l.Agent
	return Input{
		Loop: l, CommandPrefix: "keepgoing", ConfigArg: "'/private/example-loop/looptender.yaml'",
		Control: &control.File{Enabled: true}, State: &state.File{}, TargetAgent: &a,
		Window: Window{Inside: true, Text: "no window configured"}, Now: now,
	}
}

func bound(t *testing.T, v View, key string) Bound {
	t.Helper()
	for _, b := range v.Bounds.Limits {
		if b.Key == key {
			return b
		}
	}
	t.Fatalf("bound %s missing from %+v", key, v.Bounds.Limits)
	return Bound{}
}

func n(i int64) *int64 { return &i }

func wantCount(t *testing.T, b Bound, max, used, remaining int64, reached bool) {
	t.Helper()
	if !b.Set || b.Max == nil || *b.Max != max || b.Used == nil || *b.Used != used || b.Remaining == nil || *b.Remaining != remaining || b.Reached != reached || b.Unavailable != "" {
		t.Fatalf("%s = %+v; want max %d used %d remaining %d reached %t", b.Key, b, max, used, remaining, reached)
	}
}

func TestBuild(t *testing.T) {
	cases := []struct {
		name  string
		edit  func(*Input)
		check func(*testing.T, View)
	}{
		{"a new BB loop", func(*Input) {}, func(t *testing.T, v View) {
			if v.SchemaVersion != 1 || v.Loop != "example-app" || v.Adapter != "bb" {
				t.Fatalf("header %+v", v)
			}
			want := &Target{Route: "primary", Adapter: "bb", Provider: "claude-code", BBThread: "thr_example1", IdentityEvidence: "configuration_only"}
			if !reflect.DeepEqual(v.Target, want) {
				t.Fatalf("target %+v", v.Target)
			}
			if v.Owner != (Owner{Text: "none recorded"}) {
				t.Fatalf("owner %+v", v.Owner)
			}
			if v.ControlState != "enabled" || len(v.Holds) != 0 || len(v.Unavailable) != 0 {
				t.Fatalf("control %q holds %v unavailable %v", v.ControlState, v.Holds, v.Unavailable)
			}
			if !v.Bounds.Unbounded || v.Bounds.Warning != config.UnboundedWarning {
				t.Fatalf("bounds %+v", v.Bounds)
			}
			for _, key := range []string{"loop.limits.deadline", "loop.limits.max_sends", "loop.limits.no_progress_timeout"} {
				if b := bound(t, v, key); b.Set || b.Text != "unset" || b.Scope != ScopeLoop {
					t.Fatalf("%s = %+v; an unset loop-wide limit must say unset", key, b)
				}
			}
			wantCount(t, bound(t, v, "loop.limits.max_new_threads"), 3, 0, 3, false)
			wantCount(t, bound(t, v, "loop.errors.max_reprompts"), 3, 0, 3, false)
			wantCount(t, bound(t, v, "loop.progress.max_nudges"), 3, 0, 3, false)
			if v.Permissions != (Permissions{PermissionMode: NotSetByLooptender, Sandbox: NotSetByLooptender}) {
				t.Fatalf("permissions %+v", v.Permissions)
			}
			if v.Lanes.Supervised || v.Lanes.RetryErrored || !reflect.DeepEqual(v.Lanes.Text, []string{laneNotSupervised, laneRetryOff, laneStop}) {
				t.Fatalf("lanes %+v", v.Lanes)
			}
			if v.CallerIdentity != CallerIdentity || !reflect.DeepEqual(v.RetainedAuthority, RetainedAuthority) {
				t.Fatalf("fixed sentences changed: %q %q", v.CallerIdentity, v.RetainedAuthority)
			}
			var names []string
			for _, c := range v.SessionCommands {
				names = append(names, c.Name)
				if !strings.HasPrefix(c.Command, "keepgoing "+c.Name+" -c '/private/example-loop/looptender.yaml'") {
					t.Fatalf("command %q", c.Command)
				}
			}
			if !reflect.DeepEqual(names, []string{"context", "status", "complete", "await-user"}) {
				t.Fatalf("session commands %v", names)
			}
		}},
		{"holds, a note and an owner", func(in *Input) {
			in.Control = &control.File{Enabled: true, Paused: true, AwaitingUser: true, Stop: true, Completed: true, Note: "Example question",
				PendingTransfer: &control.PendingBBTransfer{}, Ownership: &control.Ownership{ThreadID: "thr_owner1", Generation: 2}}
		}, func(t *testing.T, v View) {
			if !reflect.DeepEqual(v.Holds, []string{"transfer pending", "stop requested", "paused", "awaiting a person", "completed"}) {
				t.Fatalf("holds %v", v.Holds)
			}
			if v.ControlState != "stop requested" || v.Note != "Example question" {
				t.Fatalf("control %q note %q", v.ControlState, v.Note)
			}
			if v.Owner != (Owner{Recorded: true, BBThread: "thr_owner1", Generation: 2, Text: "BB thread thr_owner1, generation 2"}) {
				t.Fatalf("owner %+v", v.Owner)
			}
		}},
		{"a disabled loop has no hold but says disabled", func(in *Input) { in.Control = &control.File{} }, func(t *testing.T, v View) {
			if v.ControlState != "disabled" || len(v.Holds) != 0 {
				t.Fatalf("control %q holds %v", v.ControlState, v.Holds)
			}
		}},
		{"an invalid owner record is not repeated", func(in *Input) {
			in.Control = &control.File{Enabled: true, Ownership: &control.Ownership{ThreadID: "not a thread", Generation: 1}}
		}, func(t *testing.T, v View) {
			if !v.Owner.Recorded || v.Owner.BBThread != "" || !strings.HasPrefix(v.Owner.Text, "invalid ownership record") {
				t.Fatalf("owner %+v", v.Owner)
			}
		}},
		{"loop-wide limits with used and remaining", func(in *Input) {
			in.Loop.Limits.MaxSends = 40
			in.Loop.Limits.Deadline = "2030-01-01T18:00:00Z"
			in.Loop.Limits.NoProgressTimeout = config.Duration{Duration: 6 * time.Hour}
			in.SendsUsed = n(12)
			in.Progress = &state.Progress{ChangedAt: now.Add(-2 * time.Hour), ObservedAt: now.Add(-time.Minute)}
			in.State = &state.File{Reprompts: 2, Nudges: 3, NewThreadsCreated: 1}
		}, func(t *testing.T, v View) {
			if v.Bounds.Unbounded || v.Bounds.Warning != "" {
				t.Fatalf("bounds %+v", v.Bounds)
			}
			d := bound(t, v, "loop.limits.deadline")
			if !d.Set || d.Limit != "2030-01-01T18:00:00Z" || d.RemainingTime != "6h0m0s" || d.Reached {
				t.Fatalf("deadline %+v", d)
			}
			wantCount(t, bound(t, v, "loop.limits.max_sends"), 40, 12, 28, false)
			p := bound(t, v, "loop.limits.no_progress_timeout")
			if !p.Set || p.Limit != "6h0m0s" || p.RemainingTime != "4h0m0s" || p.Reached {
				t.Fatalf("no progress %+v", p)
			}
			wantCount(t, bound(t, v, "loop.limits.max_new_threads"), 3, 1, 2, false)
			wantCount(t, bound(t, v, "loop.errors.max_reprompts"), 3, 2, 1, false)
			wantCount(t, bound(t, v, "loop.progress.max_nudges"), 3, 3, 0, true)
			var keys []string
			for _, b := range v.Bounds.Limits {
				keys = append(keys, b.Key)
			}
			want := []string{"loop.limits.deadline", "loop.limits.max_sends", "loop.limits.no_progress_timeout", "loop.limits.max_new_threads", "loop.errors.max_reprompts", "loop.progress.max_nudges"}
			if !reflect.DeepEqual(keys, want) {
				t.Fatalf("order %v", keys)
			}
		}},
		{"reached limits never show a negative remainder", func(in *Input) {
			in.Loop.Limits.MaxSends = 5
			in.Loop.Limits.Deadline = "2029-12-31T00:00:00Z"
			in.Loop.Limits.NoProgressTimeout = config.Duration{Duration: time.Hour}
			in.SendsUsed = n(7)
			in.Progress = &state.Progress{ChangedAt: now.Add(-3 * time.Hour), ObservedAt: now.Add(-time.Minute)}
		}, func(t *testing.T, v View) {
			wantCount(t, bound(t, v, "loop.limits.max_sends"), 5, 7, 0, true)
			if d := bound(t, v, "loop.limits.deadline"); !d.Reached || d.RemainingTime != "" {
				t.Fatalf("deadline %+v", d)
			}
			if p := bound(t, v, "loop.limits.no_progress_timeout"); !p.Reached || p.RemainingTime != "0s" {
				t.Fatalf("no progress %+v", p)
			}
		}},
		{"unreadable accounting is unavailable, not zero", func(in *Input) {
			in.Loop.Limits.MaxSends = 40
			in.Loop.Limits.NoProgressTimeout = config.Duration{Duration: time.Hour}
			in.SendsError = "sends.json could not be read"
			in.ProgressError = "progress.json could not be read"
		}, func(t *testing.T, v View) {
			s := bound(t, v, "loop.limits.max_sends")
			if s.Used != nil || s.Remaining != nil || s.Unavailable != "sends.json could not be read" {
				t.Fatalf("sends %+v", s)
			}
			p := bound(t, v, "loop.limits.no_progress_timeout")
			if p.RemainingTime != "" || p.Unavailable != "progress.json could not be read" {
				t.Fatalf("no progress %+v", p)
			}
		}},
		{"no progress baseline yet", func(in *Input) {
			in.Loop.Limits.NoProgressTimeout = config.Duration{Duration: time.Hour}
		}, func(t *testing.T, v View) {
			if p := bound(t, v, "loop.limits.no_progress_timeout"); p.RemainingTime != "1h0m0s" || p.Reached || !strings.Contains(p.Text, "awaiting the first verified baseline") {
				t.Fatalf("no progress %+v", p)
			}
		}},
		{"unreadable control and state", func(in *Input) {
			in.Control, in.State, in.TargetAgent = nil, nil, nil
		}, func(t *testing.T, v View) {
			if !reflect.DeepEqual(v.Unavailable, []string{"control.json", "state.json"}) {
				t.Fatalf("unavailable %v", v.Unavailable)
			}
			if v.ControlState != "unavailable" || len(v.Holds) != 0 || v.Owner.Recorded || !strings.HasPrefix(v.Owner.Text, "unavailable") {
				t.Fatalf("control %q holds %v owner %+v", v.ControlState, v.Holds, v.Owner)
			}
			if v.Target != nil || v.TargetUnavailable == "" {
				t.Fatalf("target %+v %q", v.Target, v.TargetUnavailable)
			}
			for _, key := range []string{"loop.limits.max_new_threads", "loop.errors.max_reprompts", "loop.progress.max_nudges"} {
				if b := bound(t, v, key); b.Used != nil || b.Unavailable == "" {
					t.Fatalf("%s = %+v", key, b)
				}
			}
			if !strings.HasPrefix(v.Permissions.PermissionMode, "unknown") {
				t.Fatalf("permissions %+v", v.Permissions)
			}
		}},
		{"an active route missing from the configuration", func(in *Input) {
			in.State = &state.File{ActiveTarget: "codex-main"}
			in.TargetAgent = nil
		}, func(t *testing.T, v View) {
			if v.Target != nil || !strings.Contains(v.TargetUnavailable, `"codex-main"`) || len(v.Unavailable) != 0 {
				t.Fatalf("target %+v %q", v.Target, v.TargetUnavailable)
			}
		}},
		{"headless Claude Code with a permission mode and extra arguments", func(in *Input) {
			in.TargetAgent = &config.Agent{Adapter: "claude", Provider: "claude-code", Claude: &config.Claude{Workdir: "/private/example-app", PermissionMode: "acceptEdits", ExtraArgs: []string{"--example"}}}
		}, func(t *testing.T, v View) {
			p := v.Permissions
			if p.PermissionMode != "acceptEdits" || p.PermissionModeSource != "claude.permission_mode" || p.Sandbox != NotSetByLooptender || p.ExtraArgs == "" {
				t.Fatalf("permissions %+v", p)
			}
			if strings.Contains(p.ExtraArgs, "--example") {
				t.Fatalf("extra arguments are not repeated: %q", p.ExtraArgs)
			}
			if v.Target.Workdir != "/private/example-app" || v.Target.BBThread != "" {
				t.Fatalf("target %+v", v.Target)
			}
		}},
		{"headless Claude Code with nothing set", func(in *Input) {
			in.TargetAgent = &config.Agent{Adapter: "claude", Claude: &config.Claude{Workdir: "/private/example-app"}}
		}, func(t *testing.T, v View) {
			if v.Permissions != (Permissions{PermissionMode: NotSetByLooptender, Sandbox: NotSetByLooptender}) {
				t.Fatalf("permissions %+v", v.Permissions)
			}
		}},
		{"Codex with a sandbox", func(in *Input) {
			in.TargetAgent = &config.Agent{Adapter: "codex", Provider: "codex", Codex: &config.Codex{Workdir: "/private/example-app", Sandbox: "workspace-write"}}
		}, func(t *testing.T, v View) {
			if p := v.Permissions; p.Sandbox != "workspace-write" || p.SandboxSource != "codex.sandbox" || p.PermissionMode != NotSetByLooptender {
				t.Fatalf("permissions %+v", p)
			}
		}},
		{"a process target has no identity beyond its route", func(in *Input) {
			in.TargetAgent = &config.Agent{Adapter: "process", Process: &config.Process{StatusCommand: []string{"example-status"}}}
		}, func(t *testing.T, v View) {
			if v.Target.BBThread != "" || v.Target.Workdir != "" || v.Target.IdentityEvidence != IdentityEvidence {
				t.Fatalf("target %+v", v.Target)
			}
			if v.Permissions.PermissionMode != NotSetByLooptender {
				t.Fatalf("permissions %+v", v.Permissions)
			}
		}},
		{"a BB thread looptender created gets its permission mode reported", func(in *Input) {
			in.State = &state.File{ActiveTarget: "claude-new"}
			in.TargetAgent = &config.Agent{Adapter: "bb", Provider: "claude-code", BB: &config.BB{Thread: "thr_created1", NewThread: true}}
			in.TargetCreated = true
		}, func(t *testing.T, v View) {
			if p := v.Permissions; p.PermissionMode != config.DefaultNewThreadPermission || !strings.HasPrefix(p.PermissionModeSource, "looptender's default") {
				t.Fatalf("permissions %+v", p)
			}
			if v.Target.Route != "claude-new" || v.Target.BBThread != "thr_created1" {
				t.Fatalf("target %+v", v.Target)
			}
		}},
		{"a created BB thread with a configured permission mode", func(in *Input) {
			in.TargetAgent = &config.Agent{Adapter: "bb", BB: &config.BB{Thread: "thr_created1", NewThread: true, PermissionMode: "auto"}}
			in.TargetCreated = true
		}, func(t *testing.T, v View) {
			if p := v.Permissions; p.PermissionMode != "auto" || !strings.HasPrefix(p.PermissionModeSource, "bb.permission_mode") {
				t.Fatalf("permissions %+v", p)
			}
		}},
		{"lane recovery on", func(in *Input) {
			in.TargetAgent.BB = &config.BB{Thread: "thr_example1", Children: &config.BBChildren{RetryErrored: true, MaxPerCheck: 2}}
		}, func(t *testing.T, v View) {
			l := v.Lanes
			if l.Supervised || !l.RetryErrored || l.MaxPerCheck != 2 || l.MaxPerEpisode != config.DefaultLanesPerEpisode {
				t.Fatalf("lanes %+v", l)
			}
			if len(l.Text) != 3 || l.Text[0] != laneNotSupervised || !strings.Contains(l.Text[1], "at most 2 per check and 20 per episode") || l.Text[2] != laneStop {
				t.Fatalf("lane text %q", l.Text)
			}
		}},
		{"an operating window", func(in *Input) {
			in.Window = Window{Set: true, Inside: false, Text: "outside window (example)"}
		}, func(t *testing.T, v View) {
			if v.Bounds.Window != (Window{Set: true, Text: "outside window (example)"}) {
				t.Fatalf("window %+v", v.Bounds.Window)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			in := base()
			tc.edit(&in)
			v := Build(in)
			tc.check(t, v)
			// The JSON form never has null lists, so a reader can range
			// over every list without a check.
			b, err := json.Marshal(v)
			if err != nil {
				t.Fatal(err)
			}
			for _, field := range []string{"holds", "limits", "sessionCommands", "retainedAuthority", "unavailable", "text"} {
				if strings.Contains(string(b), `"`+field+`":null`) {
					t.Fatalf("%s is null: %s", field, b)
				}
			}
			if strings.Contains(string(b), "device") {
				t.Fatalf("the context names no device: %s", b)
			}
			if text := v.Text(); !strings.Contains(text, "authority:\n  Supervision grants no permission.\n") {
				t.Fatalf("text:\n%s", text)
			}
		})
	}
}

// Build reads only its input: the same input gives the same context, and
// the input is not changed.
func TestBuildIsPure(t *testing.T) {
	in := base()
	in.Control.Ownership = &control.Ownership{ThreadID: "thr_owner1", Generation: 1}
	before, _ := json.Marshal(in.Control)
	a, b := Build(in), Build(in)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("two builds of one input differ")
	}
	after, _ := json.Marshal(in.Control)
	if string(before) != string(after) {
		t.Fatal("Build changed its input")
	}
	a.RetainedAuthority[0] = "changed"
	if RetainedAuthority[0] != "Supervision grants no permission." {
		t.Fatal("a view shares the fixed sentences")
	}
}

func TestText(t *testing.T) {
	in := base()
	in.Loop.Limits.MaxSends = 40
	in.SendsUsed = n(12)
	in.Control.Ownership = &control.Ownership{ThreadID: "thr_owner1", Generation: 2}
	in.Control.Paused = true
	want := `context:   schema version 1
loop:      example-app
adapter:   bb
target:    primary (bb, BB thread thr_example1; identity from configuration only, not verified with the provider)
owner:     BB thread thr_owner1, generation 2
caller:    ` + CallerIdentity + `
control:   paused
holds:     paused
  loop.limits.deadline: unset
  loop.limits.max_sends: 12 of 40 sends used; 28 remaining
  loop.limits.no_progress_timeout: unset
  loop.limits.max_new_threads: 0 of 3 BB thread creations for handoffs used; 3 remaining
  loop.errors.max_reprompts: 0 of 3 re-prompts in this error episode used; 3 remaining
  loop.progress.max_nudges: 0 of 3 nudges since progress or the last turn end used; 3 remaining
window:    no window configured
permission: not set by looptender
sandbox:   not set by looptender
lanes:     ` + laneNotSupervised + `
lanes:     ` + laneRetryOff + `
lanes:     ` + laneStop + `
commands:
  keepgoing context -c '/private/example-loop/looptender.yaml'
    show this context again; read-only
  keepgoing status -c '/private/example-loop/looptender.yaml'
    show the loop's recorded status; read-only
  keepgoing complete -c '/private/example-loop/looptender.yaml' --note 'Task completed and verified'
    declare the entire task completed and verified; the loop holds until a person resumes it
  keepgoing await-user -c '/private/example-loop/looptender.yaml' --note 'Human input required; see the conversation'
    ask for a person when all remaining work needs one; the loop holds until a person resumes it
authority:
  Supervision grants no permission.
  The provider's sandbox and permission mode are unchanged by supervision.
  Only a person clears a hold.
  looptender never merges, edits a repository or reads transcript text.
  Lanes are not supervised by implication.
`
	if got := Build(in).Text(); got != want {
		t.Fatalf("text:\n%s\nwant:\n%s", got, want)
	}
}
