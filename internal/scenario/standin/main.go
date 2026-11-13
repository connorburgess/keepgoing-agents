// Command standin is a test-only stand-in for the bb, claude and codex CLIs, used by
// the scenario suite in internal/scenario. It is compiled once per test run
// and installed under each name it plays (bb, claude; with .exe on Windows),
// so it runs natively on every platform without a POSIX shell. It picks its
// part from the name it was run as.
//
// As claude or codex it answers probes and runs headless turns. A turn's
// answer follows net.mode (online, offline or portal), unless headless.mode
// says otherwise: online succeeds and reports the session; offline (or
// "structured") fails with the CLI's structured error; portal prints a
// sign-in page; silent exits 1 with no output, before any session exists.
//
// All state lives in the directory named by SCENARIO_WORLD, which the test
// writes and reads:
//
//	<thread>.show          JSON returned by `thread show`
//	<thread>.log           JSON array of log events, filtered by --after-seq
//	<thread>.children      JSON returned by `thread list --parent-thread`
//	<thread>.interactions  JSON returned by `thread interactions list`
//	place.list             JSON array of {id, title} returned by
//	                       `thread list --project ... --environment ...`
//	spawn.modes            one outcome per line for successive `thread spawn`
//	                       calls, consumed in order (default ok); see spawn
//	probe-limited.<model>  if present, a claude probe of that model is rate limited
//	calls.jsonl            every bb invocation, one JSON array of arguments per line
//	probes.jsonl           every probe
//	net.mode               online, offline or portal (default online)
//	headless.mode          overrides net.mode for headless turns
//	sends.jsonl            everything an agent received: `thread tell` (thread,
//	                       mode, message), an accepted `thread retry`, and a
//	                       `thread spawn` (new thread, title, first prompt), and
//	                       every headless turn (thread is the CLI name, mode the
//	                       session it resumed, message the prompt)
//
// `thread retry` re-submits a failed turn: it is accepted only while the
// thread's show file says "error" and answers like BB otherwise: an error
// envelope with no_failed_turn and exit status 1.
//
// Any command it does not know is recorded and answered with {"ok":true}.
// It never contacts a network or a real agent.
package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

func main() {
	world := os.Getenv("SCENARIO_WORLD")
	if world == "" {
		fmt.Fprintln(os.Stderr, "standin: SCENARIO_WORLD is not set")
		os.Exit(97)
	}
	name := strings.TrimSuffix(filepath.Base(os.Args[0]), ".exe")
	switch name {
	case "claude":
		if isClaudeTurn(os.Args[1:]) {
			os.Exit(turn(world, name, os.Args[1:]))
		}
		os.Exit(claude(world, os.Args[1:]))
	case "codex":
		if len(os.Args) > 1 && os.Args[1] == "app-server" {
			os.Exit(1) // no structured limit snapshot
		}
		os.Exit(turn(world, name, os.Args[1:]))
	}
	os.Exit(run(world, os.Args[1:]))
}

func run(world string, args []string) int {
	appendLine(filepath.Join(world, "calls.jsonl"), args)
	if len(args) < 2 || args[0] != "thread" {
		fmt.Println(`{"ok":true}`)
		return 0
	}
	switch args[1] {
	case "show":
		return emit(world, arg(args, 2)+".show", `{"thread":{"status":"idle"}}`)
	case "list":
		if p := flagValue(args, "--parent-thread"); p != "" {
			return emit(world, p+".children", `[]`)
		}
		return emit(world, "place.list", `[]`)
	case "interactions":
		return emit(world, arg(args, 3)+".interactions", `[]`)
	case "log":
		return emitLog(world, arg(args, 2), flagValue(args, "--after-seq"))
	case "tell":
		msg, _ := io.ReadAll(os.Stdin)
		appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{
			"kind": "tell", "thread": arg(args, 2), "mode": flagValue(args, "--mode"), "message": string(msg),
		})
		fmt.Println(`{"ok":true}`)
		return 0
	case "retry":
		return retry(world, arg(args, 2))
	case "spawn":
		return spawn(world, args)
	}
	fmt.Println(`{"ok":true}`)
	return 0
}

