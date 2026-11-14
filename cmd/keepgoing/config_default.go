package main

import "github.com/connorburgess/keepgoing-agents/internal/config"

// configFlagHelp is the help of a -c flag that falls back to the default.
func configFlagHelp(what string) string { return what + " (" + config.DefaultHelp + ")" }

// configDefault fills in the default configuration path when -c was not
// given, before anything resolves or loads it. When both names are in the
// working directory as two files it says so, as name's error, and returns
// false; the caller then stops with exitUsage.
func configDefault(e env, name string, path *string) bool {
	if *path != "" {
		return true
	}
	p, err := config.DefaultPath()
	if err != nil {
		e.errf("%s: %v", name, err)
		return false
	}
	*path = p
	return true
}
