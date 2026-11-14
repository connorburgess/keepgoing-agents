package state

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
	"unicode"
	"unicode/utf8"

	"github.com/connorburgess/keepgoing-agents/internal/snapshot"
)

// LaneTitlesName is the private file of lane titles: the title BB reported
// for each direct child of the newest verified listing, by the child's hashed
// key. It is for display only. No decision reads it, and nothing in the run
// graph, state.json, the incident history, a report or the timeline carries
// a title. Removing it changes nothing but what the panel can show.
const LaneTitlesName = "lane-titles.json"

const (
	// MaxLaneTitles bounds the entries kept; MaxLaneTitle the runes of each.
	MaxLaneTitles = 64
	MaxLaneTitle  = 120
	maxLaneBytes  = 64 << 10
)

// ErrLaneTitles means the file is present but cannot be used.
var ErrLaneTitles = errors.New("lane titles unavailable")

var laneKeyPattern = regexp.MustCompile(`^[0-9a-f]{24}$`)

// LaneTitles is the file's content. CheckedAt is the check that read them,
// the same time the run graph records for that listing.
type LaneTitles struct {
	Version   int         `json:"version"`
	CheckedAt time.Time   `json:"checkedAt"`
	Titles    []LaneTitle `json:"titles"`
}

type LaneTitle struct {
	Key   string `json:"key"`
	Title string `json:"title"`
}

// CleanLaneTitle removes control characters and invalid text, trims it and
// cuts it to MaxLaneTitle runes. "" means no title.
func CleanLaneTitle(s string) string {
	s = strings.ToValidUTF8(s, "")
	s = strings.Map(func(r rune) rune {
		switch {
		case unicode.IsSpace(r):
			return ' ' // a tab or line break separates words
		case unicode.IsControl(r) || unicode.Is(unicode.Bidi_Control, r):
			return -1
		}
		return r
	}, s)
	s = strings.Join(strings.Fields(s), " ")
	if utf8.RuneCountInString(s) > MaxLaneTitle {
		r := []rune(s)
		s = strings.TrimSpace(string(r[:MaxLaneTitle-1])) + "…"
	}
	return s
}

// SaveLaneTitles replaces the file with the titles of one verified listing.
// Keys are the children's hashed keys; titles are cleaned, empty ones are
// dropped, and at most MaxLaneTitles are kept, in key order.
func SaveLaneTitles(dir string, checkedAt time.Time, titles map[string]string) error {
	f := LaneTitles{Version: 1, CheckedAt: checkedAt.UTC(), Titles: []LaneTitle{}}
	keys := make([]string, 0, len(titles))
	for k := range titles {
		if laneKeyPattern.MatchString(k) {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)
	for _, k := range keys {
		if len(f.Titles) >= MaxLaneTitles {
			break
		}
		if t := CleanLaneTitle(titles[k]); t != "" {
			f.Titles = append(f.Titles, LaneTitle{Key: k, Title: t})
		}
	}
	b, err := json.Marshal(f)
	if err != nil {
		return ErrLaneTitles
	}
	return saveAllowance(dir, LaneTitlesName, b)
}

// LoadLaneTitles reads the file. A missing file is nil with no error; one
// that is oversized, malformed, of another version, or carries a bad key or
// an uncleaned title is ErrLaneTitles.
func LoadLaneTitles(dir string) (*LaneTitles, error) {
	path := filepath.Join(dir, LaneTitlesName)
	if info, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil, nil
	} else if err != nil || !info.Mode().IsRegular() || info.Size() > maxLaneBytes {
		return nil, ErrLaneTitles
	}
	b, err := snapshot.ReadFile(path)
	if err != nil || len(b) > maxLaneBytes {
		return nil, ErrLaneTitles
	}
	var f LaneTitles
	d := json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(&f) != nil || d.Decode(new(any)) != io.EOF || f.Version != 1 || f.CheckedAt.IsZero() || f.Titles == nil || len(f.Titles) > MaxLaneTitles {
		return nil, ErrLaneTitles
	}
	for _, t := range f.Titles {
		if !laneKeyPattern.MatchString(t.Key) || t.Title == "" || CleanLaneTitle(t.Title) != t.Title {
			return nil, ErrLaneTitles
		}
	}
	return &f, nil
}
