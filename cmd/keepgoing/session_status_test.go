package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/state"
	"gopkg.in/yaml.v3"
)

const savedExampleID = "00000000-0000-4000-8000-000000000001"

func TestHeadlessSavedSessionStatusAndDoctor(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		for _, content := range []string{"missing", savedExampleID, doctorPrivate} {
			t.Run(provider+"/"+content, func(t *testing.T) {
				f := newDoctorFixture(t)
				delete(f.loop, "bb")
				f.loop["adapter"] = provider
				f.loop[provider] = map[string]any{"cli": f.executable, "workdir": f.root}
				f.save(t)
				if err := control.Save(f.state, control.File{Enabled: true, Paused: true}); err != nil {
					t.Fatal(err)
				}
				sessionFile := filepath.Join(f.state, "targets", "primary", "session")
				if content != "missing" {
					if err := os.MkdirAll(filepath.Dir(sessionFile), 0o700); err != nil {
						t.Fatal(err)
					}
					if err := os.WriteFile(sessionFile, []byte(content), 0o600); err != nil {
						t.Fatal(err)
					}
				}
				var out, errOut bytes.Buffer
				code := run(context.Background(), []string{"status", "-c", f.config, "--json"}, &out, &errOut)
				var result statusOut
				if code != 0 || json.Unmarshal(out.Bytes(), &result) != nil {
					t.Fatalf("%d %s %s", code, &out, &errOut)
				}
				want, wantID, checkStatus := "unavailable", "", "warning"
				if content == savedExampleID {
					want, wantID, checkStatus = "saved", savedExampleID, "ok"
				}
				if content == "missing" {
					want, checkStatus = "missing", "unavailable"
				}
				s := result.TargetSession
				if s == nil || s.State != want || s.ID != wantID || s.LiveVerified || result.ControlState != "paused" {
					t.Fatalf("%+v", result)
				}
				if strings.Contains(out.String(), doctorPrivate) {
					t.Fatal("private session bytes exposed")
				}
				code, report, _ := f.run(t, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
					t.Fatal("offline executed CLI")
					return nil, nil
				}, "--offline")
				if code != 0 || report.Sessions["primary"].State != want || report.Sessions["primary"].ID != wantID || doctorCheckByID(t, report, "agent.primary.session").Status != checkStatus {
					t.Fatalf("%+v", report)
				}
				ctl, err := control.Load(f.state)
				if err != nil || !ctl.Paused {
					t.Fatal("hold changed")
				}
				if content != "missing" {
					b, _ := os.ReadFile(sessionFile)
					if string(b) != content {
						t.Fatal("session changed")
					}
				}
				if _, err := os.Stat(filepath.Join(f.state, state.FileName)); !os.IsNotExist(err) {
					t.Fatal("diagnostic wrote tick state")
				}
			})
		}
	}
}

func TestSavedSessionIsTargetScoped(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "targets", "fallback-a", "session")
	if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(savedExampleID), 0o600); err != nil {
		t.Fatal(err)
	}
	if s := savedSession(dir, "primary", "codex"); s.State != "missing" {
		t.Fatal("attributed fallback to primary")
	}
	if s := savedSession(dir, "fallback-a", "claude"); s.ID != savedExampleID || s.LiveVerified {
		t.Fatalf("%+v", s)
	}
	for _, adapter := range []string{"bb", "process", ""} {
		if savedSession(dir, "fallback-a", adapter) != nil {
			t.Fatal("exposed headless ID for another adapter")
		}
	}
}

func TestStatusUsesActiveFallbackSessionAndDoctorSkipsDisabled(t *testing.T) {
	f := newDoctorFixture(t)
	delete(f.loop, "bb")
	f.loop["adapter"] = "codex"
	f.loop["codex"] = map[string]any{"cli": f.executable, "workdir": f.root}
	var candidates []map[string]any
	for _, id := range []string{"fallback-a", "disabled-a"} {
		candidates = append(candidates, map[string]any{"id": id, "adapter": "codex", "disabled": id == "disabled-a", "codex": map[string]any{"cli": f.executable, "workdir": f.root}})
	}
	b, err := yaml.Marshal(map[string]any{"version": 1, "candidates": candidates})
	if err != nil {
		t.Fatal(err)
	}
	fb := filepath.Join(f.root, "fallbacks.yaml")
	if err := os.WriteFile(fb, b, 0o600); err != nil {
		t.Fatal(err)
	}
	f.loop["fallbacks"] = fb
	f.save(t)
	for _, id := range []string{"fallback-a", "disabled-a"} {
		p := filepath.Join(f.state, "targets", id, "session")
		if err := os.MkdirAll(filepath.Dir(p), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(savedExampleID), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := state.Save(f.state, state.File{ActiveTarget: "fallback-a"}); err != nil {
		t.Fatal(err)
	}
	for _, asJSON := range []bool{true, false} {
		var out bytes.Buffer
		args := []string{"status", "-c", f.config}
		if asJSON {
			args = append(args, "--json")
		}
		if run(context.Background(), args, &out, &out) != 0 {
			t.Fatal(out.String())
		}
		if asJSON {
			var s statusOut
			if json.Unmarshal(out.Bytes(), &s) != nil || s.TargetSession == nil || s.TargetSession.ID != savedExampleID {
				t.Fatal(out.String())
			}
		} else if !strings.Contains(out.String(), savedExampleID) || !strings.Contains(out.String(), "live session identity and resumability are not verified") {
			t.Fatal(out.String())
		}
	}
	_, r, _ := f.run(t, successfulDoctorRun, "--offline")
	if len(r.Sessions) != 2 || r.Sessions["primary"].State != "missing" || r.Sessions["fallback-a"].ID != savedExampleID {
		t.Fatalf("%+v", r.Sessions)
	}
	var out bytes.Buffer
	if printDoctor(env{context.Background(), &out, &out}, r, false) != 0 || !strings.Contains(out.String(), "saved session (fallback-a): "+savedExampleID+" (live identity unverified)") {
		t.Fatal(out.String())
	}
}
