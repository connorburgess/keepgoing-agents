package config

import (
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
)

// CustomAdapter is the adapter name of an agent defined in configuration.
const CustomAdapter = "custom"

// Custom is an agent keepgoing has no built-in runner for, described by its
// documented command-line interface. It runs one turn per process through
// the shared runner, like the built-in headless agents. Anything the
// definition does not declare is unknown to keepgoing.
type Custom struct {
	// Name labels the agent. It is the provider when none is set.
	Name    string            `yaml:"name"`
	Workdir string            `yaml:"workdir"`
	Env     map[string]string `yaml:"env,omitempty"`
	// Values are this loop's values for the settings the definition accepts.
	Values     map[string]string `yaml:"values,omitempty"`
	Definition Definition        `yaml:"definition"`
}

// Definition describes the agent's interface. No command is run through a
// shell, the prompt is never an argument, and output is read only as JSON
// fields compared for equality with declared codes.
type Definition struct {
	// Settings are the setting names the commands may refer to as {name}.
	Settings []string `yaml:"settings,omitempty"`
	// Start and Resume are argv. Resume may refer to {session}.
	Start  Argv `yaml:"start"`
	Resume Argv `yaml:"resume,omitempty"`
	// Prompt is "stdin" (the default) or "file"; with "file" the commands
	// refer to the private prompt file as {prompt_file}.
	Prompt   string         `yaml:"prompt,omitempty"`
	Output   Output         `yaml:"output"`
	Session  *SessionSource `yaml:"session,omitempty"`
	Finished Match          `yaml:"finished,omitempty"`
	Waiting  *Match         `yaml:"waiting,omitempty"`
	Errors   *CustomErrors  `yaml:"errors,omitempty"`
	Probe    *CustomProbe   `yaml:"probe,omitempty"`
	Limits   *CustomLimits  `yaml:"limits,omitempty"`
	// Capabilities must each be backed by a rule above.
	Capabilities CustomCapabilities `yaml:"capabilities,omitempty"`
}

// Argv is a command. An element is one argument, or a list of arguments that
// are left out together when a setting they refer to has no value.
type Argv []Arg

// Arg is one argument or one optional group.
type Arg struct {
	Parts    []string
	Optional bool
}

// UnmarshalYAML accepts a scalar or a list of scalars.
func (a *Arg) UnmarshalYAML(n *yaml.Node) error {
	switch n.Kind {
	case yaml.ScalarNode:
		a.Parts, a.Optional = []string{n.Value}, false
		return nil
	case yaml.SequenceNode:
		a.Optional = true
		for _, part := range n.Content {
			if part.Kind != yaml.ScalarNode {
				return fmt.Errorf("line %d: an optional group holds arguments only", part.Line)
			}
			a.Parts = append(a.Parts, part.Value)
		}
		return nil
	}
	return fmt.Errorf("line %d: an argument is text, or a list of arguments left out together", n.Line)
}

// MarshalYAML writes the form UnmarshalYAML reads.
func (a Arg) MarshalYAML() (any, error) {
	if a.Optional {
		return a.Parts, nil
	}
	if len(a.Parts) == 1 {
		return a.Parts[0], nil
	}
	return nil, fmt.Errorf("an argument has exactly one part")
}

// Output says how the agent's standard output is read.
type Output struct {
	// Format is "json" (one object) or "jsonl" (one object per line).
	Format string `yaml:"format"`
	// MaxBytes bounds the output read. Larger output is not read at all.
	MaxBytes int64 `yaml:"max_bytes,omitempty"`
	// Select picks the object the rules read: the last one whose field has
	// the value. Without it the last object is read.
	Select *Selector `yaml:"select,omitempty"`
}

// Selector picks an object by one field's value.
type Selector struct {
	Field  string `yaml:"field"`
	Equals string `yaml:"equals"`
}

// SessionSource is where the session identity comes from: a field of the
// output, or a file the agent writes under its working directory.
type SessionSource struct {
	Field  string    `yaml:"field,omitempty"`
	Select *Selector `yaml:"select,omitempty"`
	File   string    `yaml:"file,omitempty"`
}

// Match is true when the exit code is listed (if any are) and the field has
// one of the values (if a field is named).
type Match struct {
	ExitCodes []int    `yaml:"exit_codes,omitempty"`
	Field     string   `yaml:"field,omitempty"`
	Equals    []string `yaml:"equals,omitempty"`
}

// CustomErrors classify a failed turn. Classes maps the agent's own codes to
// keepgoing's error classes; anything else is unknown.
type CustomErrors struct {
	Field           string            `yaml:"field,omitempty"`
	Classes         map[string]string `yaml:"classes,omitempty"`
	HTTPStatusField string            `yaml:"http_status_field,omitempty"`
	ExitCodes       map[int]string    `yaml:"exit_codes,omitempty"`
}

