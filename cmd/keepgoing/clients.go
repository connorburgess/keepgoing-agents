package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/registry"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// A local program that runs this CLI, such as the BB plugin or the macOS
// menu, may say which version of itself it is with --client NAME@VERSION on
// the read commands it calls (status, client status, panel list). The CLI then
// records, in the panel's private directory, that client's name and version,
// this CLI's own build label, and when it was seen, so the panel's Versions
// view can say which builds they run. No path is recorded. Nothing is recorded
// without the flag, and the directory is never created for it: a panel
// directory that does not exist yet gets no report. Recording is best effort:
// it never changes the command's output or exit code, and a record busy with
// another writer is skipped.

const (
	clientsName  = "clients.json"
	clientsLock  = "clients"
	maxClients   = 8
	maxClientsSz = 16 << 10
	// clientRewrite is how old an unchanged report may be before the next
	// one rewrites it; the plugin and the menu ask often.
	clientRewrite = time.Hour
	// clientFresh is how recent a report must be to count: an older one is
	// shown as not seen since, and counts for nothing.
	clientFresh = 7 * 24 * time.Hour
)

// Known client names. A name carries no product name, so it survives the
// tool's rename.
var clientNames = map[string]bool{"bb-plugin": true, "macos-menu": true}

var clientVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,31}$`)

type clientsFile struct {
	Version int            `json:"version"`
	Clients []clientReport `json:"clients"`
}

// clientReport is one client's last report: one per client name.
type clientReport struct {
	Name          string    `json:"name"`
	ClientVersion string    `json:"clientVersion"`
	CLIVersion    string    `json:"cliVersion"`
	SeenAt        time.Time `json:"seenAt"`
}

// parseClientFlag reads NAME@VERSION. ok is false for an unknown name or a
// version that is not a short plain label.
func parseClientFlag(v string) (name, ver string, ok bool) {
	i := strings.LastIndexByte(v, '@')
	if i <= 0 {
		return "", "", false
	}
	name, ver = v[:i], v[i+1:]
	if !clientNames[name] || !clientVersionPattern.MatchString(ver) {
		return "", "", false
	}
	return name, ver, true
}

// recordClient records one report in the panel's directory, which must
// already exist and be private. It returns no error a caller should show:
// the command it rides on has already answered.
func recordClient(panelDir, name, ver string, now time.Time) {
	dir := panelClientsDir(panelDir)
	if dir == "" {
		return
	}
	before, err := os.Lstat(dir)
	if err != nil || !before.IsDir() || registry.CheckDirectory(dir) != nil {
		return
	}
	l, err := lock.AcquireNamed(dir, clientsLock)
	if err != nil {
		return
	}
	defer l.Release()
	// Taking the lock creates a missing directory. One removed since the
	// check above was recreated by it: undo that and record nothing.
	if after, err := os.Lstat(dir); err != nil || !os.SameFile(before, after) {
		l.Release()
		_ = os.Remove(filepath.Join(dir, clientsLock+".lock"))
		_ = os.Remove(dir)
		return
	}
	f, err := loadClients(dir)
	if err != nil || f == nil {
		// An unreadable record is replaced, not repaired: it holds nothing a
		// decision depends on.
		f = &clientsFile{Version: 1}
	}
	r := clientReport{Name: name, ClientVersion: ver, CLIVersion: dockBuildLabel(version), SeenAt: now.UTC()}
	kept := []clientReport{r}
	for _, c := range f.Clients {
		if c.Name != name {
			kept = append(kept, c)
		} else if c.ClientVersion == r.ClientVersion && c.CLIVersion == r.CLIVersion && now.Sub(c.SeenAt) >= 0 && now.Sub(c.SeenAt) < clientRewrite {
			return // unchanged and recent: nothing to write
		}
	}
	sort.SliceStable(kept, func(i, j int) bool { return kept[i].SeenAt.After(kept[j].SeenAt) })
	if len(kept) > maxClients {
		kept = kept[:maxClients]
	}
	f.Version, f.Clients = 1, kept
	b, err := json.Marshal(f)
	if err != nil {
		return
	}
	_ = control.WritePrivateFile(filepath.Join(dir, clientsName), b)
}

// noteClient records a --client value given to a read command. A value it
// cannot use is ignored, so a client never breaks the command it calls.
func noteClient(value, panelDir string) {
	if value == "" {
		return
	}
	if name, ver, ok := parseClientFlag(value); ok {
		recordClient(panelDir, name, ver, time.Now())
	}
}

// panelClientsDir is the panel directory the reports are read from, resolved
// as panelDirectory does but never created: a read creates nothing. Empty
// when it cannot be resolved.
func panelClientsDir(path string) string {
	if path != "" {
		return path
	}
	base, err := panelStateBase()
	if err != nil {
		return ""
	}
	if other := panelRunningUnderOtherName(base); other != "" {
		return other
	}
	return filepath.Join(base, panelDirName(), "panel")
}

var errClients = errors.New("client reports unavailable")

// loadClients reads the record: nil with no error when there is none.
func loadClients(dir string) (*clientsFile, error) {
	path := filepath.Join(dir, clientsName)
	info, err := snapshot.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil || !info.Mode().IsRegular() || info.Size() > maxClientsSz {
		return nil, errClients
	}
	b, err := snapshot.ReadFile(path)
	if err != nil || len(b) > maxClientsSz {
		return nil, errClients
	}
	var f clientsFile
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || d.Decode(new(any)) != io.EOF || f.Version != 1 || len(f.Clients) > maxClients {
		return nil, errClients
	}
	seen := map[string]bool{}
	for _, c := range f.Clients {
		if !clientNames[c.Name] || seen[c.Name] || !clientVersionPattern.MatchString(c.ClientVersion) || dockBuildLabel(c.CLIVersion) != c.CLIVersion || c.SeenAt.IsZero() {
			return nil, errClients
		}
		seen[c.Name] = true
	}
	return &f, nil
}
