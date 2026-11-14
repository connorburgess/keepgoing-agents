package config

import (
	"errors"
	"os"
)

// The name of a loop's configuration file: FileName for new loops, and
// LegacyFileName, the name from before the tool was renamed, which is still
// read.
const (
	FileName       = "looptender.yaml"
	LegacyFileName = "keepgoing.yaml"
)

// ErrDefaultAmbiguous is returned by Load for the path DefaultPath gives
// when both names are in the working directory: which loop was meant
// cannot be told, so neither is read.
var ErrDefaultAmbiguous = errors.New("both " + FileName + " and " + LegacyFileName + " are in this directory; choose one with -c")

// ambiguousDefault stands for "both names are here"; no file is ever given
// this name.
const ambiguousDefault = FileName + "|" + LegacyFileName

// DefaultPath is the configuration a command reads when it is given no -c:
// looptender.yaml in the working directory, or keepgoing.yaml when only that
// is there. When both are there it is a path that Load refuses with
// ErrDefaultAmbiguous. When neither is, it is looptender.yaml, so the error
// names the file a new loop would have.
func DefaultPath() string {
	current, legacy := present(FileName), present(LegacyFileName)
	switch {
	case current && legacy:
		return ambiguousDefault
	case legacy:
		return LegacyFileName
	}
	return FileName
}

func present(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}
