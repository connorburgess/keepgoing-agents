package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os/exec"
	"strconv"
	"time"
)

// accountCodexRead follows the existing Codex limits stdio exchange, but stays
// in the account command layer: exact selected environment and working directory,
// a smaller total output bound, and no provider/session adapter defaults.
func accountCodexRead(ctx context.Context, cli string, env []string, workdir string) (raw []byte, resultErr error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	defer func() {
		if ctx.Err() != nil {
			raw, resultErr = nil, ctx.Err()
		}
	}()
	cmd := exec.CommandContext(ctx, cli, "app-server")
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
	// Closing our owned pipe also unblocks a read if a descendant retains stdout.
	done := make(chan struct{})
	defer close(done)
	go func() {
		select {
		case <-ctx.Done():
			out.Close()
		case <-done:
		}
	}()
	lines := bufio.NewScanner(io.LimitReader(out, 64*1024))
	lines.Buffer(make([]byte, 4096), 64*1024)
	send := func(v any) error { return json.NewEncoder(in).Encode(v) }
	await := func(id int) ([]byte, error) {
		for lines.Scan() {
			var m struct {
				ID     json.RawMessage `json:"id"`
				Method string          `json:"method"`
				Result json.RawMessage `json:"result"`
				Error  *struct {
					Code int `json:"code"`
				} `json:"error"`
			}
			if json.Unmarshal(lines.Bytes(), &m) != nil {
				return nil, errors.New("schema_unavailable")
			}
			if len(m.ID) == 0 && m.Method != "" {
				continue
			}
			if string(m.ID) != strconv.Itoa(id) || m.Method != "" {
				return nil, errors.New("schema_unavailable")
			}
			if m.Error != nil {
				if m.Error.Code == -32601 {
					return nil, errors.New("unsupported")
				}
				return nil, errors.New("status_unavailable")
			}
			if len(m.Result) == 0 || string(m.Result) == "null" {
				return nil, errors.New("schema_unavailable")
			}
			return m.Result, nil
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, errors.New("status_unavailable")
	}
	if err = send(map[string]any{"id": 1, "method": "initialize", "params": map[string]any{"clientInfo": map[string]string{"name": "looptender", "version": "1"}}}); err != nil {
		return nil, err
	}
	if _, err = await(1); err != nil {
		return nil, err
	}
	if err = send(map[string]any{"method": "initialized"}); err != nil {
		return nil, err
	}
	if err = send(map[string]any{"id": 2, "method": "account/read", "params": map[string]bool{"refreshToken": false}}); err != nil {
		return nil, err
	}
	return await(2)
}

func accountCodexPlan(raw []byte, err error) (string, string) {
	if err != nil {
		switch {
		case errors.Is(err, context.DeadlineExceeded):
			return "", "timeout"
		case errors.Is(err, context.Canceled):
			return "", "cancelled"
		}
		switch err.Error() {
		case "unsupported", "schema_unavailable":
			return "", err.Error()
		}
		return "", "status_unavailable"
	}
	var r struct {
		Account json.RawMessage `json:"account"`
	}
	if json.Unmarshal(raw, &r) != nil || len(r.Account) == 0 {
		return "", "schema_unavailable"
	}
	if string(r.Account) == "null" {
		return "", "no_account"
	}
	var a struct {
		Type string `json:"type"`
		Plan string `json:"planType"`
	}
	if json.Unmarshal(r.Account, &a) != nil || a.Type == "" {
		return "", "schema_unavailable"
	}
	if a.Type == "apiKey" {
		return "", "api_key"
	}
	if a.Type != "chatgpt" {
		return "", "account_type"
	}
	// Installed CLI generated PlanType enum; unknown and future values are withheld.
	switch a.Plan {
	case "free", "go", "plus", "pro", "prolite", "promax", "team", "self_serve_business_prolite", "self_serve_business_usage_based", "business", "ent26", "enterprise_cbp_automation", "enterprise_cbp_usage_based", "enterprise", "edu", "edu_plus", "edu_pro":
		return a.Plan, ""
	}
	return "", "not_recognized"
}
