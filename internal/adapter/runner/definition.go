package runner

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
	"github.com/connorburgess/keepgoing-agents/internal/decide"
	"github.com/connorburgess/keepgoing-agents/internal/evidence"
)

// An agent defined in configuration becomes a Spec like a built-in one, and
// runs through the same shared runner. Everything here reads structured
// output only: a JSON field is compared for equality with codes the
// definition declares, and anything else is unknown. No text is matched, no
// shell is used, and the prompt is never an argument.

// sessionToken is the shape a session identity must have to be saved and
// passed back as an argument. It cannot begin with a hyphen.
var sessionToken = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]{0,127}$`)

const (
	customLimitsTimeout = 20 * time.Second
	customLimitsOutput  = 64 << 10
	customSessionFile   = 256
)

// specFromDefinition builds the specification and settings of a defined
// agent. The configuration has been validated.
func specFromDefinition(c config.Custom) (*Spec, Settings) {
	d := c.Definition
	set := Settings{Workdir: c.Workdir, Model: c.Values["model"], Env: c.Env, Options: c.Values}
	if len(d.Start) > 0 && len(d.Start[0].Parts) == 1 {
		set.CLI = d.Start[0].Parts[0]
	}
	spec := &Spec{
		Name:         config.CustomAdapter,
		Provider:     c.Name,
		DefaultCLI:   set.CLI,
		SettingNames: append([]string(nil), d.Settings...),
		Prompt:       PromptVia(d.Prompt),
		MaxOutput:    d.Output.MaxBytes,
		Capabilities: Capabilities{
			ExactResume: d.Capabilities.ExactResume, Stoppable: d.Capabilities.CanBeStopped,
			StructuredErrors: d.Capabilities.StructuredErrors, StructuredWaiting: d.Capabilities.StructuredWaiting,
		},
		Argv: func(set Settings, session, promptFile string) []string {
			argv := d.Start
			if session != "" && d.Resume != nil {
				argv = d.Resume
			}
			return expandArgv(argv, set.Options, session, promptFile)
		},
		Read: func(turn Status) Reading {
			return readDefined(d, c.Workdir, turn.Stdout, turn.ExitCode, turn.ExitCodeKnown)
		},
	}
	if p := d.Probe; p != nil {
		spec.Probe = ProbeSpec{
			Command: func(r ProbeRequest) ([]string, string) {
				values := map[string]string{}
				for k, v := range c.Values {
					values[k] = v
				}
				// A probe may use a cheaper model than the turns do.
				if _, declared := values["model"]; declared || containsString(d.Settings, "model") {
					values["model"] = r.Model
				}
				argv := expandArgv(p.Argv, values, "", r.PromptFile)
				if d.Prompt == string(PromptFile) {
					return argv, ""
				}
				return argv, r.Prompt
			},
			Judge: func(stdout []byte, code int) ProbeVerdict {
				if int64(len(stdout)) > d.Output.MaxBytes {
					return ProbeVerdict{Class: decide.ErrUnknown, Detail: "output over the definition's limit"}
				}
				r := readDefined(d, "", stdout, code, true)
				switch {
				case r.Waiting:
					return ProbeVerdict{Class: decide.ErrUnknown, Detail: "waiting on a person"}
				case r.Completed:
					return ProbeVerdict{OK: true, Detail: "finished"}
				case r.Class != decide.ErrUnknown:
					return ProbeVerdict{Class: r.Class, Detail: "classified error"}
				}
				return ProbeVerdict{Class: decide.ErrUnknown, Detail: "not finished and not classified"}
			},
		}
	}
	if l := d.Limits; l != nil {
		spec.Limits = &LimitLookup{
			EvidenceSource: "custom_limits", EvidenceCode: "subscription_window_blocked", ProbeDetail: "limits lookup: ",
			Read: func(ctx context.Context, _ string, env []string) (Limits, error) {
				return readDefinedLimits(ctx, *l, c, env)
			},
		}
	}
	return spec, set
}

func containsString(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

var placeholder = regexp.MustCompile(`\{([^{}]*)\}`)

// expandArgv replaces {name} with the value of that setting, {session} and
// {prompt_file}. An optional group is left out when anything it refers to
// has no value. Each element stays one argument: a value cannot add any.
func expandArgv(argv config.Argv, values map[string]string, session, promptFile string) []string {
	lookup := func(name string) string {
		switch name {
		case "session":
			return session
		case "prompt_file":
			return promptFile
		}
		return values[name]
	}
	var out []string
	for _, arg := range argv {
		parts, complete := make([]string, 0, len(arg.Parts)), true
		for _, part := range arg.Parts {
			parts = append(parts, placeholder.ReplaceAllStringFunc(part, func(m string) string {
				v := lookup(m[1 : len(m)-1])
				if v == "" {
					complete = false
				}
				return v
			}))
		}
		if arg.Optional && !complete {
			continue
		}
		out = append(out, parts...)
	}
	return out
}

// jsonObjects returns the JSON objects in the output: one for the json
// format, one per parsable line for jsonl.
func jsonObjects(format string, stdout []byte) []map[string]any {
	var objects []map[string]any
	add := func(b []byte) {
		var o map[string]any
		if b = bytes.TrimSpace(b); len(b) > 0 && b[0] == '{' && json.Unmarshal(b, &o) == nil && o != nil {
			objects = append(objects, o)
		}
	}
	if format == "json" {
		add(stdout)
		return objects
	}
	for _, line := range bytes.Split(stdout, []byte("\n")) {
		add(line)
	}
	return objects
}

// fieldCode reads a field by its dotted path as a code: a string shaped like
// one, a whole number or a boolean. Anything else, including free text, is
// absent.
func fieldCode(o map[string]any, path string) (string, bool) {
	var cur any = o
	for _, name := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return "", false
		}
		if cur, ok = m[name]; !ok {
			return "", false
		}
	}
	switch v := cur.(type) {
	case string:
		if config.CustomToken.MatchString(v) {
			return v, true
		}
	case bool:
		return strconv.FormatBool(v), true
	case float64:
		if v == float64(int64(v)) && v > -1e15 && v < 1e15 {
			return strconv.FormatInt(int64(v), 10), true
		}
	}
	return "", false
}

// selectObject returns the last object the selector matches, or the last
// object when there is no selector.
func selectObject(objects []map[string]any, s *config.Selector) map[string]any {
	for i := len(objects) - 1; i >= 0; i-- {
		if s == nil {
			return objects[i]
		}
		if v, ok := fieldCode(objects[i], s.Field); ok && v == s.Equals {
			return objects[i]
		}
	}
	return nil
}

// matches applies a rule: the exit code is listed, when any are, and the
// field has one of the values, when a field is named.
func matches(m config.Match, o map[string]any, code int, codeKnown bool) bool {
	if len(m.ExitCodes) > 0 {
		listed := false
		for _, c := range m.ExitCodes {
			listed = listed || (codeKnown && c == code)
		}
		if !listed {
			return false
		}
	}
	if m.Field == "" {
		return true
	}
	v, ok := fieldCode(o, m.Field)
	return ok && containsString(m.Equals, v)
}

// readDefined interprets a finished turn of a defined agent.
func readDefined(d config.Definition, workdir string, stdout []byte, code int, codeKnown bool) Reading {
	objects := jsonObjects(d.Output.Format, stdout)
	result := selectObject(objects, d.Output.Select)
	r := Reading{Class: decide.ErrUnknown}

	if s := d.Session; s != nil && d.Capabilities.ExactResume {
		var id string
		switch {
		case s.Field != "":
			from := result
			if s.Select != nil {
				from = selectObject(objects, s.Select)
			}
			id, _ = fieldCode(from, s.Field)
		case workdir != "":
			id = readSessionFile(filepath.Join(workdir, s.File))
		}
		if sessionToken.MatchString(id) {
			r.Session = id
		}
	}
	if d.Waiting != nil && d.Capabilities.StructuredWaiting && matches(*d.Waiting, result, code, codeKnown) {
		r.Waiting = true
		return r
	}
	if matches(d.Finished, result, code, codeKnown) {
		r.Completed = true
		return r
	}
	e := d.Errors
	if e == nil || !d.Capabilities.StructuredErrors {
		return r
	}
	detail := evidence.Detail{Source: "custom_result"}
	if codeKnown {
		detail.ExitCode = evidence.Number(code)
	}
	if e.Field != "" {
		if v, ok := fieldCode(result, e.Field); ok {
			detail.Code = v
			if class, mapped := e.Classes[v]; mapped {
				r.Class = decide.ErrorClass(class)
			}
		}
	}
	if e.HTTPStatusField != "" {
		if v, ok := fieldCode(result, e.HTTPStatusField); ok {
			if n, err := strconv.Atoi(v); err == nil && n >= 100 && n <= 599 {
				detail.HTTPStatus = evidence.Number(n)
				if r.Class == decide.ErrUnknown {
					r.Class = adapter.ClassifyCode(v)
				}
			}
		}
	}
	if class, mapped := e.ExitCodes[code]; mapped && codeKnown && r.Class == decide.ErrUnknown {
		r.Class = decide.ErrorClass(class)
	}
	r.Evidence = evidence.Clean(detail)
	return r
}

// readSessionFile reads a session identity the agent wrote: a small regular
// file holding one token.
func readSessionFile(path string) string {
	info, err := os.Lstat(path)
	if err != nil || !info.Mode().IsRegular() || info.Size() > customSessionFile {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()
	b, err := io.ReadAll(io.LimitReader(f, customSessionFile+1))
	if err != nil || len(b) > customSessionFile {
		return ""
	}
	return strings.TrimSpace(string(b))
}

// readDefinedLimits runs the definition's limit lookup and reads one JSON
// object from it.
func readDefinedLimits(ctx context.Context, l config.CustomLimits, c config.Custom, env []string) (Limits, error) {
	argv := expandArgv(l.Argv, c.Values, "", "")
	if len(argv) == 0 {
		return Limits{}, errors.New("limits lookup: empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, customLimitsTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Dir = c.Workdir
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdin = strings.NewReader("")
	out := &boundedBuffer{limit: customLimitsOutput}
	cmd.Stdout = out
	cmd.WaitDelay = 2 * time.Second
	if err := cmd.Run(); err != nil || ctx.Err() != nil {
		return Limits{}, errors.New("limits lookup: the command failed")
	}
	if out.over {
		return Limits{}, errors.New("limits lookup: output over the limit")
	}
	objects := jsonObjects("json", out.buf.Bytes())
	if len(objects) != 1 {
		return Limits{}, errors.New("limits lookup: unexpected JSON")
	}
	limits := Limits{Blocked: matches(l.Blocked, objects[0], 0, true)}
	if l.ResetsAtField != "" {
		limits.ResetsAt = fieldTime(objects[0], l.ResetsAtField)
	}
	return limits, nil
}

// fieldTime reads an RFC 3339 time or Unix seconds; zero when it is neither
// or not a plausible time.
func fieldTime(o map[string]any, path string) time.Time {
	var cur any = o
	for _, name := range strings.Split(path, ".") {
		m, ok := cur.(map[string]any)
		if !ok {
			return time.Time{}
		}
		cur = m[name]
	}
	switch v := cur.(type) {
	case string:
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			return t.UTC()
		}
	case float64:
		if v > 0 && v < 1e11 {
			return time.Unix(int64(v), 0).UTC()
		}
	}
	return time.Time{}
}

// boundedBuffer keeps at most limit bytes and notes when more was written.
type boundedBuffer struct {
	buf   bytes.Buffer
	limit int
	over  bool
}

func (b *boundedBuffer) Write(p []byte) (int, error) {
	if room := b.limit - b.buf.Len(); len(p) > room {
		b.over = true
		b.buf.Write(p[:max(room, 0)])
		return len(p), nil
	}
	return b.buf.Write(p)
}
