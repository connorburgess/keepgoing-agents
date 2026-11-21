package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/telegram"
)

func TestDoctorTelegramLocalEvidence(t *testing.T) {
	for _, kind := range []string{"missing", "paired", "corrupt", "token_directory"} {
		t.Run(kind, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "private")
			before := map[string]string{}
			if kind != "missing" {
				if err := os.Mkdir(dir, 0700); err != nil {
					t.Fatal(err)
				}
				if kind == "corrupt" {
					if err := os.WriteFile(filepath.Join(dir, telegram.PairingFile), []byte("private malformed example"), 0600); err != nil {
						t.Fatal(err)
					}
				} else {
					now := time.Now().UTC()
					if err := telegram.SavePairing(dir, telegram.PairingRecord{SchemaVersion: 1, Mode: telegram.ModeAlerts, BotID: 100001, BotUsername: "example_private_bot", AllowedUserID: 7001, AllowedChatID: 7001, PairedAt: now, UpdatedAt: now}); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "paired" {
					if err := os.WriteFile(filepath.Join(dir, telegram.TokenFile), []byte("not even a valid token: private-secret"), 0600); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "token_directory" {
					if err := os.Mkdir(filepath.Join(dir, telegram.TokenFile), 0700); err != nil {
						t.Fatal(err)
					}
				}
			}
			snapshot := func() map[string]string {
				m := map[string]string{}
				_ = filepath.WalkDir(dir, func(p string, d os.DirEntry, err error) error {
					if err != nil {
						return nil
					}
					if d.IsDir() {
						m[p] = "directory"
					} else {
						b, e := os.ReadFile(p)
						if e != nil {
							t.Fatal(e)
						}
						m[p] = string(b)
					}
					return nil
				})
				return m
			}
			before = snapshot()
			r := doctorReport{}
			doctorTelegram(&r, dir)
			if !reflect.DeepEqual(before, snapshot()) {
				t.Fatal("diagnostic changed private state")
			}
			b, _ := json.Marshal(r)
			for _, secret := range []string{dir, "example_private_bot", "7001", "private-secret", "private malformed"} {
				if strings.Contains(string(b), secret) {
					t.Fatalf("private value leaked: %q", secret)
				}
			}
			want := "ok"
			if kind == "missing" {
				want = "warning"
			}
			if kind == "corrupt" {
				want = "unavailable"
			}
			if len(r.Checks) != 3 || r.Checks[0].Status != want || r.Checks[2].Status != "unavailable" {
				t.Fatalf("checks: %+v", r.Checks)
			}
			token := "warning"
			if kind == "paired" {
				token = "ok"
			}
			if r.Checks[1].Status != token {
				t.Fatalf("token metadata: %+v", r.Checks[1])
			}
		})
	}
}

func TestDoctorTelegramExplicitSelection(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "absent")
	for _, enabled := range []bool{false, true} {
		var stdout, stderr bytes.Buffer
		args := []string{"-c", filepath.Join(t.TempDir(), "missing.yaml"), "--offline", "--json"}
		if enabled {
			args = append(args, "--telegram", "--telegram-dir", dir)
		}
		doctor(env{ctx: context.Background(), stdout: &stdout, stderr: &stderr}, args, func(context.Context, string, []string, map[string]string, string) ([]byte, error) {
			t.Fatal("executed CLI")
			return nil, nil
		})
		if strings.Contains(stdout.String(), "telegram.pairing") != enabled {
			t.Fatalf("opt-in mismatch: %s", stdout.String())
		}
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatal("created missing pairing directory")
	}
	var stdout, stderr bytes.Buffer
	if code := doctor(env{ctx: context.Background(), stdout: &stdout, stderr: &stderr}, []string{"--telegram-dir", dir}, nil); code != exitUsage {
		t.Fatalf("unselected directory accepted: %d", code)
	}
}
