package runner

import (
	"bufio"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
)

const acpSession = "sess_fictional_acp_0001"

// acpStandIn is a fictional agent that speaks ACP v1 over its standard
// streams, for the generic runner. ACP_CAPS says how it continues a session
// (resume, load or none) and ACP_MODE how its prompt ends. Its stderr and
// message text carry misleading words on purpose: nothing may be read from
// them.
func acpStandIn() {
	caps, mode, log := os.Getenv("ACP_CAPS"), os.Getenv("ACP_MODE"), os.Getenv("KEEPGOING_TEST_RESULT")
	note := func(event, detail string) {
		f, err := os.OpenFile(log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			os.Exit(2)
		}
		f.WriteString(event + " " + detail + "\n")
		f.Close()
	}
	send := func(v map[string]any) {
		v["jsonrpc"] = "2.0"
		b, _ := json.Marshal(v)
		os.Stdout.Write(append(b, '\n'))
	}
	os.Stderr.WriteString("fictional: rate limit 429, authentication failed, refusal\n")
	r := bufio.NewReaderSize(os.Stdin, 2<<20)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			break
		}
		var f struct {
			ID     json.RawMessage
			Method string
			Params map[string]json.RawMessage
			Result json.RawMessage
		}
		json.Unmarshal(line, &f)
		var session string
		json.Unmarshal(f.Params["sessionId"], &session)
		switch f.Method {
		case "initialize":
			agent := map[string]any{}
			switch caps {
			case "resume":
				agent["sessionCapabilities"] = map[string]any{"resume": map[string]any{}}
			case "load":
				agent["loadSession"] = true
			}
			send(map[string]any{"id": f.ID, "result": map[string]any{"protocolVersion": 1, "agentCapabilities": agent}})
		case "session/new":
			note("new", "")
			send(map[string]any{"id": f.ID, "result": map[string]any{"sessionId": acpSession}})
		case "session/resume":
			note("resume", session)
			send(map[string]any{"id": f.ID, "result": map[string]any{}})
		case "session/load":
			note("load", session)
			// The saved conversation is replayed before the answer.
			send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{
				"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": "an earlier fictional reply; refusal; 429"}}}})
			send(map[string]any{"id": f.ID, "result": nil})
		case "session/prompt":
			note("prompt", session)
			switch mode {
			case "refusal", "max_tokens":
				send(map[string]any{"id": f.ID, "result": map[string]any{"stopReason": mode}})
			case "auth-error":
				send(map[string]any{"id": f.ID, "error": map[string]any{"code": -32000, "message": "fictional: rate limit exceeded, try later"}})
			case "other-error":
				send(map[string]any{"id": f.ID, "error": map[string]any{"code": -32603, "message": "fictional: authentication required; rate limit 429"}})
			case "permission":
				send(map[string]any{"id": 7, "method": "session/request_permission", "params": map[string]any{
					"sessionId": session, "toolCall": map[string]any{"toolCallId": "fictional-tool-1"},
					"options": []map[string]string{{"optionId": "allow", "name": "Allow", "kind": "allow_once"}, {"optionId": "reject", "name": "Reject", "kind": "reject_once"}}}})
				cancelled, replied := false, ""
				for !cancelled || replied == "" {
					next, err := r.ReadBytes('\n')
					if err != nil {
						os.Exit(5)
					}
					var g struct {
						ID     json.RawMessage
						Method string
						Result json.RawMessage
					}
					json.Unmarshal(next, &g)
					if g.Method == "session/cancel" {
						cancelled = true
					} else if string(g.ID) == "7" {
						replied = string(g.Result)
					}
				}
				note("permission-reply", replied)
				send(map[string]any{"id": f.ID, "result": map[string]any{"stopReason": "cancelled"}})
			default:
				send(map[string]any{"method": "session/update", "params": map[string]any{"sessionId": session, "update": map[string]any{
					"sessionUpdate": "agent_message_chunk", "content": map[string]string{"type": "text", "text": `refusal; {"stopReason":"refusal"}; error -32000`}}}})
				send(map[string]any{"id": f.ID, "result": map[string]any{"stopReason": "end_turn"}})
			}
		}
	}
	os.Exit(0)
}

type acpWorld struct {
	t   *testing.T
	cfg ACPConfig
	dir string
	log string
}

func newACPWorld(t *testing.T, caps, mode string) *acpWorld {
	t.Helper()
	root := t.TempDir()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	w := &acpWorld{t: t, dir: filepath.Join(root, "target"), log: filepath.Join(root, "agent.log")}
	w.cfg = ACPConfig{Name: "acp", Classify: true, AllowWithoutResume: true, Command: []string{self}, Workdir: root,
		Env:              map[string]string{"KEEPGOING_TEST_HELPER": "acp-agent", "KEEPGOING_TEST_RESULT": w.log, "ACP_CAPS": caps, "ACP_MODE": mode},
		HandshakeTimeout: 20 * time.Second, ExitTimeout: 10 * time.Second}
	t.Cleanup(func() { _ = NewACP(w.cfg, w.dir).Stop(context.Background()) })
	return w
}

