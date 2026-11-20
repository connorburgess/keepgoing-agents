package state

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func laneKey(i int) string { return fmt.Sprintf("%024x", i) }

// The lane-titles file: cleaned titles, bounded, private, read strictly.
func TestLaneTitlesFile(t *testing.T) {
	dir := t.TempDir()
	if f, err := LoadLaneTitles(dir); f != nil || err != nil {
		t.Fatalf("missing file: %+v %v", f, err)
	}
	at := time.Date(2030, 3, 4, 12, 0, 0, 0, time.UTC)
	titles := map[string]string{
		laneKey(1):  "  Fix   the login\tform  ",
		laneKey(2):  "Bell\u0007 and\x1b[31m escape‮ reversed line",
		laneKey(3):  "   ",
		"not-a-key": "dropped",
		laneKey(4):  strings.Repeat("é", 300),
		laneKey(5):  "bad \xff utf8",
	}
	for i := 10; i < 10+MaxLaneTitles+5; i++ {
		titles[laneKey(i)] = fmt.Sprintf("lane %d", i)
	}
	if err := SaveLaneTitles(dir, at, titles); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(dir, LaneTitlesName))
	if err != nil || info.Mode().Perm() != 0o600 {
		t.Fatalf("%v %v", info, err)
	}
	f, err := LoadLaneTitles(dir)
	if err != nil || !f.CheckedAt.Equal(at) || len(f.Titles) != MaxLaneTitles {
		t.Fatalf("%v %d", err, len(f.Titles))
	}
	got := map[string]string{}
	for _, x := range f.Titles {
		got[x.Key] = x.Title
	}
	if got[laneKey(1)] != "Fix the login form" || got[laneKey(2)] != "Bell and[31m escape reversed line" {
		t.Errorf("%q %q", got[laneKey(1)], got[laneKey(2)])
	}
	if _, ok := got[laneKey(3)]; ok {
		t.Error("an empty title was kept")
	}
	if _, ok := got["not-a-key"]; ok {
		t.Error("a malformed key was kept")
	}
	if r := []rune(got[laneKey(4)]); len(r) != MaxLaneTitle || r[len(r)-1] != '…' {
		t.Errorf("%d runes", len(r))
	}
	if got[laneKey(5)] != "bad  utf8" && got[laneKey(5)] != "bad utf8" {
		t.Errorf("%q", got[laneKey(5)])
	}
	// Strict reading: anything else in the file makes it unusable, not partly read.
	for name, body := range map[string]string{
		"unknown field":   `{"version":1,"checkedAt":"2030-03-04T12:00:00Z","titles":[],"extra":1}`,
		"other version":   `{"version":2,"checkedAt":"2030-03-04T12:00:00Z","titles":[]}`,
		"no time":         `{"version":1,"titles":[]}`,
		"bad key":         `{"version":1,"checkedAt":"2030-03-04T12:00:00Z","titles":[{"key":"x","title":"t"}]}`,
		"uncleaned title": `{"version":1,"checkedAt":"2030-03-04T12:00:00Z","titles":[{"key":"` + laneKey(1) + `","title":"a\u0007b"}]}`,
		"trailing data":   `{"version":1,"checkedAt":"2030-03-04T12:00:00Z","titles":[]} {}`,
		"oversized":       `{"version":1,"checkedAt":"2030-03-04T12:00:00Z","titles":[]}` + strings.Repeat(" ", maxLaneBytes),
	} {
		if err := os.WriteFile(filepath.Join(dir, LaneTitlesName), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		if f, err := LoadLaneTitles(dir); f != nil || err != ErrLaneTitles {
			t.Errorf("%s: %+v %v", name, f, err)
		}
	}
}
