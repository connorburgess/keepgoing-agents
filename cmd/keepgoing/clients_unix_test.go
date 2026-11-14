//go:build unix

package main

import (
	"bytes"
	"context"
	"testing"
)

// status and client status record a --client report and print exactly what
// they print without it; no other command takes the flag.
func TestClientFlagOnReadCommandsOnly(t *testing.T) {
	base := panelShortDir(t)
	previous := panelStateBase
	panelStateBase = func() (string, error) { return base, nil }
	t.Cleanup(func() { panelStateBase = previous })
	f := newFixture(t)
	f.run("enable")
	code, plain, _ := f.run("status", "--json")
	codeWith, with, _ := f.run("status", "--json", "--client", "bb-plugin@0.3.1")
	if code != codeWith {
		t.Fatalf("exit %d without the flag, %d with it", code, codeWith)
	}
	if len(plain) == 0 || len(with) != len(plain) {
		t.Errorf("the flag changed the output: %d bytes, then %d", len(plain), len(with))
	}
	dir := panelClientsDir("")
	clients, err := loadClients(dir)
	if err != nil || clients == nil || len(clients.Clients) != 1 || clients.Clients[0].Name != "bb-plugin" || clients.Clients[0].ClientVersion != "0.3.1" {
		t.Fatalf("%+v %v", clients, err)
	}
	// An unusable value is ignored, and the command still answers.
	if c, _, _ := f.run("status", "--json", "--client", "bb-plugin@not a version"); c != code {
		t.Errorf("exit %d", c)
	}
	if c, _, errs := f.run("pause", "--client", "bb-plugin@0.3.1"); c != exitUsage || errs == "" {
		t.Errorf("pause took --client: %d %s", c, errs)
	}
	// client status records the menu's report the same way.
	regDir, entry := clientFixture(t)
	var out, errs bytes.Buffer
	if c := run(context.Background(), []string{"client", "status", "--directory", regDir, "--id", entry.ID, "--json", "--offline", "--client", "macos-menu@0.1.0"}, &out, &errs); c != 0 || errs.Len() != 0 {
		t.Fatalf("%d %s %s", c, out.String(), errs.String())
	}
	clients, _ = loadClients(dir)
	if clients == nil || len(clients.Clients) != 2 || clients.Clients[0].Name != "macos-menu" || clients.Clients[0].ClientVersion != "0.1.0" {
		t.Fatalf("%+v", clients)
	}
}
