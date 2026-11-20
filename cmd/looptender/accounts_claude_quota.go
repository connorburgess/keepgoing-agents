package main

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"github.com/looptender/looptender/internal/envvar"
)

// This experimental protocol is intentionally version-gated. A newer executable
// must be reviewed before its startup, hooks and usage behavior is trusted.
const accountClaudeQuotaVersion = "2.1.326"

// A wrapper can drop --await-claim while reporting the same version. Only the
// inspected native artifact is admitted, before any process is started. Other
// platform builds and updates require independent startup/handler verification.
const accountClaudeQuotaDigest = "e214ad76dcc0e846b03be28ed1c2734aacd4e27acb527cb7f8a9e2bed644e837"

var accountClaudeQuotaArgs = []string{
	"--print", "--input-format", "stream-json", "--output-format", "stream-json",
	"--verbose", "--await-claim", "--safe-mode", "--no-session-persistence",
	"--setting-sources", "", "--settings", `{"disableAllHooks":true}`,
	"--strict-mcp-config", "--mcp-config", `{"mcpServers":{}}`,
	"--tools", "", "--disable-slash-commands",
}

func accountQuotaExecutionEnvironment(e accounts.Entry, env []string) []string {
	out := accounts.ExecutionEnvironment(e, env)
	if e.Provider == "claude-code" {
		// Preserve the caller's provenance in the isolated Claude child. This
		// read grants no BB lifecycle authority and never claims a session.
		for _, pair := range env {
			key, _, _ := strings.Cut(pair, "=")
			if envvar.IsSupervisorSession(key) {
				out = append(out, pair)
			}
		}
	}
	return out
}

// A parked process is never claimed: no user message, resume, model request or
// hook callback is sent. In the reviewed CLI, claiming starts SessionStart hooks;
// safe-mode alone would still permit managed hooks. skip_behaviors also excludes
// the native behavior reader, which otherwise scans persisted conversations.
func accountClaudeQuotaRead(ctx context.Context, cli string, env []string, workdir string) (raw []byte, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			raw, resultErr = nil, ctx.Err()
		}
	}()
	if !accountClaudeQuotaEnvironment(env) {
		return nil, errors.New("isolation_unavailable")
	}
	image, cleanup, err := accountClaudeQuotaStage(ctx, cli, accountClaudeQuotaDigest)
	if err != nil {
		return nil, err
	}
	defer cleanup()
	return accountClaudeQuotaProcess(ctx, image, env, workdir)
}

func accountClaudeQuotaEnvironment(env []string) bool {
	for _, entry := range env {
		key, _, _ := strings.Cut(entry, "=")
		key = strings.ToUpper(key)
		if key == "NODE_OPTIONS" || key == "NODE_PATH" || key == "BUN_OPTIONS" || key == "BUN_INSPECT" || strings.HasPrefix(key, "DYLD_") || strings.HasPrefix(key, "LD_") {
			return false
		}
	}
	return true
}

// Launch only bytes copied from the admitted handle, never the selected pathname
// again. Provider updates may replace that pathname during or after admission.
// The private image survives until the owned child has been killed and waited.
func accountClaudeQuotaStage(ctx context.Context, path, digest string) (image string, cleanup func(), resultErr error) {
	unavailable := errors.New("unverified_cli")
	if err := ctx.Err(); err != nil {
		return "", nil, err
	}
	f, err := accountClaudeQuotaOpen(path)
	if err != nil {
		return "", nil, unavailable
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil || !st.Mode().IsRegular() || st.Size() > 512<<20 {
		return "", nil, unavailable
	}
	dir, err := os.MkdirTemp("", "looptender-claude-quota-")
	if err != nil {
		return "", nil, unavailable
	}
	cleanup = func() { os.RemoveAll(dir) }
	defer func() {
		if resultErr != nil {
			cleanup()
		}
	}()
	image = filepath.Join(dir, "claude")
	staged, err := os.OpenFile(image, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return "", cleanup, unavailable
	}
	defer staged.Close()
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return "", cleanup, err
		}
		n, readErr := f.Read(buf)
		total += int64(n)
		if total > 512<<20 {
			return "", cleanup, unavailable
		}
		if n > 0 {
			if _, err := staged.Write(buf[:n]); err != nil {
				return "", cleanup, unavailable
			}
			h.Write(buf[:n])
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", cleanup, unavailable
		}
	}
	if hex.EncodeToString(h.Sum(nil)) != digest {
		return "", cleanup, unavailable
	}
	if err := staged.Close(); err != nil {
		return "", cleanup, unavailable
	}
	if err := os.Chmod(image, 0500); err != nil {
		return "", cleanup, unavailable
	}
	if err := ctx.Err(); err != nil {
		return "", cleanup, err
	}
	return image, cleanup, nil
}

