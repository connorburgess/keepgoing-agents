package accounts

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func fixture(t *testing.T) (string, Entry) {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "catalog")
	var out Entry
	err := Update(dir, func(f *File) error {
		out = NewEntry(*f, Entry{Label: "Example studio", Provider: "claude-code", Mode: "inherited", CLI: filepath.Join(dir, "cli"), Workdir: dir}, "thr_example_owner", time.Now().UTC(), []string{"HOME=/example", "ANTHROPIC_API_KEY=fictional-sensitive-value"})
		f.Entries = append(f.Entries, out)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return dir, out
}
func TestCatalogBoundsCorruptionAndPrivacy(t *testing.T) {
	for _, kind := range []string{"corrupt", "unknown", "oversize", "duplicate", "revision", "private_version", "permissions", "symlink", "lock_permissions", "directory_permissions"} {
		t.Run(kind, func(t *testing.T) {
			dir, e := fixture(t)
			p := filepath.Join(dir, FileName)
			raw, _ := os.ReadFile(p)
			switch kind {
			case "corrupt":
				_ = os.WriteFile(p, []byte("{"), 0600)
			case "unknown":
				_ = os.WriteFile(p, []byte(strings.Replace(string(raw), "{", "{\"unknown\":true,", 1)), 0600)
			case "oversize":
				_ = os.WriteFile(p, make([]byte, MaxBytes+1), 0600)
			case "duplicate":
				f, _ := Load(dir)
				f.Entries = append(f.Entries, e)
				b, _ := json.Marshal(f)
				_ = os.WriteFile(p, b, 0600)
			case "revision":
				f, _ := Load(dir)
				f.Entries[0].Attempt = &Observation{Revision: "different", Context: e.Context, At: time.Now(), Auth: "reported_present"}
				b, _ := json.Marshal(f)
				_ = os.WriteFile(p, b, 0600)
			case "private_version":
				f, _ := Load(dir)
				f.Entries[0].Attempt = &Observation{Revision: e.Revision, Context: e.Context, At: time.Now(), Auth: "reported_present", Version: "/private/example/sensitive"}
				b, _ := json.Marshal(f)
				_ = os.WriteFile(p, b, 0600)
			case "permissions":
				if runtime.GOOS == "windows" {
					t.Skip("Unix mode guard")
				}
				_ = os.Chmod(p, 0644)
			case "symlink":
				target := filepath.Join(dir, "other")
				_ = os.Rename(p, target)
				if os.Symlink(target, p) != nil {
					t.Skip("symlink unavailable")
				}
			case "lock_permissions":
				if runtime.GOOS == "windows" {
					t.Skip("Unix mode guard")
				}
				_ = os.Chmod(filepath.Join(dir, "accounts.lock"), 0644)
			case "directory_permissions":
				if runtime.GOOS == "windows" {
					t.Skip("Unix mode guard")
				}
				_ = os.Chmod(dir, 0755)
			}
			before, _ := os.ReadFile(p)
			if kind != "lock_permissions" {
				if _, err := Load(dir); err == nil {
					t.Fatal("unsafe catalog read")
				}
			}
			if err := Update(dir, func(*File) error { return nil }); err == nil {
				t.Fatal("unsafe catalog rewritten")
			}
			after, _ := os.ReadFile(p)
			if string(before) != string(after) {
				t.Fatal("corrupt record repaired")
			}
		})
	}
}
func TestCatalogLimitsAndAtomicSnapshots(t *testing.T) {
	dir, e := fixture(t)
	for i := 1; i < MaxEntries; i++ {
		if err := Update(dir, func(f *File) error { f.Entries = append(f.Entries, NewEntry(*f, e, "", time.Now(), nil)); return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := Update(dir, func(f *File) error { f.Entries = append(f.Entries, NewEntry(*f, e, "", time.Now(), nil)); return nil }); err == nil {
		t.Fatal("entry limit")
	}
	// Failed writes preserve the previous complete record.
	f, err := Load(dir)
	if err != nil || len(f.Entries) != MaxEntries {
		t.Fatalf("lost entries: %v", err)
	}
	if err := Update(dir, func(f *File) error { f.Entries[0].Label = strings.Repeat("a", 121); return nil }); err == nil {
		t.Fatal("label limit")
	}
	if _, err = Load(dir); err != nil {
		t.Fatal("failed write corrupted snapshot")
	}
}
func TestContextFingerprintAndProjection(t *testing.T) {
	dir, e := fixture(t)
	f, _ := Load(dir)
	a := Context(f, e, []string{"HOME=/example", "ANTHROPIC_API_KEY=a", "BB_THREAD_ID=one"})
	if a != Context(f, e, []string{"BB_THREAD_ID=two", "ANTHROPIC_API_KEY=a", "HOME=/example"}) {
		t.Fatal("caller changes auth context")
	}
	if a == Context(f, e, []string{"HOME=/example", "ANTHROPIC_API_KEY=b"}) {
		t.Fatal("secret input not bound")
	}
	explicit := e
	explicit.Mode = "explicit_directory"
	explicit.Directory = "/selected"
	if Context(f, explicit, []string{"CLAUDE_CONFIG_DIR=/ignored-a"}) != Context(f, explicit, []string{"CLAUDE_CONFIG_DIR=/ignored-b"}) {
		t.Fatal("effective override not honored")
	}
	now := time.Now().UTC()
	o := Observation{Revision: e.Revision, Context: e.Context, At: now.Add(-time.Minute), Auth: "reported_present", Plan: "max", Caller: "thr_example_owner"}
	f.Entries[0].Last = &o
	f.Entries[0].Attempt = &o
	v := Project(f, now)
	if v.Cards[0].Auth.Freshness != "fresh" || v.Cards[0].Plan.Text != "Max" {
		t.Fatal("fresh evidence lost")
	}
	if Project(f, now.Add(5*time.Minute)).Cards[0].Auth.Freshness != "stale" {
		t.Fatal("age promoted")
	}
	failure := o
	failure.At = now
	failure.Auth = "unavailable"
	failure.Plan = ""
	failure.Reason = "timeout"
	f.Entries[0].Attempt = &failure
	v = Project(f, now)
	if c := v.Cards[0]; c.Auth.State != "reported_present" || !c.Auth.ObservedAt.Equal(o.At) || c.Auth.Freshness != "stale" || c.Plan.Text != "Max" || c.Plan.Freshness != "stale" || c.FailedCheckAt == nil || !c.FailedCheckAt.Equal(now) || c.Reason != "timeout" {
		t.Fatal("failure hid or refreshed prior observation")
	}
	raw, _ := json.Marshal(v)
	for _, private := range []string{"fictional-sensitive-value", "thr_example_owner", e.CLI, e.Context, e.Revision} {
		if strings.Contains(string(raw), private) {
			t.Fatal("private field projected")
		}
	}
	if !ReadView(dir, now).Complete {
		t.Fatal("read failure")
	}
	bad := ReadView(filepath.Join(dir, "accounts.lock"), now)
	if bad.Complete || bad.State != "unavailable" {
		t.Fatal("unavailable became empty")
	}
	if err := Update(dir, func(*File) error { return ErrChanged }); !errors.Is(err, ErrChanged) {
		t.Fatal("revision refusal lost")
	}
}

func TestAtomicCatalogReadersNeverSeePartialWrite(t *testing.T) {
	dir, _ := fixture(t)
	done := make(chan error, 1)
	go func() {
		for i := 0; i < 40; i++ {
			if _, err := Load(dir); err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	for i := 0; i < 20; i++ {
		if err := Update(dir, func(f *File) error { f.Entries[0].Label = "Example alternate"; return nil }); err != nil {
			t.Fatal(err)
		}
	}
	if err := <-done; err != nil {
		t.Fatal("partial record observed", err)
	}
}

func TestContextEffectiveEnvironmentSemantics(t *testing.T) {
	_, e := fixture(t)
	f := File{Salt: strings.Repeat("ab", 16)}
	for _, key := range []string{"http_proxy", "https_proxy", "all_proxy", "no_proxy"} {
		a := contextForOS(f, e, []string{key + "=example-a"}, false)
		b := contextForOS(f, e, []string{key + "=example-b"}, false)
		if a == b {
			t.Fatalf("Unix lowercase %s omitted", key)
		}
	}
	if contextForOS(f, e, []string{"ANTHROPIC_API_KEY=a", "ANTHROPIC_API_KEY=b"}, false) != contextForOS(f, e, []string{"ANTHROPIC_API_KEY=b"}, false) {
		t.Fatal("Unix duplicate last-wins mismatch")
	}
	if contextForOS(f, e, []string{"ANTHROPIC_API_KEY=a", "anthropic_api_key=b"}, false) == contextForOS(f, e, []string{"ANTHROPIC_API_KEY=b"}, false) {
		t.Fatal("Unix names incorrectly folded")
	}
	if contextForOS(f, e, []string{"Anthropic_Api_Key=a", "ANTHROPIC_API_KEY=b", "Https_Proxy=example"}, true) != contextForOS(f, e, []string{"anthropic_api_key=b", "HTTPS_PROXY=example"}, true) {
		t.Fatal("Windows case folding/last-wins mismatch")
	}
	if contextForOS(f, e, []string{"https_proxy=example-a"}, true) == contextForOS(f, e, []string{"HTTPS_PROXY=example-b"}, true) {
		t.Fatal("Windows lowercase proxy omitted")
	}
	for _, provider := range []string{"claude-code", "codex"} {
		explicit := e
		explicit.Provider = provider
		explicit.Mode = "explicit_directory"
		explicit.Directory = filepath.Join(t.TempDir(), "selected")
		key := "CLAUDE_CONFIG_DIR"
		if provider == "codex" {
			key = "CODEX_HOME"
		}
		for _, windows := range []bool{false, true} {
			env := []string{strings.ToLower(key) + "=lower", key + "=old", strings.ToLower(key) + "=last", "BB_THREAD_ID=thr_example_actual"}
			executed := executionEnvironmentForOS(explicit, env, windows)
			if contextForOS(f, explicit, env, windows) != contextForOS(f, explicit, executed, windows) {
				t.Fatal("fingerprint differs from actual explicit override")
			}
			equivalent := 0
			for _, pair := range executed {
				name, value, _ := strings.Cut(pair, "=")
				match := name == key
				if windows {
					match = strings.EqualFold(name, key)
				}
				if match {
					equivalent++
					if value != explicit.Directory {
						t.Fatal("old selected directory retained")
					}
				}
			}
			if equivalent != 1 {
				t.Fatal("duplicate override names")
			}
			// The caller is recorded privately, not passed to the provider.
			if strings.Contains(strings.Join(executed, "\n"), "BB_THREAD_ID") {
				t.Fatal("caller session passed to the status command")
			}
		}
	}
}
func TestCatalogGenuineAbsenceDoesNotWrite(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "never-created")
	f, err := Load(dir)
	if err != nil || len(f.Entries) != 0 {
		t.Fatal("genuine absence not empty")
	}
	if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("read created catalog")
	}
}

func TestContextToolCallIdentifierExclusion(t *testing.T) {
	_, e := fixture(t)
	f := File{Salt: strings.Repeat("ab", 16)}
	for _, windows := range []bool{false, true} {
		for _, name := range []string{"CODEX_TOOL_CALL_ID", "CoDeX_ToOl_CaLl_Id"} {
			if !windows && name != "CODEX_TOOL_CALL_ID" {
				continue
			}
			if contextForOS(f, e, []string{name + "=example-add"}, windows) != contextForOS(f, e, []string{name + "=example-check"}, windows) || contextForOS(f, e, nil, windows) != contextForOS(f, e, []string{name + "=example-check"}, windows) {
				t.Fatalf("tool invocation identifier bound, windows=%t", windows)
			}
		}
		for _, name := range []string{"CODEX_API_KEY", "CLAUDE_CODE_OAUTH_TOKEN", "ANTHROPIC_API_KEY", "CODEX_SESSION_ID", "CODEX_TOOL_CALL_ID_EXTRA"} {
			if contextForOS(f, e, []string{name + "=example-a"}, windows) == contextForOS(f, e, []string{name + "=example-b"}, windows) {
				t.Fatalf("other binding lost: %s windows=%t", name, windows)
			}
		}
	}
	// Unix's differently cased, still prefix-matching key is not the excluded name.
	if contextForOS(f, e, []string{"CODEX_tool_call_id=example-a"}, false) == contextForOS(f, e, []string{"CODEX_tool_call_id=example-b"}, false) {
		t.Fatal("Unix exclusion incorrectly case folded")
	}
}

func TestLoginProjectionNamesSelectedCLI(t *testing.T) {
	_, e := fixture(t)
	now := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	for _, tc := range []struct{ state, text string }{{"reported_present", "Selected CLI reports login"}, {"reported_absent", "Selected CLI reports no login"}} {
		t.Run(tc.state, func(t *testing.T) {
			o := Observation{Revision: e.Revision, Context: e.Context, At: now.Add(-time.Minute), Auth: tc.state, Version: "2.1.318"}
			selected := e
			selected.Attempt = &o
			selected.Last = &o
			f := File{Entries: []Entry{selected}}
			for _, age := range []time.Duration{0, 10 * time.Minute} {
				c := Project(f, now.Add(age)).Cards[0]
				wantFresh := "fresh"
				if age > 0 {
					wantFresh = "stale"
				}
				if c.Auth.Text != tc.text || c.Auth.State != tc.state || c.Auth.Source != "claude-code CLI 2.1.318 status" || !c.Auth.ObservedAt.Equal(o.At) || c.Auth.Freshness != wantFresh || c.Uses != "unavailable" {
					t.Fatal("selected CLI evidence or association changed")
				}
			}
		})
	}
}
