//go:build !windows

package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/testexec"
)

// A real child listing is about 1.5 KB per lane, so 150 lanes are well over
// doctor's 64 KiB limit. The production runners must still list them all.
func TestTransferInventoryListingLargerThanDoctorLimit(t *testing.T) {
	root := t.TempDir()
	const lanes = 150
	var listing strings.Builder
	listing.WriteString("[")
	for i := 0; i < lanes; i++ {
		if i > 0 {
			listing.WriteString(",")
		}
		fmt.Fprintf(&listing, `{"id":"thr_lane_%03d","title":"PRIVATE-%s"}`, i, strings.Repeat("x", 1500))
	}
	listing.WriteString("]")
	if listing.Len() <= 64*1024 || listing.Len() > inventoryListLimit {
		t.Fatalf("fixture size %d", listing.Len())
	}
	listFile := filepath.Join(root, "listing.json")
	if err := os.WriteFile(listFile, []byte(listing.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	workdir := filepath.Join(root, "workdir")
	if err := os.MkdirAll(workdir, 0o700); err != nil {
		t.Fatal(err)
	}
	bb := filepath.Join(root, "bb")
	testexec.Script(t, bb, `#!/bin/sh
case "$2" in
list) cat '`+listFile+`' ;;
show) printf '{"thread":{"id":"%s","projectId":"proj_example","environmentId":"env_example","providerId":"codex","status":"idle","archivedAt":null,"deletedAt":null},"environment":{"id":"env_example","projectId":"proj_example","hostId":"host_example","path":"%s"}}' "$3" '`+workdir+`' ;;
*) exit 2 ;;
esac
`)
	config := filepath.Join(root, "loop.yaml")
	raw := "version: 1\nloop:\n  name: example-loop\n  state_dir: ./state\n  adapter: bb\n  provider: codex\n  bb:\n    thread: thr_target\n    cli: " + bb + "\n  prompt: Continue the fictional loop.\n"
	if err := os.WriteFile(config, []byte(raw), 0o600); err != nil {
		t.Fatal(err)
	}
	decode := func(code int, out, errs *bytes.Buffer) (int, transferInventory) {
		var r transferInventory
		if err := json.Unmarshal(out.Bytes(), &r); err != nil {
			t.Fatalf("no report (%d): %s", code, errs.String())
		}
		if strings.Contains(out.String(), "PRIVATE-") {
			t.Fatal("title exposed")
		}
		return code, r
	}
	args := []string{"inventory", "-c", config, "--json", "--timeout", "20s"}
	// Through the command's real wiring, as `keepgoing transfer inventory`
	// runs it: the listing must use the larger limit there.
	var out, errs bytes.Buffer
	code, r := decode(run(context.Background(), append([]string{"transfer"}, args...), &out, &errs), &out, &errs)
	if code != exitOK || !r.Complete || !r.LanesListed || len(r.Lanes) != lanes {
		t.Fatalf("%d complete=%t listed=%t lanes=%d notes=%v", code, r.Complete, r.LanesListed, len(r.Lanes), r.Notes)
	}
	// Doctor's limit would have lost the whole listing.
	out.Reset()
	errs.Reset()
	code, r = decode(transferInventoryCommand(env{ctx: context.Background(), stdout: &out, stderr: &errs}, args, doctorExec, doctorExec), &out, &errs)
	if code != exitFailed || r.LanesListed || len(r.Lanes) != 0 {
		t.Fatalf("64 KiB runner listed lanes: %d %d", code, len(r.Lanes))
	}
}
