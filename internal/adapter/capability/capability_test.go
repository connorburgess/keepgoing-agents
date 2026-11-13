package capability

import (
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
	if got := caseStrings(t, "internal/adapter/factory/factory.go", "New"); !reflect.DeepEqual(got, sorted(want)) {
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
	if got := caseStrings(t, "internal/supervise/graph.go", "graphSession"); !reflect.DeepEqual(got, sorted(want)) {
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
