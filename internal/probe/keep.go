package probe

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// keepFailures is how many failed-probe captures are kept per candidate.
const keepFailures = 5

var unsafeName = regexp.MustCompile(`[^a-z0-9-]`)

// KeepFailure saves a failed probe's raw output under
// <stateDir>/probe-failures/<id>/ so an error shape keepgoing does not yet
// recognise, such as a provider's usage-limit event, can be mapped later.
// Files are owner-only and only the newest few are kept. Nothing reads them
// to make a decision. Passing results are not saved.
func KeepFailure(stateDir, id string, r Result) error {
	if r.OK {
		return nil
	}
	dir := filepath.Join(stateDir, "probe-failures", unsafeName.ReplaceAllString(strings.ToLower(id), "-"))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	name := r.At.UTC().Format("20060102T150405Z") + "-" + string(r.Tier) + ".out"
	header := fmt.Sprintf("# keepgoing probe failure\n# candidate: %s\n# tier: %s\n# kind: %s\n# model: %s\n# exit: %d\n# class: %s\n# detail: %s\n\n",
		id, r.Tier, r.Kind, r.Model, r.ExitCode, r.ErrorClass, r.Detail)
	if err := os.WriteFile(filepath.Join(dir, name), append([]byte(header), r.Raw...), 0o600); err != nil {
		return err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return err
	}
	var names []string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".out") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names) // timestamp prefix sorts oldest first
	for len(names) > keepFailures {
		os.Remove(filepath.Join(dir, names[0]))
		names = names[1:]
	}
	return nil
}