// CustomProbe is a small real request that shows the account and model work.
// It receives the probe prompt as a turn does and is judged by the same
// finished and error rules.
type CustomProbe struct {
	Argv Argv `yaml:"argv"`
}

// CustomLimits asks the agent whether its account is blocked by a usage
// limit. It is run only when a failure is unclassified.
type CustomLimits struct {
	Argv    Argv  `yaml:"argv"`
	Blocked Match `yaml:"blocked"`
	// ResetsAtField holds an RFC 3339 time or Unix seconds.
	ResetsAtField string `yaml:"resets_at_field,omitempty"`
}

// CustomCapabilities is the definition's capability declaration.
type CustomCapabilities struct {
	ExactResume       bool `yaml:"exact_resume,omitempty"`
	CanBeStopped      bool `yaml:"can_be_stopped,omitempty"`
	StructuredErrors  bool `yaml:"structured_errors,omitempty"`
	StructuredWaiting bool `yaml:"structured_waiting,omitempty"`
	RetriesItself     bool `yaml:"retries_itself,omitempty"`
	FailsOverItself   bool `yaml:"fails_over_itself,omitempty"`
	SchedulesItself   bool `yaml:"schedules_itself,omitempty"`
	ChildWork         bool `yaml:"child_work,omitempty"`
}

// Bounds on a definition.
const (
	DefaultCustomOutput = 1 << 20
	MinCustomOutput     = 1 << 10
	MaxCustomOutput     = 16 << 20
	maxCustomArgs       = 64
	maxCustomValue      = 256
)

