package main

import (
	"errors"
	"flag"

	"github.com/connorburgess/keepgoing-agents/internal/config"
)

// configFlagHelp is the help of a -c flag that falls back to the default.
func configFlagHelp(what string) string { return what + " (" + config.DefaultHelp + ")" }

// errEmptyConfig refuses -c given an empty value, as from an unset shell
// variable: that is not the same as leaving -c out, and reading whatever
// configuration is in the working directory could act on another loop.
var errEmptyConfig = errors.New("-c was given an empty path; name the configuration file, or leave -c out to use the one in this directory")

// configPath is the configuration path a command reads: the -c value when
// -c was given, or the default when it was not. -c given empty is refused.
func configPath(fs *flag.FlagSet, given string) (string, error) {
	set := false
	fs.Visit(func(f *flag.Flag) { set = set || f.Name == "c" })
	switch {
	case set && given == "":
		return "", errEmptyConfig
	case set:
		return given, nil
	}
	return config.DefaultPath()
}

// configDefault puts the configuration path in *path (see configPath),
// before anything resolves or loads it. When it cannot, it says so as
// name's error and returns false; the caller then stops with exitUsage.
func configDefault(e env, name string, fs *flag.FlagSet, path *string) bool {
	p, err := configPath(fs, *path)
	if err != nil {
		e.errf("%s: %v", name, err)
		return false
	}
	*path = p
	return true
}
