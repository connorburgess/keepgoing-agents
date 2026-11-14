package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

const configNameLoop = "version: 1\nloop:\n  name: example-loop\n  state_dir: ./state\n  prompt: Continue the fictional task.\n  adapter: process\n  provider: example\n  group: example-main\n  process:\n    status_command: [\"/bin/sh\", \"-c\", \"true\"]\n    resume_command: [\"/bin/sh\", \"-c\", \"true\"]\n"

// With no -c, commands read looptender.yaml in the working directory, or
// keepgoing.yaml when only that is there; with both there they read
// neither and say why.
func TestCommandsReadTheDefaultConfigurationUnderEitherName(t *testing.T) {
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)
	validate := func() (int, string) {
		t.Helper()
		var out, errOut bytes.Buffer
		code := run(context.Background(), []string{"validate"}, &out, &errOut)
		return code, out.String() + errOut.String()
	}
	if code, text := validate(); code == exitOK || !strings.Contains(text, config.FileName) {
		t.Fatalf("neither: %d %s", code, text)
	}
	if err := os.WriteFile(config.LegacyFileName, []byte(configNameLoop), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, text := validate(); code != exitOK || !strings.Contains(text, "ok: "+config.LegacyFileName) {
		t.Fatalf("the earlier name only: %d %s", code, text)
	}
	if err := os.WriteFile(config.FileName, []byte(configNameLoop), 0o600); err != nil {
		t.Fatal(err)
	}
	if code, text := validate(); code == exitOK || !strings.Contains(text, "both looptender.yaml and keepgoing.yaml are in this directory; choose one with -c") {
		t.Fatalf("both: %d %s", code, text)
	}
	var out bytes.Buffer
	run(context.Background(), []string{"doctor", "--json"}, &out, &bytes.Buffer{})
	var v struct {
		Checks []doctorCheck `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil || len(v.Checks) == 0 || v.Checks[0].ID != "config" || !strings.Contains(v.Checks[0].Message, "both looptender.yaml and keepgoing.yaml") || v.Checks[0].Fix != "Pass -c with the one you mean." {
		t.Fatalf("doctor: %v %s", err, out.String())
	}
	// -c chooses, as always.
	var errOut bytes.Buffer
	if code := run(context.Background(), []string{"validate", "-c", config.LegacyFileName}, &bytes.Buffer{}, &errOut); code != exitOK {
		t.Fatalf("-c: %d %s", code, errOut.String())
	}
	if err := os.Remove(config.LegacyFileName); err != nil {
		t.Fatal(err)
	}
	if code, text := validate(); code != exitOK || !strings.Contains(text, "ok: "+config.FileName) {
		t.Fatalf("the current name only: %d %s", code, text)
	}
}

// init writes looptender.yaml, and nothing under the earlier name.
func TestInitWritesTheConfigurationUnderTheCurrentName(t *testing.T) {
	f := newSetupFixture(t)
	if code, _, errOut := f.run(t, f.args(), ""); code != 0 {
		t.Fatalf("%d %s", code, errOut)
	}
	if _, err := os.Stat(filepath.Join(f.destination, config.FileName)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(f.destination, config.LegacyFileName)); err == nil {
		t.Fatal("a keepgoing.yaml was written")
	}
}
