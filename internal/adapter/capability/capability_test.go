package capability

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/bb"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/factory"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/process"
	"github.com/connorburgess/keepgoing-agents/internal/adapter/runner"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

// root is the repository root, three directories above this package.
const root = "../../.."

func sorted(m map[string]bool) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func parse(t *testing.T, rel string) *ast.File {
	t.Helper()
	f, err := parser.ParseFile(token.NewFileSet(), filepath.Join(root, rel), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	return f
}

// A new decision input must be listed here before any adapter can set it.
func TestEveryObservationFieldIsListed(t *testing.T) {
	fields := map[string]bool{}
	typ := reflect.TypeOf(decide.Observation{})
	for i := 0; i < typ.NumField(); i++ {
		fields[typ.Field(i).Name] = true
	}
	want := append([]string{}, ObservationFields...)
	sort.Strings(want)
	if got := sorted(fields); !reflect.DeepEqual(got, want) {
		t.Fatalf("decide.Observation has fields %v; capability.ObservationFields lists %v. Add the field, then add it to the Inputs of each adapter that sets it and update docs/adapters.md.", got, want)
	}
}

// The decision engine can only act on what an adapter reports. This reads
// each registered adapter's source and compares the Observation fields it
// refers to, as a selector or a composite-literal key, with its Inputs.
func TestAdapterInputsMatchTheirCode(t *testing.T) {
	known := map[string]bool{}
	for _, f := range ObservationFields {
		known[f] = true
	}
	for _, a := range Adapters {
		if !a.Registered {
			if len(a.Inputs) != 0 {
				t.Errorf("%s is not registered and cannot report inputs", a.ID)
			}
			continue
		}
		if spec, ok := runner.SpecFor(a.ID); ok {
			// On the runner contract one shared runner sets the fields for
			// every agent. Which of them an agent can produce follows from
			// its specification, not from which file mentions a field.
			want := append([]string{}, a.Inputs...)
			sort.Strings(want)
			if got := spec.ObservationInputs(); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: its specification can produce Observation fields %v; the capability table lists %v. Update Inputs, the matrix rows the new input serves, and docs/adapters.md.", a.ID, got, want)
			}
			continue
		}
		used := map[string]bool{}
		for _, file := range a.Files {
			ast.Inspect(parse(t, file), func(n ast.Node) bool {
				switch x := n.(type) {
				case *ast.SelectorExpr:
					if known[x.Sel.Name] {
						used[x.Sel.Name] = true
					}
				case *ast.KeyValueExpr:
					if id, ok := x.Key.(*ast.Ident); ok && known[id.Name] {
						used[id.Name] = true
					}
				}
				return true
			})
		}
		want := append([]string{}, a.Inputs...)
		sort.Strings(want)
		if got := sorted(used); !reflect.DeepEqual(got, want) {
			t.Errorf("%s: code refers to Observation fields %v; the capability table lists %v. Update Inputs, the matrix rows the new input serves, and docs/adapters.md.", a.ID, got, want)
		}
	}
}

// caseStrings collects the string literals of every case clause inside the
// named function.
func caseStrings(t *testing.T, rel, function string) []string {
	t.Helper()
	found := map[string]bool{}
	for _, decl := range parse(t, rel).Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != function {
			continue
		}
		ast.Inspect(fn, func(n ast.Node) bool {
			if c, ok := n.(*ast.CaseClause); ok {
				for _, e := range c.List {
					if lit, ok := e.(*ast.BasicLit); ok && lit.Kind == token.STRING {
						s, _ := strconv.Unquote(lit.Value)
						found[s] = true
					}
				}
			}
			return true
		})
	}
	if len(found) == 0 {
		t.Fatalf("%s: no case strings found in %s; the check needs updating", rel, function)
	}
	return sorted(found)
}

func TestRegisteredAdaptersMatchTheFactory(t *testing.T) {
	want := map[string]bool{}
	for _, a := range Adapters {
		if a.Registered {
			want[a.ID] = true
		}
	}
	// The factory names some adapters and takes the rest from the runner
	// contract's registry.
	built := map[string]bool{}
	for _, name := range caseStrings(t, "internal/adapter/factory/factory.go", "New") {
		built[name] = true
	}
	for _, spec := range runner.Specs() {
		built[spec.Name] = true
	}
	if got := sorted(built); !reflect.DeepEqual(got, sorted(want)) {
		t.Fatalf("the factory builds %v; the capability table registers %v. A new adapter needs a column in every row.", got, sorted(want))
	}
	if _, err := factory.New(config.Agent{Adapter: "antigravity"}, t.TempDir(), "primary"); err == nil {
		t.Fatal("antigravity is listed as not registered but the factory built it")
	}
}