// Kept separate so fictional subprocess fixtures can verify the exchange and
// cancellation without admitting a stand-in through the native-artifact fence.
func accountClaudeQuotaProcess(ctx context.Context, cli string, env []string, workdir string) (raw []byte, resultErr error) {
	defer func() {
		if ctx.Err() != nil {
			raw, resultErr = nil, ctx.Err()
		}
	}()
	cmd := exec.CommandContext(ctx, cli, accountClaudeQuotaArgs...)
	cmd.Env, cmd.Dir, cmd.Stderr = env, workdir, io.Discard
	cmd.WaitDelay = time.Second
	in, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	defer in.Close()
	out, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err = cmd.Start(); err != nil {
		return nil, err
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
	return accountClaudeQuotaExchange(out, in)
}

// Only correlated control responses are accepted, including during startup.
// Permission/hook requests and ordinary conversation frames are never answered.
// Provider strings (including error messages, hook commands and account details)
// are discarded; only fixed local error reasons escape this exchange.
func accountClaudeQuotaExchange(out io.Reader, in io.Writer) ([]byte, error) {
	lines := bufio.NewScanner(io.LimitReader(out, 64<<10))
	lines.Buffer(make([]byte, 4096), 64<<10)
	schema := errors.New("schema_unavailable")
	request := func(id, subtype string, fields map[string]any) (map[string]json.RawMessage, error) {
		fields["subtype"] = subtype
		if err := json.NewEncoder(in).Encode(map[string]any{"type": "control_request", "request_id": id, "request": fields}); err != nil {
			return nil, errors.New("status_unavailable")
		}
		var frame map[string]json.RawMessage
		for {
			if !lines.Scan() {
				return nil, errors.New("status_unavailable")
			}
			var ok bool
			frame, ok = accountClaudeObject(lines.Bytes())
			if !ok || !accountClaudeCanonical(frame, "type", "subtype", "response") {
				return nil, schema
			}
			// The pinned native image may emit this UI-only invalidation before
			// its control reply. Discard the entire notification, including IDs.
			// It is never answered; all other system/conversation frames refuse.
			if string(frame["type"]) == `"system"` && string(frame["subtype"]) == `"ui_invalidate"` {
				continue
			}
			if string(frame["type"]) != `"control_response"` {
				return nil, schema
			}
			break
		}
		response, ok := accountClaudeObject(frame["response"])
		if !ok || !accountClaudeCanonical(response, "request_id", "subtype", "response", "error") || string(response["request_id"]) != `"`+id+`"` {
			return nil, schema
		}
		if string(response["subtype"]) == `"error"` {
			return nil, errors.New("status_unavailable")
		}
		if string(response["subtype"]) != `"success"` {
			return nil, schema
		}
		data, ok := accountClaudeObject(response["response"])
		if !ok {
			return nil, schema
		}
		return data, nil
	}
	init, err := request("quota-init", "initialize", map[string]any{"hooks": map[string]any{}, "mcpServers": map[string]any{}})
	if err != nil {
		return nil, err
	}
	if !accountClaudeCanonical(init, "claude_code_version", "hooks_applied", "session_state") || string(init["claude_code_version"]) != `"`+accountClaudeQuotaVersion+`"` || string(init["hooks_applied"]) != "true" || string(init["session_state"]) != `"idle"` {
		return nil, errors.New("isolation_unavailable")
	}
	hooks, err := request("quota-hooks", "get_hooks_listing", map[string]any{})
	if err != nil {
		return nil, err
	}
	policy, ok := accountClaudeObject(hooks["policy"])
	// Missing/unknown policy is not evidence of disabled hooks. Refuse even
	// dormant configured hooks, managed policies or unreadable policy data.
	if !ok || !accountClaudeCanonical(hooks, "policy") || !accountClaudeCanonical(policy, "allDisabled", "policyHookCount", "policyUnreadable") || string(policy["allDisabled"]) != "true" || string(policy["policyHookCount"]) != "0" ||
		(string(policy["policyUnreadable"]) != "" && string(policy["policyUnreadable"]) != "false") {
		return nil, errors.New("isolation_unavailable")
	}
	usage, err := request("quota-usage", "get_usage", map[string]any{"skip_behaviors": true})
	if err != nil {
		return nil, err
	}
	if !accountClaudeCanonical(usage, "behaviors", "rate_limits", "rate_limits_available") || string(usage["behaviors"]) != "null" {
		return nil, errors.New("isolation_unavailable")
	}
	// Never return session tokens/costs, identities, subscription data or any
	// behavior output. Native cached/seeded data has no provider timestamp.
	return json.Marshal(map[string]json.RawMessage{
		"rate_limits_available": usage["rate_limits_available"], "rate_limits": usage["rate_limits"],
	})
}

func accountClaudeObject(raw []byte) (map[string]json.RawMessage, bool) {
	if !accounts.QuotaJSONUnique(raw) {
		return nil, false
	}
	var m map[string]json.RawMessage
	err := json.Unmarshal(raw, &m)
	return m, err == nil && m != nil
}

func accountClaudeCanonical(m map[string]json.RawMessage, keys ...string) bool {
	for key := range m {
		for _, canonical := range keys {
			if key != canonical && strings.EqualFold(key, canonical) {
				return false
			}
		}
	}
	return true
}

// These scopes are independent, even when their durations coincide. Model
// display names, monetary extra_usage and session costs are not quota percents.
func accountClaudeQuotaParse(raw []byte, err error) (*accounts.QuotaDetails, string) {
	if err != nil {
		if err.Error() == "isolation_unavailable" || err.Error() == "unverified_cli" {
			return nil, err.Error()
		}
		_, reason := accountCodexPlan(nil, err)
		return nil, reason
	}
	if len(raw) > 64<<10 {
		return nil, "schema_unavailable"
	}
	top, ok := accountClaudeObject(raw)
	if !ok || !accountClaudeCanonical(top, "rate_limits_available", "rate_limits") {
		return nil, "schema_unavailable"
	}
	if string(top["rate_limits_available"]) == "false" {
		return nil, "quota_unavailable"
	}
	if string(top["rate_limits_available"]) != "true" {
		return nil, "schema_unavailable"
	}
	if string(top["rate_limits"]) == "null" {
		return nil, "no_windows"
	}
	limits, ok := accountClaudeObject(top["rate_limits"])
	if !ok {
		return nil, "schema_unavailable"
	}
	d := &accounts.QuotaDetails{Buckets: []accounts.QuotaBucket{}}
	scopes := []struct {
		key     string
		minutes int64
	}{{"five_hour", 300}, {"seven_day", 10080}, {"seven_day_oauth_apps", 10080}, {"seven_day_opus", 10080}, {"seven_day_sonnet", 10080}}
	known := map[string]bool{}
	for _, scope := range scopes {
		known[scope.key] = true
		if !accountClaudeCanonical(limits, scope.key) {
			return nil, "schema_unavailable"
		}
		value, exists := limits[scope.key]
		if !exists || string(value) == "null" {
			continue
		}
		window, ok := accountClaudeObject(value)
		if !ok || !accountClaudeCanonical(window, "utilization", "resets_at") {
			return nil, "schema_unavailable"
		}
		var percent *float64
		if json.Unmarshal(window["utilization"], &percent) != nil || percent == nil || math.IsNaN(*percent) || math.IsInf(*percent, 0) || *percent < 0 || *percent > 100 {
			return nil, "schema_unavailable"
		}
		var reset *time.Time
		if value, exists := window["resets_at"]; exists && string(value) != "null" {
			var text string
			if json.Unmarshal(value, &text) != nil {
				return nil, "schema_unavailable"
			}
			t, err := time.Parse(time.RFC3339Nano, text)
			if err != nil || t.Year() < 2000 || t.Year() > 9999 {
				return nil, "schema_unavailable"
			}
			u := t.UTC()
			reset = &u
		}
		for key := range window {
			if key != "utilization" && key != "resets_at" {
				d.Partial = true
			}
		}
		minutes := scope.minutes
		d.Buckets = append(d.Buckets, accounts.QuotaBucket{Key: scope.key, Windows: []accounts.QuotaMeter{{Name: "primary", UsedPercent: *percent, DurationMinutes: &minutes, ResetsAt: reset}}})
	}
	for key, value := range limits {
		if !known[key] && string(value) != "null" {
			d.Partial = true
		}
	}
	if len(d.Buckets) == 0 {
		return nil, "no_windows"
	}
	return d, ""
}
