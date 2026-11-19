package main

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func TestAccountQuotaSandboxCLIWebAndNativeProjection(t *testing.T) {
	// Isolate every platform's default catalog, so the actual panel-list command
	// cannot consult personal state while exercising its default-directory path.
	home := t.TempDir()
	for _, key := range []string{"HOME", "USERPROFILE", "APPDATA", "LOCALAPPDATA", "XDG_CONFIG_HOME", "XDG_STATE_HOME"} {
		t.Setenv(key, home)
	}
	t.Setenv("CODEX_SANDBOX", "example-restricted")
	dir, e := accountFixture(t, "codex")
	defaultDir, err := accountsDirectory("")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(defaultDir), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(dir, defaultDir); err != nil {
		t.Fatal(err)
	}
	dir = defaultDir
	reader, _ := dockFixtureReader(t)
	reader.accountsDir = dir
	at := time.Now().UTC().Add(-time.Second)
	reader.now = func() time.Time { return at.Add(time.Second) }
	const response = `{"rateLimits":{"limitId":"codex","secondary":{"usedPercent":64,"windowDurationMins":10080,"resetsAt":1900500000}}}`
	calls := 0
	run := func(_ context.Context, _ string, env []string, _ string) ([]byte, error) {
		calls++
		if !strings.Contains(strings.Join(env, "\n"), "CODEX_SANDBOX=example-restricted") {
			t.Fatal("execution sandbox was stripped")
		}
		return []byte(response), nil
	}
	v, err := accountQuotaRefresh(context.Background(), dir, e.ID, nil, run, os.Environ(), func() time.Time { return at })
	if err != nil || v.State != "reported" {
		t.Fatalf("CLI refresh: %+v %v", v, err)
	}
	catalogBefore, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	quotaBefore, _ := os.ReadFile(filepath.Join(dir, accounts.QuotaName))
	for _, sandbox := range []string{"example-restricted", "example-unrestricted"} {
		t.Setenv("CODEX_SANDBOX", sandbox)
		var output bytes.Buffer
		status := panelListCommand(env{ctx: context.Background(), stdout: &output, stderr: io.Discard}, []string{"--json", "--offline", "--directory", reader.directory})
		var cliList panelList
		if status != exitOK || json.Unmarshal(output.Bytes(), &cliList) != nil || cliList.AccountQuotas == nil || len(cliList.AccountQuotas.Accounts) != 1 || cliList.AccountQuotas.Accounts[0].Summary.State != "reported" {
			t.Fatalf("actual panel list lost saved quota: %s", output.Bytes())
		}
		r := reader.read(context.Background(), false)
		native := buildPanelList(r)
		if native.AccountQuotas == nil || len(native.AccountQuotas.Accounts) != 1 {
			t.Fatal("native quota absent")
		}
		ns := native.AccountQuotas.Accounts[0].Summary
		_, web, err := dockSplitAccounts(r, strings.Repeat("a", 32))
		if err != nil {
			t.Fatal(err)
		}
		var saved dockAccountSnapshot
		if err := json.Unmarshal(web, &saved); err != nil {
			t.Fatal(err)
		}
		if len(saved.Accounts.Cards) != 1 || saved.Accounts.Cards[0].Quota == nil {
			t.Fatal("browser quota absent")
		}
		ws := saved.Accounts.Cards[0].Quota.Summary
		if ns.State != "reported" || ws.State != "reported" || len(ns.Rows) != 2 || ns.Rows[0].UsedPercent == nil || *ns.Rows[0].UsedPercent != 64 || ws.Rows[0].UsedPercent == nil || *ws.Rows[0].UsedPercent != 64 {
			t.Fatalf("web/native diverged: %+v %+v", ns, ws)
		}
		n, _ := json.Marshal(ns)
		w, _ := json.Marshal(ws)
		if !bytes.Equal(n, w) {
			t.Fatal("web/native did not use the same summary")
		}
		if out := os.Getenv("LOOPTENDER_SANDBOX_QUOTA_FIXTURE"); out != "" {
			nb, _ := json.Marshal(native)
			rb, _ := json.Marshal(r)
			if err := os.WriteFile(filepath.Join(out, "panel.json"), nb, 0600); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "report.json"), rb, 0600); err != nil {
				t.Fatal(err)
			}
		}
	}
	if calls != 1 {
		t.Fatal("projection refreshed provider")
	}
	catalogAfter, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	quotaAfter, _ := os.ReadFile(filepath.Join(dir, accounts.QuotaName))
	if !bytes.Equal(catalogBefore, catalogAfter) || !bytes.Equal(quotaBefore, quotaAfter) {
		t.Fatal("projection rewrote private evidence")
	}
	// A genuine key change still withholds the same saved reading everywhere.
	t.Setenv("OPENAI_API_KEY", "example-different-authentication")
	r := reader.read(context.Background(), false)
	q := r.Accounts.Cards[0].Quota
	n := buildPanelList(r).AccountQuotas.Accounts[0].Summary
	if q.State != "unavailable" || q.Reason != "selection_review_required" || n.State != "unavailable" || len(n.Rows) != 0 || !strings.Contains(n.Text, "Open Accounts") {
		t.Fatalf("real drift lost: %+v %+v", q, n)
	}
	if out := os.Getenv("LOOPTENDER_SANDBOX_QUOTA_FIXTURE"); out != "" {
		b, _ := json.Marshal(r)
		if err := os.WriteFile(filepath.Join(out, "recovery.json"), b, 0600); err != nil {
			t.Fatal(err)
		}
	}
}
