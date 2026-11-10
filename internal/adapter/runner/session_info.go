package runner

import (
	"errors"
	"io"
	"os"
	"regexp"
	"strings"
)

var sessionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}$`)

// InspectSession reads only the saved resume identifier, without observing a
// turn, reading its output, or changing state. Empty means no record exists.
// Only UUID-shaped identifiers are exposed by diagnostics; unknown formats
// remain usable by the runner's existing Session method but are not displayed.
// A saved ID does not establish a live process or a resumable provider session.
func (d Dir) InspectSession() (string, error) {
	p := d.path("session")
	info, err := os.Lstat(p)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", errors.New("saved session metadata is inaccessible")
	}
	if !info.Mode().IsRegular() || info.Size() > 128 {
		return "", errors.New("saved session metadata has an unsupported format")
	}
	f, err := os.Open(p)
	if err != nil {
		return "", errors.New("saved session metadata is inaccessible")
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, 129))
	if err != nil || len(b) > 128 {
		return "", errors.New("saved session metadata could not be read within its limit")
	}
	id := strings.TrimSpace(string(b))
	if !sessionIDPattern.MatchString(id) {
		return "", errors.New("saved session identifier has an unrecognized format")
	}
	return id, nil
}
