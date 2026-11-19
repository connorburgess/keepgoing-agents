package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

var errConnectProtocol = errors.New("provider sign-in protocol unavailable")

// Existing reviewed nonblocking/no-follow admission is shared with the quota
// transport. Only standalone native executables are supported: staging a JS
// launcher or an adjacent-resource wrapper would change its launch semantics.
func accountConnectDigest(ctx context.Context, e accounts.Entry) (string, error) {
	f, err := accountClaudeQuotaOpen(e.CLI)
	if err != nil {
		return "", errConnectProtocol
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 512<<20 {
		return "", errConnectProtocol
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	total := int64(0)
	first := true
	for {
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		n, re := f.Read(buf)
		total += int64(n)
		if total > 512<<20 {
			return "", errConnectProtocol
		}
		if first {
			first = false
			if n < 4 || !accountConnectNativeMagic(buf[:n]) {
				return "", errConnectProtocol
			}
		}
		if n > 0 {
			h.Write(buf[:n])
		}
		if re == io.EOF {
			break
		}
		if re != nil {
			return "", errConnectProtocol
		}
	}
	digest := hex.EncodeToString(h.Sum(nil))
	if e.Provider == "claude-code" && digest != accountClaudeQuotaDigest {
		return "", errConnectProtocol
	}
	return digest, nil
}
func accountConnectNativeMagic(b []byte) bool {
	return len(b) >= 4 && (bytes.Equal(b[:4], []byte{0x7f, 'E', 'L', 'F'}) || bytes.Equal(b[:4], []byte{0xcf, 0xfa, 0xed, 0xfe}) || bytes.Equal(b[:4], []byte{0xfe, 0xed, 0xfa, 0xcf}))
}
func accountConnectProvider(ctx context.Context, e accounts.Entry, env []string, progress func(string)) error {
	if !accountConnectEnvironmentAllowed(env) {
		return errConnectProtocol
	}
	if e.Provider == "claude-code" {
		return accountConnectClaude(ctx, e, env)
	}
	if e.Provider == "codex" {
		return accountConnectCodex(ctx, e, env, progress)
	}
	return errConnectProtocol
}

// This is the provider's auth subcommand, not an interactive agent session.
// The provider opens its own browser. No OAuth endpoints, token exchange,
// transcript parsing or manual-code input is implemented by looptender.
func accountConnectClaude(ctx context.Context, e accounts.Entry, env []string) error {
	cmd := exec.CommandContext(ctx, e.CLI, "--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "auth", "login", "--claudeai")
	cmd.Env, cmd.Dir = env, e.Workdir
	cmd.WaitDelay = time.Second
	// Hold stdin open for the provider-owned browser callback. Never feed a
	// token/code or close it early (the native fallback treats EOF as cancellation).
	in, err := cmd.StdinPipe()
	if err != nil {
		return errConnectProtocol
	}
	defer in.Close()
	output := &doctorBuffer{remaining: 64 << 10}
	cmd.Stdout = output
	cmd.Stderr = io.Discard
	if err = cmd.Run(); err != nil {
		return errConnectProtocol
	}
	if output.exceeded {
		return errConnectProtocol
	}
	raw, err := accountExec(ctx, e.CLI, []string{"--setting-sources", "", "--settings", `{"disableAllHooks":true}`, "auth", "status", "--json"}, env, e.Workdir)
	if err != nil || !accountConnectJSON(raw) {
		return errConnectProtocol
	}
	var status struct {
		LoggedIn        bool   `json:"loggedIn"`
		AuthMethod      string `json:"authMethod"`
		ConfigDirectory string `json:"configDirectory"`
	}
	if json.Unmarshal(raw, &status) != nil || !status.LoggedIn || status.AuthMethod != "claude.ai" || status.ConfigDirectory != e.Directory {
		return errConnectProtocol
	}
	return nil
}

func accountConnectAuthorizationURL(raw string) bool {
	if len(raw) > 8192 || strings.ContainsAny(raw, "\r\n\x00") {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Port() != "" || u.Fragment != "" {
		return false
	}
	return (u.Host == "auth.openai.com" || u.Host == "auth0.openai.com" || u.Host == "chatgpt.com") && strings.HasPrefix(u.Path, "/")
}

// login-only app-server task. The caller keeps it alive until completion or
// cancellation. Output has a total bound; stderr and provider errors are discarded.
func accountConnectCodex(ctx context.Context, e accounts.Entry, env []string, progress func(string)) error {
	// Persist the supported file-store setting inside ONLY the fresh context so
	// subsequent selected-context reads retain the same isolation. No auth file
	// is read, copied, or written by looptender.
	config := []byte("cli_auth_credentials_store = \"file\"\n")
	f, err := os.OpenFile(filepath.Join(e.Directory, "config.toml"), os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errConnectProtocol
	}
	_, err = f.Write(config)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		return errConnectProtocol
	}
	cmd := exec.CommandContext(ctx, e.CLI, "app-server", "-c", `cli_auth_credentials_store="file"`)
	cmd.Env, cmd.Dir, cmd.Stderr = env, e.Workdir, io.Discard
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return errConnectProtocol
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return errConnectProtocol
	}
	if err = cmd.Start(); err != nil {
		return errConnectProtocol
	}
	defer func() { cmd.Process.Kill(); out.Close(); cmd.Wait() }()
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			out.Close()
		case <-done:
		}
	}()
	return accountConnectCodexExchange(out, in, progress)
}
func accountConnectCodexExchange(out io.Reader, in io.Writer, progress func(string)) error {
	// LimitedReader+Scanner caps the complete exchange, not each notification.
	lines := bufio.NewScanner(io.LimitReader(out, 128<<10))
	lines.Buffer(make([]byte, 4096), 64<<10)
	send := func(id int, method string, params any) error {
		return json.NewEncoder(in).Encode(map[string]any{"id": id, "method": method, "params": params})
	}
	type frame struct {
		ID     json.RawMessage `json:"id"`
		Method string          `json:"method"`
		Result json.RawMessage `json:"result"`
		Error  json.RawMessage `json:"error"`
		Params json.RawMessage `json:"params"`
	}
	next := func() (frame, error) {
		var f frame
		if !lines.Scan() || !accountConnectJSON(lines.Bytes()) || json.Unmarshal(lines.Bytes(), &f) != nil {
			return f, errConnectProtocol
		}
		return f, nil
	}
	await := func(id string) (json.RawMessage, error) {
		for {
			f, err := next()
			if err != nil {
				return nil, err
			}
			if len(f.ID) == 0 && f.Method != "" {
				if f.Method == "account/updated" {
					continue
				}
				return nil, errConnectProtocol
			}
			if string(f.ID) != id || f.Method != "" || (len(f.Error) > 0 && string(f.Error) != "null") || len(f.Result) == 0 {
				return nil, errConnectProtocol
			}
			return f.Result, nil
		}
	}
	if send(1, "initialize", map[string]any{"clientInfo": map[string]string{"name": "looptender", "version": "1"}}) != nil {
		return errConnectProtocol
	}
	if _, err := await("1"); err != nil {
		return err
	}
	if json.NewEncoder(in).Encode(map[string]string{"method": "initialized"}) != nil {
		return errConnectProtocol
	}
	// Managed settings may override a requested store. Refuse before login if
	// the effective structured configuration cannot confirm file isolation.
	if send(2, "config/read", map[string]bool{"includeLayers": false}) != nil {
		return errConnectProtocol
	}
	raw, err := await("2")
	if err != nil {
		return err
	}
	var cfg struct {
		Config struct {
			Store string `json:"cli_auth_credentials_store"`
		} `json:"config"`
	}
	if json.Unmarshal(raw, &cfg) != nil || cfg.Config.Store != "file" {
		return errConnectProtocol
	}
	if send(3, "account/login/start", map[string]string{"type": "chatgpt"}) != nil {
		return errConnectProtocol
	}
	raw, err = await("3")
	if err != nil {
		return err
	}
	var start struct {
		Type    string `json:"type"`
		LoginID string `json:"loginId"`
		URL     string `json:"authUrl"`
	}
	if json.Unmarshal(raw, &start) != nil || start.Type != "chatgpt" || len(start.LoginID) > 128 || start.LoginID == "" || !accountConnectAuthorizationURL(start.URL) {
		return errConnectProtocol
	}
	progress(start.URL)
	for {
		f, err := next()
		if err != nil {
			return err
		}
		if len(f.ID) > 0 {
			return errConnectProtocol
		}
		if f.Method == "account/updated" {
			continue
		}
		if f.Method != "account/login/completed" {
			return errConnectProtocol
		}
		var complete struct {
			LoginID string `json:"loginId"`
			Success *bool  `json:"success"`
		}
		if json.Unmarshal(f.Params, &complete) != nil || complete.LoginID != start.LoginID || complete.Success == nil || !*complete.Success {
			return errConnectProtocol
		}
		break
	}
	if send(4, "account/read", map[string]bool{"refreshToken": false}) != nil {
		return errConnectProtocol
	}
	raw, err = await("4")
	if err != nil {
		return err
	}
	var result struct {
		Account *struct {
			Type string `json:"type"`
		} `json:"account"`
	}
	if json.Unmarshal(raw, &result) != nil || result.Account == nil || result.Account.Type != "chatgpt" {
		return errConnectProtocol
	}
	return nil
}

// encoding/json accepts case aliases. Provider success/correlation fields must
// use their exact protocol spelling, including inside nested responses.
func accountConnectJSON(raw []byte) bool {
	if !accounts.QuotaJSONUnique(raw) {
		return false
	}
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return false
	}
	keys := []string{"id", "method", "result", "error", "params", "config", "cli_auth_credentials_store", "type", "loginId", "authUrl", "success", "account", "loggedIn", "authMethod", "configDirectory"}
	var valid func(any) bool
	valid = func(v any) bool {
		switch m := v.(type) {
		case map[string]any:
			for k, x := range m {
				for _, canonical := range keys {
					if k != canonical && strings.EqualFold(k, canonical) {
						return false
					}
				}
				if !valid(x) {
					return false
				}
			}
		case []any:
			for _, x := range m {
				if !valid(x) {
					return false
				}
			}
		}
		return true
	}
	return valid(value)
}
