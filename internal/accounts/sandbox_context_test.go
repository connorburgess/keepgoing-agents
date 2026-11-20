package accounts

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A pinned definition-2 fixture, independent of Context and its current tag.
// Its sole environment input is the fictional HOME; the catalog salt is public fixture data.
func definition2Context(f File, e Entry) string {
	key, _ := hex.DecodeString(f.Salt)
	h := hmac.New(sha256.New, key)
	h.Write([]byte("looptender accounts context 2\n"))
	enc := json.NewEncoder(h)
	enc.Encode([]string{e.Provider, e.Mode, e.CLI, e.Directory, e.Workdir})
	enc.Encode([]string{"HOME", "/example"})
	sum := h.Sum(nil)[:24]
	tag := hmac.New(sha256.New, key)
	tag.Write([]byte("looptender accounts context 2 tag\n"))
	tag.Write(sum)
	return hex.EncodeToString(append(sum, tag.Sum(nil)[:8]...))
}

func TestContextSandboxDoesNotSelectAnAccount(t *testing.T) {
	f := File{Salt: strings.Repeat("ab", 16)}
	e := Entry{Provider: "codex", Mode: "inherited", CLI: "/example/bin/codex", Workdir: "/example"}
	for _, windows := range []bool{false, true} {
		base := contextForOS(f, e, []string{"HOME=/example"}, windows)
		for _, key := range []string{"CODEX_SANDBOX", "CoDeX_SaNdBoX"} {
			if !windows && key != "CODEX_SANDBOX" {
				continue
			}
			for _, value := range []string{"example-restricted", "example-unrestricted"} {
				env := []string{"HOME=/example", key + "=" + value}
				if contextForOS(f, e, env, windows) != base {
					t.Errorf("execution mode changed account binding windows=%t", windows)
				}
				if got := executionEnvironmentForOS(e, env, windows); strings.Join(got, "\n") != strings.Join(env, "\n") {
					t.Fatal("execution environment changed")
				}
			}
		}
		for _, key := range []string{"CODEX_API_KEY", "CODEX_HOME", "CODEX_SESSION_ID", "CODEX_THREAD_ID", "CODEX_SANDBOX_EXTRA", "HTTPS_PROXY", "SSL_CERT_FILE", "HOME"} {
			if contextForOS(f, e, []string{key + "=example-a"}, windows) == contextForOS(f, e, []string{key + "=example-b"}, windows) {
				t.Errorf("auth/selection/other input lost: %s", key)
			}
		}
	}
}

func TestContextDefinition2RequiresExplicitSelectionReview(t *testing.T) {
	dir, e, env := quotaFixture(t)
	env = []string{"HOME=/example"}
	f, _ := Load(dir)
	old := definition2Context(f, e)
	if !taggedDefinition(f, old, contextTagDefinition) || !protectedDefinition(f, old) || old == Context(f, e, env) {
		t.Fatal("valid protected v2 binding not distinguished from v3")
	}
	if err := Update(dir, func(f *File) error { f.Entries[0].Context = old; return nil }); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(dir)
	e = f.Entries[0]
	before, _ := os.ReadFile(filepath.Join(dir, FileName))
	if _, ok := Bound(f, e, env); ok {
		t.Fatal("protected definition2 silently accepted")
	}
	a := QuotaAttempt{At: time.Now().UTC(), Windows: []QuotaWindow{{Name: "secondary", UsedPercent: 64}}}
	if err := quotaPublish(t, dir, e, env, a); err == nil {
		t.Fatal("old catalog published quota")
	}
	v := QuotaViews(dir, f, env, a.At)[e.ID]
	if v.State != "unavailable" || v.Reason != "selection_review_required" {
		t.Fatalf("no precise selection recovery: %+v", v)
	}
	after, _ := os.ReadFile(filepath.Join(dir, FileName))
	if string(before) != string(after) {
		t.Fatal("read/publication changed catalog")
	}
	// The installed v2 reader recognizes the same guard tag on a v3 binding.
	// Its exact v2 recomputation differs, so its existing Bound refuses rollback reuse.
	current := Context(f, e, env)
	if !taggedDefinition(f, current, "looptender accounts context 2") || current == old {
		t.Fatal("v2 reader would silently accept v3 as pre-tag")
	}
	// A malformed keyed tag stays on the existing pre-tag path; it is never
	// considered a valid protected definition2 binding or accepted cache evidence.
	b, _ := hex.DecodeString(old)
	b[len(b)-1] ^= 1
	e.Context = hex.EncodeToString(b)
	if taggedDefinition(f, e.Context, contextTagDefinition) || protectedDefinition(f, e.Context) {
		t.Fatal("invalid tag recognized")
	}
	if _, ok := Bound(f, e, env); !ok {
		t.Fatal("pre-tag compatibility changed")
	}
	if err := Update(dir, func(f *File) error {
		f.Entries[0] = Revise(*f, f.Entries[0], f.Entries[0], "thr_example_owner", a.At, env)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	f, _ = Load(dir)
	updated := f.Entries[0]
	if updated.Revision == e.Revision || !protectedDefinition(f, updated.Context) {
		t.Fatal("explicit update did not create new v3 selection")
	}
	if err := quotaPublish(t, dir, updated, env, a); err != nil {
		t.Fatal(err)
	}
	if got := QuotaViews(dir, f, env, a.At)[updated.ID]; got.State != "reported" || got.Windows[0].UsedPercent != 64 {
		t.Fatal("explicit recovery failed")
	}

}

func TestQuotaSandboxProjectionAndOldCacheRefusal(t *testing.T) {
	dir, e, env := quotaFixture(t)
	now := time.Now().UTC()
	a := QuotaAttempt{At: now, Windows: []QuotaWindow{{Name: "secondary", UsedPercent: 64}}}
	if err := quotaPublish(t, dir, e, append(env, "CODEX_SANDBOX=example-restricted"), a); err != nil {
		t.Fatal(err)
	}
	f, _ := Load(dir)
	for _, mode := range []string{"example-restricted", "example-unrestricted"} {
		v := QuotaViews(dir, f, append(env, "CODEX_SANDBOX="+mode), now)[e.ID]
		if v.State != "reported" || len(v.Windows) != 1 || v.Windows[0].UsedPercent != 64 {
			t.Fatalf("saved reading disappeared: %+v", v)
		}
	}
	q, _ := LoadQuota(dir)
	q.Entries[0].Context = definition2Context(f, e)
	raw, _ := json.Marshal(q)
	os.WriteFile(filepath.Join(dir, QuotaName), raw, 0600)
	v := QuotaViews(dir, f, env, now)[e.ID]
	if v.State != "unavailable" || v.Reason != "context_changed" || len(v.Windows) != 0 {
		t.Fatal("old cache borrowed")
	}
	after, _ := os.ReadFile(filepath.Join(dir, QuotaName))
	if string(raw) != string(after) {
		t.Fatal("read migrated cache")
	}
}
