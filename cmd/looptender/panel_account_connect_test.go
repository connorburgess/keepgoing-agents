package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func connectFixture(t *testing.T) (*panelChanges, string, accounts.Entry) {
	t.Helper()
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("immutable executable admission unsupported on this platform")
	}
	dir, e := accountFixture(t, "codex")
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	c, err := newPanelChanges(ctx, "", &dockReader{accountsDir: dir}, time.Now(), time.Now)
	if err != nil {
		t.Fatal(err)
	}
	c.quotaEnv = []string{"HOME=" + e.Workdir, "BB_THREAD_ID=thr_example_owner"}
	c.caller = "thr_example_owner"
	c.connectAdmit = func(context.Context, accounts.Entry) (string, error) {
		raw, err := os.ReadFile(e.CLI)
		sum := sha256.Sum256(raw)
		return hex.EncodeToString(sum[:]), err
	}
	return c, dir, e
}
func connectStart(t *testing.T, c *panelChanges, e accounts.Entry, label string) *panelAccountConnectAttempt {
	t.Helper()
	p := c.prepareAccountConnectLabel("example-session", e.ID, label)
	if p.Preview == nil {
		t.Fatalf("prepare refused: %#v", p.Result)
	}
	result := c.startAccountConnect("example-session", e.ID, p.Preview.Form)
	if result.Connect == nil || result.Connect.ID != p.Preview.AttemptID {
		t.Fatal("missing stable reviewed attempt")
	}
	c.mu.Lock()
	a := c.connectAttempts[result.Connect.ID]
	c.mu.Unlock()
	return a
}
func connectWait(t *testing.T, a *panelAccountConnectAttempt) {
	t.Helper()
	select {
	case <-a.done:
	case <-time.After(3 * time.Second):
		t.Fatal("owned connection did not finish")
	}
}
func TestPanelAccountConnectIsolatedPublicationAndLostReply(t *testing.T) {
	for _, another := range []bool{false, true} {
		t.Run(map[bool]string{false: "same-row", true: "another-row"}[another], func(t *testing.T) {
			c, dir, e := connectFixture(t)
			before, _ := accounts.Load(dir)
			originalCLI := e.CLI
			started := make(chan accounts.Entry, 1)
			release := make(chan struct{})
			c.connectRun = func(ctx context.Context, selected accounts.Entry, env []string, progress func(string)) error {
				if selected.CLI == originalCLI || selected.Directory == "" || selected.Directory == e.Directory || selected.Workdir != e.Workdir {
					return errors.New("not isolated")
				}
				caller, home := false, false
				for _, v := range env {
					caller = caller || v == "BB_THREAD_ID=thr_example_owner"
					home = home || v == "CODEX_HOME="+selected.Directory
				}
				if !caller || !home {
					return errors.New("wrong launch environment")
				}
				started <- selected
				select {
				case <-release:
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			label := ""
			if another {
				label = "Example second"
			}
			a := connectStart(t, c, e, label)
			selected := <-started
			unchanged, _ := accounts.Load(dir)
			if !reflect.DeepEqual(before, unchanged) {
				t.Fatal("catalog written before provider success")
			}
			// A dropped start reply is recovered from the preview ID; status causes no provider call.
			for i := 0; i < 3; i++ {
				v := c.readAccountConnect("example-session", e.ID, a.view.ID, false)
				if v.Connect == nil || v.Connect.State != "waiting" {
					t.Fatal(v)
				}
			}
			if c.readAccountConnect("wrong-session", e.ID, a.view.ID, false).Connect != nil {
				t.Fatal("cross-session disclosure")
			}
			if p := c.prepareAccountConnect("example-session", e.ID); p.Preview == nil {
				t.Fatal("could not prepare second review")
			} else if c.startAccountConnect("example-session", e.ID, p.Preview.Form).Connect != nil {
				t.Fatal("concurrent selection admission")
			}
			close(release)
			connectWait(t, a)
			v := c.readAccountConnect("example-session", e.ID, a.view.ID, false).Connect
			if v.State != "connected" || !panelAccountID.MatchString(v.ConnectedAccountID) {
				t.Fatal(v)
			}
			after, err := accounts.Load(dir)
			if err != nil {
				t.Fatal(err)
			}
			target := accounts.Find(&after, v.ConnectedAccountID)
			if target == nil || target.CLI != originalCLI || target.Directory != selected.Directory || target.Mode != "explicit_directory" || target.Last != nil {
				t.Fatal("wrong published selection")
			}
			if another {
				if len(after.Entries) != 2 || !reflect.DeepEqual(*accounts.Find(&after, e.ID), e) {
					t.Fatal("template changed")
				}
			} else if len(after.Entries) != 1 || target.ID != e.ID || target.Revision == e.Revision {
				t.Fatal("same row publication")
			}
			if _, err := os.Stat(selected.CLI); !os.IsNotExist(err) {
				t.Fatal("staged image survives owned completion")
			}
		})
	}
}
func TestPanelAccountConnectFailureCancelAndDrift(t *testing.T) {
	for _, kind := range []string{"failure", "cancel", "server-close", "revision", "directory", "cli"} {
		t.Run(kind, func(t *testing.T) {
			c, dir, e := connectFixture(t)
			started := make(chan accounts.Entry, 1)
			release := make(chan struct{})
			c.connectRun = func(ctx context.Context, selected accounts.Entry, _ []string, _ func(string)) error {
				started <- selected
				select {
				case <-release:
					if kind == "failure" {
						return errConnectProtocol
					}
					return nil
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
			a := connectStart(t, c, e, "")
			selected := <-started
			switch kind {
			case "cancel":
				c.readAccountConnect("example-session", e.ID, a.view.ID, true)
			case "server-close":
				a.cancel()
			case "revision":
				if err := accounts.Update(dir, func(f *accounts.File) error {
					old := accounts.Find(f, e.ID)
					*old = accounts.Revise(*f, *old, *old, "thr_example_other", time.Now(), c.quotaEnv)
					return nil
				}); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Rename(selected.Directory, selected.Directory+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(selected.Directory, 0700); err != nil {
					t.Fatal(err)
				}
			case "cli":
				if err := os.Rename(e.CLI, e.CLI+"-old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(e.CLI, []byte("replacement"), 0700); err != nil {
					t.Fatal(err)
				}
			}
			close(release)
			connectWait(t, a)
			view := c.readAccountConnect("example-session", e.ID, a.view.ID, false).Connect
			if view.State == "connected" || view.AuthorizationURL != "" || view.ConnectedAccountID != "" {
				t.Fatal(view)
			}
			if kind != "revision" {
				after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
				if string(after) != string(before) {
					t.Fatal("failed attempt changed catalog")
				}
			}
		})
	}
}
func TestPanelAccountConnectStrictRequestsAndEnvironment(t *testing.T) {
	for _, body := range []string{`{"action":"connect_account","Action":"connect_account"}`, `{"action":"connect_account","action":"connect_account"}`, `{"action":"connect_account","token":"secret"}`, `null`, `{"action":null}`} {
		_, _, _, ok := panelReadConnect(strings.NewReader(body))
		if ok {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, key := range []string{"ANTHROPIC_API_KEY", "OPENAI_API_KEY", "OPENAI_IDENTITY_TOKEN_FILE", "CLAUDE_CODE_OAUTH_TOKEN", "CLAUDE_CODE_USE_BEDROCK", "NODE_OPTIONS", "CODEX_ACCESS_TOKEN"} {
		if accountConnectEnvironmentAllowed([]string{key + "=fictional"}) {
			t.Fatal(key)
		}
	}
	if !accountConnectEnvironmentAllowed([]string{"HOME=/example", "BB_THREAD_ID=thr_example_owner", "CODEX_HOME=/example/account"}) {
		t.Fatal("safe context refused")
	}
	c, _, e := connectFixture(t)
	for _, body := range []string{`{"action":"connect_account","attemptId":"bad"}`, `{"action":"connect_another_account"}`, `{"action":"connect_account","label":"Example"}`, `{"action":"connect_account","attemptId":"0123456789abcdef0123456789abcdef","label":"bad"}`} {
		r := httptest.NewRequest(http.MethodPost, "/", strings.NewReader(body))
		r.Header.Set(panelFormHeader, c.prepareValue("s"))
		w := httptest.NewRecorder()
		c.serveAccountConnect(w, r, panelAccountConnectPrefix+e.ID+"/prepare", "s")
		if w.Code == 200 {
			t.Fatalf("accepted %s", body)
		}
	}
	for _, label := range []string{" Example", "Example workshop", "example WORKSHOP", "x\n", ""} {
		if label != "" && c.prepareAccountConnectLabel("s", e.ID, label).Preview != nil {
			t.Fatal("bad/duplicate label", label)
		}
	}
	// Malformed catalog fails before admission or process creation.
	if err := os.WriteFile(filepath.Join(c.reader.accountsDir, accounts.FileName), []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if c.prepareAccountConnect("s", e.ID).Preview != nil {
		t.Fatal("corrupt catalog accepted")
	}
}

const connectCodexResponses = `{"id":1,"result":{}}
{"id":2,"result":{"config":{"cli_auth_credentials_store":"file"}}}
{"id":3,"result":{"type":"chatgpt","loginId":"example-login","authUrl":"https://auth.openai.com/authorize?state=fictional"}}
{"method":"account/login/completed","params":{"loginId":"example-login","success":true}}
{"id":4,"result":{"account":{"type":"chatgpt","email":"private@example.com"}}}
`

func TestAccountConnectCodexProtocolAndPrivacy(t *testing.T) {
	for _, kind := range []string{"success", "wrong-login", "wrong-correlation", "api-key", "unsafe-url", "managed-store", "error", "overflow", "false-success", "case-alias"} {
		t.Run(kind, func(t *testing.T) {
			input := connectCodexResponses
			switch kind {
			case "wrong-login":
				input = strings.Replace(input, `"params":{"loginId":"example-login"`, `"params":{"loginId":"other"`, 1)
			case "wrong-correlation":
				input = strings.Replace(input, `"id":3`, `"id":9`, 1)
			case "api-key":
				input = strings.Replace(input, `"type":"chatgpt","email"`, `"type":"apiKey","email"`, 1)
			case "unsafe-url":
				input = strings.Replace(input, "https://auth.openai.com/authorize?state=fictional", "https://example.com/steal", 1)
			case "managed-store":
				input = strings.Replace(input, `"cli_auth_credentials_store":"file"`, `"cli_auth_credentials_store":"keyring"`, 1)
			case "error":
				input = strings.Replace(input, `"id":3,"result"`, `"id":3,"error":{"message":"fictional-secret"},"result"`, 1)
			case "overflow":
				input = strings.Repeat("x", 130<<10) + input
			case "case-alias":
				input = strings.ReplaceAll(input, `"loginId"`, `"LoginId"`)
			case "false-success":
				input = strings.Replace(input, `"success":true`, `"success":false`, 1)
			}
			var requests strings.Builder
			links := []string{}
			err := accountConnectCodexExchange(strings.NewReader(input), &requests, func(s string) { links = append(links, s) })
			if (err == nil) != (kind == "success") {
				t.Fatal(kind, err)
			}
			if strings.Contains(requests.String(), "thread/") || strings.Contains(requests.String(), "turn/") || strings.Contains(requests.String(), "rateLimits") || strings.Contains(requests.String(), "private@example.com") || strings.Contains(requests.String(), "fictional-secret") {
				t.Fatal("unrelated/private request")
			}
			if kind == "managed-store" && strings.Contains(requests.String(), "account/login/start") {
				t.Fatal("unisolated store started login")
			}
			if kind == "success" {
				var frames []map[string]any
				dec := json.NewDecoder(strings.NewReader(requests.String()))
				for dec.More() {
					var f map[string]any
					if dec.Decode(&f) != nil {
						break
					}
					frames = append(frames, f)
				}
				if len(frames) != 5 || len(links) != 1 || !strings.Contains(requests.String(), `"refreshToken":false`) {
					t.Fatal(requests.String())
				}
			}
		})
	}
}

func TestPanelAccountConnectAuthenticatedHTTPBoundary(t *testing.T) {
	c, _, e := connectFixture(t)
	state, err := newPanelState(54321, time.Hour, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	session, _ := state.newSession()
	handler := &panelHandler{state: state, guard: newDockGuard(54321), page: newDockServer(54321, state, c.reader), changes: c}
	for _, kind := range []string{"valid", "wrong-origin", "no-cookie", "no-csrf", "no-metadata", "read-only"} {
		t.Run(kind, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, state.base()+state.pagePath()+panelAccountConnectPrefix+e.ID+"/prepare", strings.NewReader(`{"action":"connect_account"}`))
			r.Header.Set("Origin", state.base())
			r.Header.Set("Content-Type", "application/json")
			r.Header.Set("Sec-Fetch-Site", "same-origin")
			r.Header.Set("Sec-Fetch-Dest", "empty")
			r.Header.Set("Sec-Fetch-Mode", "cors")
			r.Header.Set(panelFormHeader, c.prepareValue(session))
			r.AddCookie(&http.Cookie{Name: panelCookieName, Value: session})
			switch kind {
			case "wrong-origin":
				r.Header.Set("Origin", "https://example.com")
			case "no-cookie":
				r.Header.Del("Cookie")
			case "no-csrf":
				r.Header.Del(panelFormHeader)
			case "no-metadata":
				r.Header.Del("Sec-Fetch-Site")
			case "read-only":
				handler.changes = nil
			}
			w := httptest.NewRecorder()
			handler.ServeHTTP(w, r)
			handler.changes = c
			var reply panelChangeReply
			_ = json.Unmarshal(w.Body.Bytes(), &reply)
			if kind == "valid" {
				if w.Code != 200 || reply.Preview == nil {
					t.Fatal(w.Code, w.Body.String())
				}
			} else if w.Code == 200 {
				t.Fatal("guard bypass", kind)
			}
			if len(c.connectAttempts) != 0 {
				t.Fatal("prepare/guard starts provider")
			}
		})
	}
}

func TestPanelAccountConnectReplayAndLateDuplicateLabel(t *testing.T) {
	c, dir, e := connectFixture(t)
	started := make(chan struct{})
	release := make(chan struct{})
	c.connectRun = func(context.Context, accounts.Entry, []string, func(string)) error {
		close(started)
		<-release
		return nil
	}
	p := c.prepareAccountConnectLabel("s", e.ID, "Example second")
	if p.Preview == nil {
		t.Fatal("prepare")
	}
	reply := c.startAccountConnect("s", e.ID, p.Preview.Form)
	if reply.Connect == nil {
		t.Fatal("start")
	}
	<-started
	if r := c.startAccountConnect("s", e.ID, p.Preview.Form); r.Connect != nil || r.Result == nil || r.Result.Outcome != "review_again" {
		t.Fatal("form replay", r)
	}
	err := accounts.Update(dir, func(f *accounts.File) error {
		other := accounts.NewEntry(*f, e, "thr_example_other", time.Now(), c.quotaEnv)
		other.Label = "EXAMPLE SECOND"
		f.Entries = append(f.Entries, other)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	close(release)
	c.mu.Lock()
	a := c.connectAttempts[reply.Connect.ID]
	c.mu.Unlock()
	connectWait(t, a)
	v := c.readAccountConnect("s", e.ID, reply.Connect.ID, false).Connect
	if v.State == "connected" || v.ConnectedAccountID != "" {
		t.Fatal("duplicate published", v)
	}
	after, _ := os.ReadFile(filepath.Join(dir, accounts.FileName))
	if string(before) != string(after) {
		t.Fatal("concurrent unrelated publication overwritten")
	}
}

func TestPanelAccountConnectAdditivePreviewAndCloseJoin(t *testing.T) {
	c, dir, e := connectFixture(t)
	p := c.prepareAccountConnectLabel("example-session", e.ID, "Example second")
	if p.Preview == nil {
		t.Fatal("prepare")
	}
	raw, _ := json.Marshal(p.Preview.Lines)
	if !strings.Contains(string(raw), "new row named Example second") || !strings.Contains(string(raw), "template row Example workshop") || strings.Contains(string(raw), "this row select") {
		t.Fatal("misleading additive review", string(raw))
	}
	started, cleaning, finish := make(chan struct{}), make(chan struct{}), make(chan struct{})
	c.connectRun = func(ctx context.Context, _ accounts.Entry, _ []string, _ func(string)) error {
		close(started)
		<-ctx.Done()
		close(cleaning)
		<-finish
		return ctx.Err()
	}
	a := connectStart(t, c, e, "")
	<-started
	closed := make(chan struct{})
	go func() { c.closeAccountConnect(); close(closed) }()
	<-cleaning
	select {
	case <-closed:
		t.Fatal("close returned before owned task cleanup")
	default:
	}
	p = c.prepareAccountConnect("example-session", e.ID)
	if p.Preview != nil && c.startAccountConnect("example-session", e.ID, p.Preview.Form).Connect != nil {
		t.Fatal("shutdown admitted another provider")
	}
	close(finish)
	select {
	case <-closed:
	case <-time.After(3 * time.Second):
		t.Fatal("close failed to join owned task")
	}
	select {
	case <-a.done:
	default:
		t.Fatal("attempt survives close")
	}
	if g, err := accounts.CheckGuard(dir, e.ID); err != nil {
		t.Fatal("selection guard survives close")
	} else {
		g.Release()
	}
	v := c.readAccountConnect("example-session", e.ID, a.view.ID, false).Connect
	if v.State != "cancelled" || v.ConnectedAccountID != "" {
		t.Fatal("shutdown published", v)
	}
	// Close is repeatable and never restarts a task.
	c.closeAccountConnect()
}