// spawn creates a thread. The next line of spawn.modes says what happens:
//
//	ok        the thread is created and listed, and its ID is printed
//	lost      the thread is created and listed, but the command prints
//	          nothing and exits 1, as when bb is cut off after BB acted
//	unlisted  the thread is created but not listed under its title (it was
//	          renamed), and bb prints a server error
//	refuse    bb rejects the invocation with unknown_option; nothing exists
func spawn(world string, args []string) int {
	mode := nextMode(filepath.Join(world, "spawn.modes"))
	if mode == "refuse" {
		fmt.Println(`{"ok":false,"error":{"code":"unknown_option"}}`)
		return 1
	}
	prompt, _ := io.ReadAll(os.Stdin)
	var made []string
	readJSON(filepath.Join(world, "spawned.json"), &made)
	id := fmt.Sprintf("thr_example_new%d", len(made)+1)
	made = append(made, id)
	writeJSON(filepath.Join(world, "spawned.json"), made)
	project, environment, title := flagValue(args, "--project"), flagValue(args, "--environment"), flagValue(args, "--title")
	writeJSON(filepath.Join(world, id+".show"), map[string]any{"thread": map[string]any{
		"id": id, "status": "active", "projectId": project, "environmentId": environment,
	}})
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{
		"kind": "spawn", "thread": id, "title": title, "mode": flagValue(args, "--permission-mode"), "message": string(prompt),
	})
	if mode != "unlisted" {
		var listed []map[string]any
		readJSON(filepath.Join(world, "place.list"), &listed)
		writeJSON(filepath.Join(world, "place.list"), append(listed, map[string]any{"id": id, "title": title}))
	}
	switch mode {
	case "lost":
		return 1
	case "unlisted":
		fmt.Println(`{"ok":false,"error":{"code":"internal_error"}}`)
		return 1
	}
	b, _ := json.Marshal(map[string]string{"id": id, "projectId": project, "environmentId": environment})
	fmt.Println(string(b))
	return 0
}

// nextMode pops the first line of a file of modes; "ok" when there is none.
func nextMode(path string) string {
	b, err := os.ReadFile(path)
	if err != nil {
		return "ok"
	}
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) == 0 || lines[0] == "" {
		return "ok"
	}
	os.WriteFile(path, []byte(strings.Join(lines[1:], "\n")), 0o600)
	return strings.TrimSpace(lines[0])
}

// claude answers a probe: a success, or a structured rate limit when
// probe-limited.<model> exists.
func claude(world string, args []string) int {
	appendLine(filepath.Join(world, "probes.jsonl"), args)
	model := flagValue(args, "--model")
	if model == "" {
		model = "default"
	}
	if _, err := os.Stat(filepath.Join(world, "probe-limited."+model)); err == nil {
		fmt.Println(`{"type":"result","subtype":"error_during_execution","is_error":true,"api_error_status":429,"result":"fictional limit"}`)
		return 1
	}
	fmt.Println(`{"type":"result","subtype":"success","is_error":false,"session_id":"session_example_probe","result":"OK"}`)
	return 0
}

func readJSON(path string, v any) {
	if b, err := os.ReadFile(path); err == nil {
		json.Unmarshal(b, v)
	}
}

func writeJSON(path string, v any) {
	b, _ := json.Marshal(v)
	tmp := path + ".tmp"
	os.WriteFile(tmp, b, 0o600)
	os.Rename(tmp, path)
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func flagValue(args []string, name string) string {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == name {
			return args[i+1]
		}
	}
	return ""
}

func emit(world, name, fallback string) int {
	b, err := os.ReadFile(filepath.Join(world, name))
	if err != nil {
		b = []byte(fallback)
	}
	os.Stdout.Write(b)
	fmt.Println()
	return 0
}