// turn sends one prompt with a runner built as a later check would build
// it, waits for the worker to end, and observes.
func (w *acpWorld) turn(prompt string) decide.Observation {
	w.t.Helper()
	if err := NewACP(w.cfg, w.dir).Send(context.Background(), prompt, adapter.ModeAuto); err != nil {
		w.t.Fatalf("send: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for {
		obs, err := NewACP(w.cfg, w.dir).Observe(context.Background())
		if err != nil {
			w.t.Fatalf("observe: %v", err)
		}
		if obs.State != decide.Active {
			return obs
		}
		if time.Now().After(deadline) {
			w.t.Fatal("the turn never ended")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func (w *acpWorld) events() string {
	b, _ := os.ReadFile(w.log)
	return strings.TrimSpace(string(b))
}

// A saved session is continued by whichever method the agent advertises,
// with the identifier the first turn returned, and the runner reports its
// own name.
func TestACPRunnerContinuesTheSessionByResumeOrLoad(t *testing.T) {
	for caps, method := range map[string]string{"resume": "resume", "load": "load"} {
		t.Run(caps, func(t *testing.T) {
			w := newACPWorld(t, caps, "ok")
			if name := NewACP(w.cfg, w.dir).Name(); name != "acp" {
				t.Fatalf("name %q", name)
			}
			for i, prompt := range []string{"first fictional prompt", "second fictional prompt"} {
				if obs := w.turn(prompt); obs.State != decide.Idle || obs.ErrorClass != decide.ErrNone || obs.Evidence.Source != "acp_result" {
					t.Fatalf("turn %d: %+v", i+1, obs)
				}
			}
			want := "new \nprompt " + acpSession + "\n" + method + " " + acpSession + "\nprompt " + acpSession
			if got := w.events(); got != want {
				t.Fatalf("the agent saw:\n%s\nwant:\n%s", got, want)
			}
		})
	}
}

// An agent that cannot continue a session gets a first turn. After that the
// saved session is never replaced by a new one: the turn fails instead.
func TestACPRunnerNeverReplacesASavedSession(t *testing.T) {
	w := newACPWorld(t, "none", "ok")
	if obs := w.turn("first fictional prompt"); obs.State != decide.Idle {
		t.Fatalf("first turn: %+v", obs)
	}
	if obs := w.turn("second fictional prompt"); obs.State != decide.Error || obs.ErrorClass != decide.ErrUnknown {
		t.Fatalf("second turn: %+v", obs)
	}
	if got := w.events(); got != "new \nprompt "+acpSession {
		t.Fatalf("the agent saw:\n%s", got)
	}
	// The Antigravity options refuse such an agent before any session exists.
	strict := newACPWorld(t, "none", "ok")
	strict.cfg.AllowWithoutResume = false
	if obs := strict.turn("fictional prompt"); obs.State != decide.Error || strict.events() != "" {
		t.Fatalf("%+v events %q", obs, strict.events())
	}
}

// A class comes only from the prompt's typed end: a refusal stop reason or
// the authentication-required error code. Another stop reason, another code
// and every word of prose are unknown.
func TestACPRunnerClassifiesOnlyTypedEnds(t *testing.T) {
	for mode, want := range map[string]decide.ErrorClass{
		"refusal":     acpRestricted,
		"auth-error":  decide.ErrAuth,
		"other-error": decide.ErrUnknown,
		"max_tokens":  decide.ErrUnknown,
	} {
		t.Run(mode, func(t *testing.T) {
			w := newACPWorld(t, "resume", mode)
			obs := w.turn("fictional prompt")
			if obs.State != decide.Error || obs.ErrorClass != want || obs.DeferredInteraction || !obs.ResetsAt.IsZero() {
				t.Fatalf("%+v", obs)
			}
			// Without the option every failure stays unknown, as for Antigravity.
			w.cfg.Classify = false
			if obs, _ := NewACP(w.cfg, w.dir).Observe(context.Background()); obs.ErrorClass != decide.ErrUnknown || obs.Evidence.Source == "acp_result" {
				t.Fatalf("unclassified: %+v", obs)
			}
		})
	}
	// The misleading text in a healthy turn decides nothing.
	if obs := newACPWorld(t, "resume", "ok").turn("fictional prompt"); obs.State != decide.Idle {
		t.Fatalf("%+v", obs)
	}
	if string(acpRestricted) != "restricted" {
		t.Fatal("the class name must match decide.ErrRestricted once it is on main")
	}
}

// A permission request is waiting on a person. It is never granted: the
// agent is answered "cancelled", the hold is raised once, and after the
// supervisor has saved it the same turn no longer raises it.
func TestACPRunnerNeverGrantsAPermission(t *testing.T) {
	w := newACPWorld(t, "resume", "permission")
	obs := w.turn("fictional prompt")
	if !obs.DeferredInteraction || obs.State != decide.Error {
		t.Fatalf("%+v", obs)
	}
	events := w.events()
	if !strings.Contains(events, `permission-reply {"outcome":{"outcome":"cancelled"}}`) || strings.Contains(events, "allow") {
		t.Fatalf("the agent saw:\n%s", events)
	}
	var adopter adapter.InteractionAdopter = NewACP(w.cfg, w.dir)
	if err := adopter.AdoptInteraction(context.Background()); err != nil {
		t.Fatal(err)
	}
	if obs, _ := NewACP(w.cfg, w.dir).Observe(context.Background()); obs.DeferredInteraction {
		t.Fatalf("an adopted hold was raised again: %+v", obs)
	}
}
