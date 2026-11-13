package config

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// ACPAdapter is the adapter name of an agent reached over the Agent Client
// Protocol.
const ACPAdapter = "acp"

// ACP configures an agent that speaks the Agent Client Protocol (ACP v1)
// over its standard streams, such as Hermes Agent's `hermes acp`. keepgoing
// starts the command once per turn, sends the prompt over the protocol and
// reads the turn's typed end. It never grants a permission request.
type ACP struct {
	// Name labels the agent. It is the provider when none is set.
	Name string `yaml:"name"`
	// Command is the agent's ACP server command, as argv. No shell is used.
	Command []string          `yaml:"command"`
	Workdir string            `yaml:"workdir"`
	Env     map[string]string `yaml:"env,omitempty"`
	// HandshakeTimeout bounds initialization and session setup; ExitTimeout
	// bounds the wait for the agent to exit after its turn.
	HandshakeTimeout Duration `yaml:"handshake_timeout,omitempty"`
	ExitTimeout      Duration `yaml:"exit_timeout,omitempty"`
}

// MaxACPTimeout is the most either ACP timeout may be.
const MaxACPTimeout = 10 * time.Minute

var acpName = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

func (a *ACP) resolvePaths(base string) {
	a.Workdir = resolve(base, a.Workdir)
	resolveArgv(base, a.Command)
}

func (a ACP) validate(add func(format string, args ...any)) {
	if !acpName.MatchString(a.Name) {
		add("acp.name must be lower-case letters, digits and hyphens (at most 32)")
	}
	if contains(Adapters, a.Name) {
		add("acp.name %q is the name of a built-in adapter", a.Name)
	}
	if a.Workdir == "" {
		add("acp.workdir is required")
	}
	if len(a.Command) == 0 || len(a.Command) > 64 || a.Command[0] == "" {
		add("acp.command is the agent's ACP server command as a list of arguments, starting with its executable")
		return
	}
	shell := customShells[strings.TrimSuffix(strings.TrimSuffix(strings.ToLower(filepath.Base(strings.ReplaceAll(a.Command[0], `\`, "/"))), ".exe"), ".cmd")]
	for _, arg := range a.Command {
		if arg == "" || len(arg) > 256 || strings.ContainsAny(arg, "\x00\r\n") {
			add("acp.command: each argument is one non-empty line of at most 256 characters")
		}
		if shell && (arg == "-c" || strings.EqualFold(arg, "/c") || strings.EqualFold(arg, "-Command")) {
			add("acp.command: the executable is a shell given a command string; name the agent's own executable")
		}
	}
	for name, d := range map[string]Duration{"handshake_timeout": a.HandshakeTimeout, "exit_timeout": a.ExitTimeout} {
		if d.Duration < 0 || d.Duration > MaxACPTimeout {
			add("acp.%s must be between 0 and %s", name, MaxACPTimeout)
		}
	}
}
