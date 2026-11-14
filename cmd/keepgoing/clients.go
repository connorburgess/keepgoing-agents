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
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/control"
	"github.com/connorburgess/keepgoing-agents/internal/lock"
	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// A local program that runs this CLI, such as the BB plugin or the macOS
// menu, may say which version of itself it is with --client NAME@VERSION on
// the read commands it calls (status, client status). The CLI then records,
// in the panel's private directory, that client's name and version, this
// executable's path and build label, and when it was seen, so the panel's
// Versions view can say which build each of them runs. Nothing is recorded
// without the flag. Recording is best effort: it never changes the command's
// output or exit code, and a record busy with another writer is skipped.

const (
	clientsName  = "clients.json"
	clientsLock  = "clients"
	maxClients   = 8
	maxClientsSz = 16 << 10
)

// Known client names. A name carries no product name, so it survives the
// tool's rename.
var clientNames = map[string]bool{"bb-plugin": true, "macos-menu": true}

var clientVersionPattern = regexp.MustCompile(`^[0-9A-Za-z][0-9A-Za-z.+-]{0,31}$`)

type clientsFile struct {
	Version int            `json:"version"`
	Clients []clientReport `json:"clients"`
}

// clientReport is one client's last report.
type clientReport struct {
	Name          string    `json:"name"`
	ClientVersion string    `json:"clientVersion"`
	CLIPath       string    `json:"cliPath"`
	CLIVersion    string    `json:"cliVersion"`
	SeenAt        time.Time `json:"seenAt"`
}

// parseClientFlag reads NAME@VERSION. ok is false for an unknown name or a
// version that is not a short plain label.
func parseClientFlag(v string) (name, ver string, ok bool) {
	i := bytes.LastIndexByte([]byte(v), '@')
	if i <= 0 {
		return "", "", false
	}
	name, ver = v[:i], v[i+1:]
	if !clientNames[name] || !clientVersionPattern.MatchString(ver) {
		return "", "", false
	}
	return name, ver, true
}

// recordClient records one report in the panel's directory. It returns no
// error a caller should show: the command it rides on has already answered.
func recordClient(panelDir, name, ver string, now time.Time) {
	exe, err := os.Executable()
	if err != nil {
		return
	}
	if resolved, err := filepath.EvalSymlinks(exe); err == nil {
		exe = resolved
	}
	dir, err := panelDirectory(panelDir)
	if err != nil {
		return
	}
	l, err := lock.AcquireNamed(dir, clientsLock)
	if err != nil {
		return
	}
	defer l.Release()
	f, err := loadClients(dir)
	if err != nil {
		// An unreadable record is replaced, not repaired: it holds nothing a
		// decision depends on.
		f = &clientsFile{Version: 1}
	}
	if f == nil {
		f = &clientsFile{Version: 1}
	}
	r := clientReport{Name: name, ClientVersion: ver, CLIPath: exe, CLIVersion: dockBuildLabel(version), SeenAt: now.UTC()}
	kept := []clientReport{r}
	for _, c := range f.Clients {
		if c.Name != name || c.CLIPath != exe {
			kept = append(kept, c)
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
	info, err := os.Lstat(path)
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
	for _, c := range f.Clients {
		if !clientNames[c.Name] || !clientVersionPattern.MatchString(c.ClientVersion) || !filepath.IsAbs(c.CLIPath) || c.SeenAt.IsZero() {
			return nil, errClients
		}
	}
	return &f, nil
}
