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

// ErrDefaultAmbiguous is DefaultPath's answer when both names are in the
// working directory as two different files: which loop was meant cannot be
// told, so neither is read.
var ErrDefaultAmbiguous = errors.New("both " + FileName + " and " + LegacyFileName + " are in this directory; choose one with -c")

// DefaultHelp describes the default in a -c flag's help.
const DefaultHelp = "default ./" + FileName + ", or ./" + LegacyFileName + " when only that is there"

// DefaultPath is the configuration a command reads when it is given no -c:
// looptender.yaml in the working directory, or keepgoing.yaml when only that
// is there. When both are there and are one file (one a link to the other,
// symbolic or hard), that file is meant, by its current name. When both are
// there as two files it is ErrDefaultAmbiguous. When neither is, it is
// looptender.yaml, so the error a command then gives names the file a new
// loop would have. It reads the directory only.
func DefaultPath() (string, error) {
	current, legacy := present(FileName), present(LegacyFileName)
	switch {
	case current && legacy:
		a, aerr := os.Stat(FileName)
		b, berr := os.Stat(LegacyFileName)
		if aerr == nil && berr == nil && os.SameFile(a, b) {
			return FileName, nil
		}
		return "", ErrDefaultAmbiguous
	case legacy:
		return LegacyFileName, nil
	}
	return FileName, nil
}

func present(name string) bool {
	_, err := os.Lstat(name)
	return err == nil
}
