//go:build unix

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
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
	dir := panelClientsDir("")
	// No panel directory yet: the flag records nothing and creates nothing.
	f.run("status", "--json", "--client", "bb-plugin@0.3.1")
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("status --client created the panel directory: %v", err)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	code, plain, _ := f.run("status", "--json")
	codeWith, with, _ := f.run("status", "--json", "--client", "bb-plugin@0.3.1")
	if code != codeWith {
		t.Fatalf("exit %d without the flag, %d with it", code, codeWith)
	}
	if len(plain) == 0 || sameReply(plain) != sameReply(with) {
		t.Errorf("the flag changed the output:\n%s\n%s", plain, with)
	}
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
	// panel list, the read the menu makes every minute, takes it too, and
	// prints the same with and without it.
	list := func(extra ...string) (int, string) {
		var out, errs bytes.Buffer
		c := run(context.Background(), append([]string{"panel", "list", "--json", "--offline", "--directory", regDir}, extra...), &out, &errs)
		return c, out.String()
	}
	c1, o1 := list()
	c2, o2 := list("--client", "macos-menu@0.2.0")
	if c1 != c2 || len(o1) == 0 || sameReply(o1) != sameReply(o2) {
		t.Errorf("panel list changed with the flag: %d %d\n%s\n%s", c1, c2, o1, o2)
	}
	clients, _ = loadClients(dir)
	if clients == nil || clients.Clients[0].Name != "macos-menu" || clients.Clients[0].ClientVersion != "0.2.0" {
		t.Fatalf("%+v", clients)
	}
}

// sameReply is a JSON reply without the time it was read, so two reads of
// the same records compare equal.
func sameReply(s string) string {
	var m map[string]any
	if json.Unmarshal([]byte(s), &m) != nil {
		return "unparsed: " + s
	}
	delete(m, "readAt")
	b, _ := json.Marshal(m)
	return string(b)
}