var (
	customName    = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)
	customSetting = regexp.MustCompile(`^[a-z][a-z0-9_]{0,31}$`)
	customField   = regexp.MustCompile(`^[A-Za-z0-9_]{1,64}(\.[A-Za-z0-9_]{1,64}){0,7}$`)
	// CustomToken is the shape of a code a rule may compare a field with.
	// Free text is never compared.
	CustomToken       = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,64}$`)
	customPlaceholder = regexp.MustCompile(`\{([^{}]*)\}`)
)

func (c *Custom) applyDefaults() {
	d := &c.Definition
	if d.Prompt == "" {
		d.Prompt = "stdin"
	}
	if d.Output.MaxBytes == 0 {
		d.Output.MaxBytes = DefaultCustomOutput
	}
	// A turn is finished, or waiting on a person, only with an exit code the
	// definition names; without one that is exit code 0. A field alone never
	// overrides a failing or unreadable exit record.
	if len(d.Finished.ExitCodes) == 0 {
		d.Finished.ExitCodes = []int{0}
	}
	if d.Waiting != nil && len(d.Waiting.ExitCodes) == 0 {
		d.Waiting.ExitCodes = []int{0}
	}
}

// resolvePaths resolves the working directory and every command's executable
// against the directory of the configuration file, like every other command
// in a configuration. A bare name is left for PATH.
func (c *Custom) resolvePaths(base string) {
	c.Workdir = resolve(base, c.Workdir)
	d := &c.Definition
	argvs := []Argv{d.Start, d.Resume}
	if d.Probe != nil {
		argvs = append(argvs, d.Probe.Argv)
	}
	if d.Limits != nil {
		argvs = append(argvs, d.Limits.Argv)
	}
	for _, argv := range argvs {
		if len(argv) > 0 && !argv[0].Optional && len(argv[0].Parts) == 1 {
			argv[0].Parts[0] = resolveExec(base, argv[0].Parts[0])
		}
	}
}

// customShells are command interpreters. A shell given a command string
// would make an argument a command line. Refusing that is a guard against an
// obvious mistake, not a security boundary: a wrapper script or another
// interpreter can do the same and is not detected.
var customShells = map[string]bool{"sh": true, "bash": true, "zsh": true, "dash": true, "ksh": true, "fish": true, "csh": true, "tcsh": true, "ash": true,
	"cmd": true, "powershell": true, "pwsh": true}

func customShell(executable string) bool {
	name := strings.ToLower(filepath.Base(strings.ReplaceAll(executable, `\`, "/")))
	for _, ext := range []string{".exe", ".cmd", ".bat"} {
		name = strings.TrimSuffix(name, ext)
	}
	return customShells[name]
}

// commandString reports whether an argument is a flag that makes a shell run
// the next argument as a command line.
func commandString(argv Argv) bool {
	for _, arg := range argv[1:] {
		for _, part := range arg.Parts {
			if part == "-c" || strings.EqualFold(part, "/c") || strings.EqualFold(part, "-Command") {
				return true
			}
		}
	}
	return false
}

// classWords says an error class in plain words.
var classWords = map[string]string{"rate_limit": "a rate limit", "auth": "an authentication failure", "overloaded": "an overloaded provider", "network": "a network failure"}

// FailoverMappings lists, in words, everything in the definition that maps
// the agent's own output to an error class the loop fails over on. Each is a
// declaration that such a result is a real provider limit: it moves work to
// a fallback. A refusal, a policy block or an account restriction must not
// be mapped; left unmapped it is unknown, which never moves work.
func (c Custom) FailoverMappings(failoverOn []string) []string {
	moves := func(class string) bool { return class != "" && contains(failoverOn, class) }
	const then = " and will move work to a fallback"
	var out []string
	e := c.Definition.Errors
	if e != nil {
		codes := make([]int, 0, len(e.ExitCodes))
		for code := range e.ExitCodes {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			if class := e.ExitCodes[code]; moves(class) {
				out = append(out, fmt.Sprintf("exit code %d is treated as %s%s", code, classWords[class], then))
			}
		}
		for _, code := range sortedKeys(e.Classes) {
			if class := e.Classes[code]; moves(class) {
				out = append(out, fmt.Sprintf("a result whose %s is %q is treated as %s%s", e.Field, code, classWords[class], then))
			}
		}
		if e.HTTPStatusField != "" {
			for _, status := range []struct{ codes, class string }{{"429", "rate_limit"}, {"401 or 403", "auth"}, {"503 or 529", "overloaded"}} {
				if moves(status.class) {
					out = append(out, fmt.Sprintf("an HTTP status of %s in %s is treated as %s%s", status.codes, e.HTTPStatusField, classWords[status.class], then))
				}
			}
		}
	}
	if l := c.Definition.Limits; l != nil && moves("rate_limit") {
		out = append(out, fmt.Sprintf("an unclassified failure for which the limits command reports %s as %s is treated as a rate limit%s", l.Blocked.Field, strings.Join(l.Blocked.Equals, " or "), then))
	}
	return out
}

// SettingValue returns the value of a setting, or "".
func (c Custom) SettingValue(name string) string { return c.Values[name] }

func (c Custom) validate(add func(format string, args ...any)) {
	d := c.Definition
	if !customName.MatchString(c.Name) {
		add("custom.name must be lower-case letters, digits and hyphens (at most 32)")
	}
	if contains(Adapters, c.Name) {
		add("custom.name %q is the name of a built-in adapter", c.Name)
	}
	if c.Workdir == "" {
		add("custom.workdir is required")
	}
	settings := map[string]bool{}
	for _, name := range d.Settings {
		switch {
		case !customSetting.MatchString(name):
			add("custom.definition.settings: %q is not a setting name (lower-case letters, digits and underscores)", name)
		case name == "session" || name == "prompt_file":
			add("custom.definition.settings: %q is reserved", name)
		case settings[name]:
			add("custom.definition.settings: %q is listed twice", name)
		}
		settings[name] = true
	}
	for _, name := range sortedKeys(c.Values) {
		v := c.Values[name]
		switch {
		case !settings[name]:
			add("custom.values.%s: the definition accepts no such setting", name)
		case v == "" || len(v) > maxCustomValue || strings.ContainsAny(v, "\x00\r\n"):
			add("custom.values.%s must be one line of at most %d characters", name, maxCustomValue)
		}
	}

	file := d.Prompt == "file"
	if d.Prompt != "stdin" && !file {
		add("custom.definition.prompt must be stdin or file; a prompt is never passed as an argument")
	}
	// prompted says the command receives a prompt (a turn or the probe).
	checkArgv := func(where string, argv Argv, session, prompted bool) (usesSession bool) {
		if len(argv) == 0 || len(argv) > maxCustomArgs {
			add("%s must have between 1 and %d arguments", where, maxCustomArgs)
			return false
		}
		if first := argv[0]; first.Optional || len(first.Parts) != 1 || first.Parts[0] == "" || strings.ContainsAny(first.Parts[0], "{}") {
			add("%s: the first argument is the executable, written out", where)
		} else if customShell(first.Parts[0]) && commandString(argv) {
			add("%s: the executable is a shell given a command string; name the agent's own executable, so that no argument is a command line", where)
		}
		usesFile := false
		for _, arg := range argv {
			if arg.Optional && len(arg.Parts) == 0 {
				add("%s: an optional group is empty", where)
			}
			for _, part := range arg.Parts {
				if part == "" {
					add("%s: an argument is empty", where)
				}
				if len(part) > maxCustomValue || strings.ContainsAny(part, "\x00\r\n") {
					add("%s: an argument must be one line of at most %d characters", where, maxCustomValue)
				}
				rest := customPlaceholder.ReplaceAllString(part, "")
				if strings.ContainsAny(rest, "{}") {
					add("%s: %q has an unmatched brace", where, part)
				}
				for _, m := range customPlaceholder.FindAllStringSubmatch(part, -1) {
					switch name := m[1]; {
					case name == "session" && session:
						usesSession = true
					case name == "session":
						add("%s: {session} is available to resume only", where)
					case name == "prompt_file" && file && prompted:
						usesFile = true
					case name == "prompt_file" && file:
						add("%s: this command receives no prompt", where)
					case name == "prompt_file":
						add("%s: {prompt_file} needs prompt: file", where)
					case name == "prompt":
						add("%s: a prompt is never passed as an argument; use prompt: stdin or prompt: file with {prompt_file}", where)
					case !settings[name]:
						add("%s: {%s} is not a declared setting", where, name)
					case !arg.Optional && c.Values[name] == "":
						add("%s: {%s} has no value; set custom.values.%s or put the argument in an optional group", where, name, name)
					}
				}
			}
		}
		if file && prompted && !usesFile {
			add("%s: prompt is file, so the command must refer to {prompt_file}", where)
		}
		return usesSession
	}
	checkArgv("custom.definition.start", d.Start, false, true)
	resumeUsesSession := false
	if d.Resume != nil {
		resumeUsesSession = checkArgv("custom.definition.resume", d.Resume, true, true)
	}

	if d.Output.Format != "json" && d.Output.Format != "jsonl" {
		add("custom.definition.output.format must be json or jsonl")
	}
	if d.Output.MaxBytes < MinCustomOutput || d.Output.MaxBytes > MaxCustomOutput {
		add("custom.definition.output.max_bytes must be between %d and %d", MinCustomOutput, MaxCustomOutput)
	}
	checkSelect := func(where string, s *Selector) {
		if s == nil {
			return
		}
		if !customField.MatchString(s.Field) || !CustomToken.MatchString(s.Equals) {
			add("%s needs a field and a code to compare it with", where)
		}
	}
	checkSelect("custom.definition.output.select", d.Output.Select)
	checkMatch := func(where string, m Match, needField bool) {
		for _, code := range m.ExitCodes {
			if code < 0 || code > 255 {
				add("%s.exit_codes: %d is not an exit code", where, code)
			}
		}
		if (m.Field == "") != (len(m.Equals) == 0) {
			add("%s: field and equals go together", where)
		}
		if m.Field != "" && !customField.MatchString(m.Field) {
			add("%s.field is not a field path (names joined by dots)", where)
		}
		for _, v := range m.Equals {
			if !CustomToken.MatchString(v) {
				add("%s.equals: %q is not a code; only codes are compared, never text", where, v)
			}
		}
		if needField && m.Field == "" || !needField && m.Field == "" && len(m.ExitCodes) == 0 {
			add("%s gives nothing to observe: name a field and its values%s", where, map[bool]string{true: "", false: ", or exit codes"}[needField])
		}
	}
	checkMatch("custom.definition.finished", d.Finished, false)

	if s := d.Session; s != nil {
		switch {
		case (s.Field == "") == (s.File == ""):
			add("custom.definition.session needs exactly one of field and file")
		case s.Field != "" && !customField.MatchString(s.Field):
			add("custom.definition.session.field is not a field path")
		case s.File != "" && (filepath.IsAbs(s.File) || !filepath.IsLocal(s.File) || strings.ContainsAny(s.File, "{}\x00")):
			add("custom.definition.session.file must be a path inside the working directory")
		}
		if s.File != "" && s.Select != nil {
			add("custom.definition.session.select applies to a field, not a file")
		}
		checkSelect("custom.definition.session.select", s.Select)
	}
	if d.Waiting != nil {
		checkMatch("custom.definition.waiting", *d.Waiting, false)
		// Waiting is read first, so a turn both rules match is a wait. That
		// is a fault only when it leaves finished nothing to match: every
		// finished exit code is a waiting one, and waiting has no field, or
		// the same field with every finished value.
		if w, f := *d.Waiting, d.Finished; subset(intStrings(f.ExitCodes), intStrings(w.ExitCodes)) &&
			(w.Field == "" || (w.Field == f.Field && subset(f.Equals, w.Equals))) {
			add("custom.definition.waiting matches every turn finished matches, so no turn could finish; give them different exit codes or values")
		}
	}
	observesErrors := false
	if e := d.Errors; e != nil {
		if (e.Field == "") != (len(e.Classes) == 0) {
			add("custom.definition.errors: field and classes go together")
		}
		if e.Field != "" && !customField.MatchString(e.Field) {
			add("custom.definition.errors.field is not a field path")
		}
		for _, code := range sortedKeys(e.Classes) {
			if !CustomToken.MatchString(code) {
				add("custom.definition.errors.classes: %q is not a code; only codes are compared, never text", code)
			}
			if e.Field == d.Finished.Field && containsString(d.Finished.Equals, code) {
				add("custom.definition.errors.classes: %q is also a finished value", code)
			}
			if class := e.Classes[code]; !customClass(class) {
				add("custom.definition.errors.classes[%s]: unknown error class %q (use one of %s)", code, class, strings.Join(ErrorClasses, ", "))
			}
		}
		if e.HTTPStatusField != "" && !customField.MatchString(e.HTTPStatusField) {
			add("custom.definition.errors.http_status_field is not a field path")
		}
		codes := make([]int, 0, len(e.ExitCodes))
		for code := range e.ExitCodes {
			codes = append(codes, code)
		}
		sort.Ints(codes)
		for _, code := range codes {
			if code < 1 || code > 255 {
				add("custom.definition.errors.exit_codes: %d is not a failing exit code", code)
			}
			if !customClass(e.ExitCodes[code]) {
				add("custom.definition.errors.exit_codes[%d]: unknown error class %q", code, e.ExitCodes[code])
			}
			for _, ok := range d.Finished.ExitCodes {
				if ok == code {
					add("custom.definition.errors.exit_codes[%d] is also a finished exit code", code)
				}
			}
		}
		observesErrors = e.Field != "" || e.HTTPStatusField != "" || len(e.ExitCodes) > 0
		if !observesErrors {
			add("custom.definition.errors gives nothing to observe")
		}
	}
	if p := d.Probe; p != nil {
		checkArgv("custom.definition.probe.argv", p.Argv, false, true)
	}
	if l := d.Limits; l != nil {
		checkArgv("custom.definition.limits.argv", l.Argv, false, false)
		checkMatch("custom.definition.limits.blocked", l.Blocked, true)
		if len(l.Blocked.ExitCodes) != 0 {
			add("custom.definition.limits.blocked is read from a field, not an exit code")
		}
		if l.ResetsAtField != "" && !customField.MatchString(l.ResetsAtField) {
			add("custom.definition.limits.resets_at_field is not a field path")
		}
	}

	// A capability is a claim keepgoing acts on, so each must be backed by a
	// rule that lets it be observed, and a rule without its capability would
	// be silently ignored.
	caps := d.Capabilities
	canResume := d.Resume != nil && d.Session != nil && resumeUsesSession
	switch {
	case caps.ExactResume && !canResume:
		add("custom.definition.capabilities.exact_resume needs a session source and a resume command that refers to {session}")
	case !caps.ExactResume && (d.Resume != nil || d.Session != nil):
		add("custom.definition: resume and session are used only with capabilities.exact_resume")
	}
	switch {
	case caps.StructuredErrors && !observesErrors:
		add("custom.definition.capabilities.structured_errors needs errors rules")
	case !caps.StructuredErrors && d.Errors != nil:
		add("custom.definition.errors is used only with capabilities.structured_errors")
	}
	switch {
	case caps.StructuredWaiting && d.Waiting == nil:
		add("custom.definition.capabilities.structured_waiting needs a waiting rule")
	case !caps.StructuredWaiting && d.Waiting != nil:
		add("custom.definition.waiting is used only with capabilities.structured_waiting")
	}
	for _, claim := range []struct {
		name    string
		claimed bool
	}{{"retries_itself", caps.RetriesItself}, {"fails_over_itself", caps.FailsOverItself}, {"schedules_itself", caps.SchedulesItself}, {"child_work", caps.ChildWork}} {
		if claim.claimed {
			add("custom.definition.capabilities.%s cannot be declared yet: a definition has no way to let keepgoing observe it", claim.name)
		}
	}
}

// customClass reports whether a definition may map a code to the class: a
// real error class, not "none".
func customClass(class string) bool { return ValidErrorClass(class) && class != "none" }

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// subset reports whether every element of a is in b.
func subset(a, b []string) bool {
	for _, v := range a {
		if !containsString(b, v) {
			return false
		}
	}
	return true
}

func intStrings(in []int) []string {
	out := make([]string, 0, len(in))
	for _, n := range in {
		out = append(out, fmt.Sprint(n))
	}
	return out
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