func TestSessionEvidenceMatchesTheCheck(t *testing.T) {
	want := map[string]bool{}
	for _, a := range Adapters {
		if a.SessionEvidence {
			want[a.ID] = true
		}
	}
	// The check names BB and asks the runner package which other adapters
	// keep a saved session.
	recorded := map[string]bool{}
	for _, name := range caseStrings(t, "internal/supervise/graph.go", "graphSessionOf") {
		recorded[name] = true
	}
	for _, a := range Adapters {
		if runner.KeepsSessionFile(a.ID) {
			recorded[a.ID] = true
		}
	}
	if got := sorted(recorded); !reflect.DeepEqual(got, sorted(want)) {
		t.Fatalf("a check records a session for %v; the capability table says %v", got, sorted(want))
	}
	for _, a := range Adapters {
		c := byID("run_graph").Cells[a.ID]
		if a.SessionEvidence != (c.Status == Yes) {
			t.Errorf("%s: run_graph is %s but SessionEvidence is %v", a.ID, c.Status, a.SessionEvidence)
		}
	}
}

func byID(id string) Behaviour {
	for _, row := range Matrix {
		if row.ID == id {
			return row
		}
	}
	return Behaviour{}
}

func TestOptionalInterfacesMatchTheAdapters(t *testing.T) {
	dir := t.TempDir()
	built := map[string]adapter.Adapter{
		"bb":      bb.New(config.BB{Thread: "thr_example"}),
		"claude":  runner.NewClaude(config.Claude{CLI: "absent"}, dir),
		"codex":   runner.NewCodex(config.Codex{CLI: "absent"}, dir),
		"cursor":  runner.NewCursor(config.Cursor{CLI: "absent"}, dir),
		"process": process.New(config.Process{}),
	}
	for _, a := range Adapters {
		impl, ok := built[a.ID]
		if !ok {
			if a.Registered {
				t.Errorf("%s is registered but not built by this test", a.ID)
			}
			continue
		}
		if impl.Name() != a.ID {
			t.Errorf("%s reports its name as %q", a.ID, impl.Name())
		}
		_, activates := impl.(adapter.Activator)
		_, polls := impl.(adapter.WakePoller)
		if activates != a.Activator || polls != a.WakePoller {
			t.Errorf("%s: Activator=%v WakePoller=%v; the capability table says %v and %v", a.ID, activates, polls, a.Activator, a.WakePoller)
		}
		// The turn_retry row must agree with what the adapter can actually do.
		_, retries := impl.(adapter.TurnRetrier)
		if listed := byID("turn_retry").Cells[a.ID].Status == Yes; retries != a.TurnRetrier || retries != listed {
			t.Errorf("%s: TurnRetrier=%v; the capability table says %v and the turn_retry row says %v", a.ID, retries, a.TurnRetrier, listed)
		}
		// And the child_recovery row with whether it can act on a lane at all.
		_, lanes := impl.(adapter.LaneRecoverer)
		if listed := byID("child_recovery").Cells[a.ID].Status == Yes; lanes != a.LaneRecoverer || lanes != listed {
			t.Errorf("%s: LaneRecoverer=%v; the capability table says %v and the child_recovery row says %v", a.ID, lanes, a.LaneRecoverer, listed)
		}
	}
	// A process loop can stop only through a configured command.
	if _, unsupported := process.New(config.Process{}).Stop(t.Context()).(adapter.ErrUnsupported); !unsupported || byID("stop").Cells["process"].Status != Partial {
		t.Error("process stop without a stop command should be unsupported and listed as partial")
	}
}