// emitLog returns the events newer than --after-seq, oldest first, as bb does.
func emitLog(world, thread, after string) int {
	var events []map[string]any
	if b, err := os.ReadFile(filepath.Join(world, thread+".log")); err == nil {
		if err := json.Unmarshal(b, &events); err != nil {
			fmt.Fprintln(os.Stderr, "standin: bad log fixture:", err)
			return 98
		}
	}
	min, _ := strconv.ParseFloat(after, 64)
	out := []map[string]any{}
	for _, e := range events {
		if seq, _ := e["seq"].(float64); seq > min {
			out = append(out, e)
		}
	}
	b, _ := json.Marshal(out)
	os.Stdout.Write(b)
	fmt.Println()
	return 0
}

func appendLine(path string, v any) {
	b, _ := json.Marshal(v)
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	f.Write(append(b, '\n'))
}

func retry(world, thread string) int {
	var show struct {
		Thread struct {
			Status string `json:"status"`
		} `json:"thread"`
	}
	b, _ := os.ReadFile(filepath.Join(world, thread+".show"))
	if json.Unmarshal(b, &show) != nil || show.Thread.Status != "error" {
		fmt.Println(`{"ok":false,"error":{"code":"no_failed_turn","message":"fictional"}}`)
		return 1
	}
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{"kind": "retry", "thread": thread})
	fmt.Println(`{"ok":true,"delivery":"sent","attempt":1}`)
	return 0
}

// HeadlessSession is the session every stand-in headless turn reports.
const HeadlessSession = "00000000-0000-4000-8000-0000000000aa"

// isClaudeTurn tells a headless turn (prompt on stdin, JSON output) from a
// probe (prompt as the argument after -p).
func isClaudeTurn(args []string) bool {
	return len(args) > 2 && args[0] == "-p" && args[1] == "--output-format" && args[2] == "json"
}

// turn runs one headless turn of claude or codex.
func turn(world, cli string, args []string) int {
	prompt, _ := io.ReadAll(os.Stdin)
	resumed := ""
	switch cli {
	case "claude":
		resumed = flagValue(args, "--resume")
	case "codex":
		if len(args) > 2 && args[0] == "exec" && args[1] == "resume" {
			resumed = args[len(args)-2] // the session comes before the final "-"
		}
	}
	appendLine(filepath.Join(world, "sends.jsonl"), map[string]string{"kind": "turn", "thread": cli, "mode": resumed, "message": string(prompt)})
	mode := readMode(filepath.Join(world, "headless.mode"), "")
	if mode == "" {
		mode = readMode(filepath.Join(world, "net.mode"), "online")
	}
	switch mode {
	case "silent":
		return 1
	case "portal":
		fmt.Println("<html><body>Fictional sign-in page</body></html>")
		return 1
	case "online":
		if cli == "claude" {
			fmt.Printf(`{"type":"result","subtype":"success","is_error":false,"session_id":%q,"api_error_status":null}`+"\n", HeadlessSession)
		} else {
			fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n"+`{"type":"turn.completed","usage":{}}`+"\n", HeadlessSession)
		}
		return 0
	}
	// offline or structured: the CLI's structured failure, no recognised code
	// for Claude Code, stream_disconnected for Codex.
	if cli == "claude" {
		fmt.Printf(`{"type":"result","subtype":"error_during_execution","is_error":true,"session_id":%q,"api_error_status":null}`+"\n", HeadlessSession)
	} else {
		fmt.Printf(`{"type":"thread.started","thread_id":%q}`+"\n"+`{"type":"turn.failed","error":{"type":"stream_disconnected"}}`+"\n", HeadlessSession)
	}
	return 1
}

func readMode(path, fallback string) string {
	b, err := os.ReadFile(path)
	if err != nil || strings.TrimSpace(string(b)) == "" {
		return fallback
	}
	return strings.TrimSpace(string(b))
}
