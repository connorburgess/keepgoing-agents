package config

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSendAllowancePreservesOtherConfiguration(t *testing.T) {
	for _, limitSection := range []string{"", "  limits:\n    max_sends: 40 # reviewed allowance\n    max_new_threads: 2\n"} {
		t.Run(limitSection, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "loop.yaml")
			raw := []byte("version: 1\nloop:\n  name: example\n  state_dir: ./state\n  adapter: bb\n  bb:\n    thread: thr_example\n  prompt: 'fictional work'\n" + limitSection)
			if err := os.WriteFile(path, raw, 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			edit, err := cfg.PrepareSendAllowance(500)
			if err != nil {
				t.Fatal(err)
			}
			before, _ := os.ReadFile(path)
			if !bytes.Equal(before, raw) {
				t.Fatal("prepare wrote source")
			}
			if err := edit.Commit(); err != nil {
				t.Fatal(err)
			}
			next, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			if next.Loop.Limits.MaxSends != 500 || next.SourceDigest() != edit.AfterDigest() {
				t.Fatal("wrong allowance or digest")
			}
			next.Loop.Limits.MaxSends = cfg.Loop.Limits.MaxSends
			if !reflect.DeepEqual(next.Loop, cfg.Loop) {
				t.Fatal("unrelated loop settings changed")
			}
			if edit.Commit() == nil {
				t.Fatal("replay accepted")
			}
		})
	}
}

func TestSendAllowanceRefusesStaleAndIndirectSource(t *testing.T) {
	raw := "version: 1\nloop:\n  name: example\n  state_dir: ./state\n  adapter: bb\n  bb:\n    thread: thr_example\n  prompt: fictional\n  limits:\n    max_sends: 40\n"
	for _, kind := range []string{"changed", "same-bytes-replacement", "anchor", "zero", "negative", "unchanged"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "loop.yaml")
			input := raw
			if kind == "anchor" {
				input = strings.Replace(input, "fictional", "&prompt fictional", 1)
			}
			if err := os.WriteFile(path, []byte(input), 0600); err != nil {
				t.Fatal(err)
			}
			cfg, err := Load(path)
			if err != nil {
				t.Fatal(err)
			}
			limit := int64(500)
			switch kind {
			case "zero":
				limit = 0
			case "negative":
				limit = -1
			case "unchanged":
				limit = 40
			}
			edit, err := cfg.PrepareSendAllowance(limit)
			if kind == "anchor" || kind == "zero" || kind == "negative" || kind == "unchanged" {
				if err == nil {
					t.Fatal("unsafe or empty edit accepted")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if kind == "changed" {
				if err := os.WriteFile(path, []byte(raw+"# external change\n"), 0600); err != nil {
					t.Fatal(err)
				}
			} else {
				if err := os.Rename(path, path+".old"); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			before, _ := os.ReadFile(path)
			if edit.Commit() == nil {
				t.Fatal("stale source accepted")
			}
			after, _ := os.ReadFile(path)
			if !bytes.Equal(before, after) {
				t.Fatal("refusal changed source")
			}
		})
	}
}