// Every cell is filled in, says why when it does not apply, and points at a
// test or document that exists.
func TestCellsAreCompleteAndTheirReferencesExist(t *testing.T) {
	seen := map[string]bool{}
	for _, row := range Matrix {
		if seen[row.ID] || row.Title == "" || row.What == "" {
			t.Errorf("row %q is duplicated or incomplete", row.ID)
		}
		seen[row.ID] = true
		if len(row.Cells) != len(Adapters) {
			t.Errorf("%s has %d cells for %d adapters", row.ID, len(row.Cells), len(Adapters))
		}
		for _, a := range Adapters {
			c, ok := row.Cells[a.ID]
			where := row.ID + "/" + a.ID
			if !ok || c.Note == "" {
				t.Errorf("%s: missing cell or note", where)
				continue
			}
			switch c.Status {
			case Yes, Partial:
				if c.Evidence == None || len(c.Refs) == 0 {
					t.Errorf("%s: an implemented behaviour needs evidence and a reference", where)
				}
				if !a.Registered {
					t.Errorf("%s: an unregistered adapter cannot implement a behaviour", where)
				}
			case NA:
				if c.Evidence != None {
					t.Errorf("%s: a behaviour that does not apply has no evidence level", where)
				}
			case Missing:
			default:
				t.Errorf("%s: unknown status %q", where, c.Status)
			}
			if c.Evidence != Live && c.Evidence != Fixture && c.Evidence != None {
				t.Errorf("%s: unknown evidence %q", where, c.Evidence)
			}
			for _, r := range c.Refs {
				path, _, _ := strings.Cut(r.Path, "#")
				body, err := os.ReadFile(filepath.Join(root, path))
				if err != nil {
					t.Errorf("%s: reference %s does not exist", where, r.Path)
					continue
				}
				if r.Test != "" && !regexp.MustCompile(`(?m)^func `+regexp.QuoteMeta(r.Test)+`\(`).Match(body) {
					t.Errorf("%s: %s has no test named %s", where, path, r.Test)
				}
			}
		}
	}
}

// docs/adapters.md carries the matrix between two markers. It must be exactly
// what the table renders.
func TestDocumentMatchesTheTable(t *testing.T) {
	const begin, end = "<!-- capability-matrix:begin -->\n", "<!-- capability-matrix:end -->"
	body, err := os.ReadFile(filepath.Join(root, "docs/adapters.md"))
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(body), "\r\n", "\n")
	i, j := strings.Index(text, begin), strings.Index(text, end)
	if i < 0 || j < i {
		t.Fatal("docs/adapters.md has no capability-matrix markers")
	}
	if got, want := text[i+len(begin):j], Render(); got != want {
		if os.Getenv("KEEPGOING_UPDATE_ADAPTERS_DOC") == "1" {
			if err := os.WriteFile(filepath.Join(root, "docs/adapters.md"), []byte(text[:i+len(begin)]+want+text[j:]), 0o644); err != nil {
				t.Fatal(err)
			}
			return
		}
		t.Fatal("docs/adapters.md does not match internal/adapter/capability. Regenerate it with: KEEPGOING_UPDATE_ADAPTERS_DOC=1 go test ./internal/adapter/capability -run TestDocumentMatchesTheTable")
	}
}

// The shared runner refers to exactly the Observation fields a specification
// can declare. A field added to it must be added to Spec.ObservationInputs,
// so that it reaches the table for the agents that can produce it.
func TestSharedRunnerInputsAreAllDeclarable(t *testing.T) {
	known := map[string]bool{}
	for _, f := range ObservationFields {
		known[f] = true
	}
	used := map[string]bool{}
	ast.Inspect(parse(t, "internal/adapter/runner/spec.go"), func(n ast.Node) bool {
		switch x := n.(type) {
		case *ast.SelectorExpr:
			if known[x.Sel.Name] {
				used[x.Sel.Name] = true
			}
		case *ast.KeyValueExpr:
			if id, ok := x.Key.(*ast.Ident); ok && known[id.Name] {
				used[id.Name] = true
			}
		}
		return true
	})
	if got, want := sorted(used), []string{"AwaitingInput", "ErrorClass", "Evidence", "ResetsAt", "State"}; !reflect.DeepEqual(got, want) {
		t.Fatalf("the shared runner refers to Observation fields %v, want %v", got, want)
	}
}

// turns are finished turns for each agent on the runner contract: a healthy
// one, a failure with a structured code, and a failure with nothing
// structured. With the never-run and unreadable cases they are every kind of
// output its reader distinguishes.
var turns = map[string][]struct {
	out  string
	exit string
}{
	"claude": {
		{`{"type":"result","subtype":"success","is_error":false,"session_id":"00000000-0000-4000-8000-0000000000aa"}`, "0"},
		{`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429,"terminal_reason":"rate_limit"}`, "1"},
		{`{"type":"result","subtype":"error_during_execution","is_error":true}`, "1"},
		{`fictional text`, "1"},
	},
	"codex": {
		{`{"type":"thread.started","thread_id":"00000000-0000-4000-8000-0000000000aa"}` + "\n" + `{"type":"turn.completed"}`, "0"},
		{`{"type":"turn.failed","error":{"message":"x","code":"usage_limit_reached","status":429}}`, "1"},
		{`{"type":"turn.failed","error":{"message":"x"}}`, "1"},
		{`fictional text`, "1"},
	},
}

// An agent's Inputs are what its reader actually sets. Each ported agent's
// real runner observes its turns, with the account-limit lookup answering
// "blocked, with a reset time" for any agent that asks, and the Observation
// fields that came back non-zero are compared with the table. So an agent
// that starts producing a field (a reader that reports waiting, a limit
// lookup added to a specification) fails here, and so does a table entry
// the agent cannot produce.
func TestPortedAgentsSetExactlyTheirInputs(t *testing.T) {
	lookup := runner.ReadCodexLimits
	defer func() { runner.ReadCodexLimits = lookup }()
	runner.ReadCodexLimits = func(context.Context, string, []string) (runner.CodexLimits, error) {
		return runner.CodexLimits{Blocked: true, ResetsAt: time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)}, nil
	}
	settings := map[string]config.Agent{
		"claude": {Adapter: "claude", Claude: &config.Claude{CLI: "absent"}},
		"codex":  {Adapter: "codex", Codex: &config.Codex{CLI: "absent"}},
	}
	for _, spec := range runner.Specs() {
		var listed []string
		for _, a := range Adapters {
			if a.ID == spec.Name {
				listed = append(listed, a.Inputs...)
			}
		}
		sort.Strings(listed)
		samples, ok := turns[spec.Name]
		if !ok || len(listed) == 0 {
			t.Errorf("%s is on the runner contract: give it turns here and a column in the table", spec.Name)
			continue
		}
		set := map[string]bool{}
		observe := func(dir string) {
			t.Helper()
			h, ok := runner.New(settings[spec.Name], dir)
			if !ok {
				t.Fatalf("%s: no runner", spec.Name)
			}
			obs, err := h.Observe(t.Context())
			if err != nil {
				t.Fatalf("%s: %v", spec.Name, err)
			}
			v := reflect.ValueOf(obs)
			for i := 0; i < v.NumField(); i++ {
				if !v.Field(i).IsZero() {
					set[v.Type().Field(i).Name] = true
				}
			}
		}
		observe(t.TempDir()) // never run
		for _, turn := range samples {
			dir := t.TempDir()
			for name, content := range map[string]string{"run.json": `{"pid":1,"startedAt":"2026-01-01T00:00:00Z","argv0":"absent"}`, "exit": turn.exit + "\n", "stdout": turn.out} {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			observe(dir)
		}
		if got := sorted(set); !reflect.DeepEqual(got, listed) {
			t.Errorf("%s: its runner set Observation fields %v; the capability table lists %v", spec.Name, got, listed)
		}
		// An agent has child work exactly when the matrix gives it child rows.
		for _, row := range []string{"child_awareness", "child_notice"} {
			if has := byID(row).Cells[spec.Name].Status != NA; has != spec.Capabilities.ChildWork {
				t.Errorf("%s: ChildWork=%v but the %s row is %s", spec.Name, spec.Capabilities.ChildWork, row, byID(row).Cells[spec.Name].Status)
			}
		}
		// It can be stopped exactly when the matrix says so.
		if listed := byID("stop").Cells[spec.Name].Status == Yes; listed != spec.Capabilities.Stoppable {
			t.Errorf("%s: Stoppable=%v but the stop row is %s", spec.Name, spec.Capabilities.Stoppable, byID("stop").Cells[spec.Name].Status)
		}
		// Exact resume and structured errors likewise.
		if listed := byID("session_resume").Cells[spec.Name].Status == Yes; listed != spec.Capabilities.ExactResume {
			t.Errorf("%s: ExactResume=%v but the session_resume row is %s", spec.Name, spec.Capabilities.ExactResume, byID("session_resume").Cells[spec.Name].Status)
		}
		if listed := byID("error_classes").Cells[spec.Name].Status == Yes; listed != spec.Capabilities.StructuredErrors {
			t.Errorf("%s: StructuredErrors=%v but the error_classes row is %s", spec.Name, spec.Capabilities.StructuredErrors, byID("error_classes").Cells[spec.Name].Status)
		}
		// Waiting on a person is structured exactly when permissions are.
		if listed := byID("permissions").Cells[spec.Name].Status == Yes; listed != spec.Capabilities.StructuredWaiting {
			t.Errorf("%s: StructuredWaiting=%v but the permissions row is %s", spec.Name, spec.Capabilities.StructuredWaiting, byID("permissions").Cells[spec.Name].Status)
		}
	}
}
