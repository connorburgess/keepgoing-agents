package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/telegram"
	"github.com/looptender/looptender/internal/userdir"
)

const telegramUsage = `Telegram remote channel (zero-infrastructure local pairing):
  telegram setup       Pair a personal @BotFather bot via one-tap ?start= deep link
  telegram status      Show local Telegram pairing status (read-only; never prints tokens)
  telegram test        Send one redacted test alert to the paired chat (--dry-run previews)
  telegram poll        Long-poll getUpdates for /status, Pause/Stop, and ticketed Resume
  telegram topics      Inspect or sync per-loop private-chat topics (--sync creates missing topics)
  telegram disconnect  Remove local Telegram pairing and saved token file
`

func telegramCommand(e env, args []string) int {
	return telegramCommandWithInput(e, args, os.Stdin)
}

func telegramCommandWithInput(e env, args []string, stdin io.Reader) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(e.stdout, telegramUsage)
		return exitOK
	}
	sub, rest := args[0], args[1:]
	switch sub {
	case "setup":
		return telegramSetupCommand(e, rest, stdin)
	case "status":
		return telegramStatusCommand(e, rest)
	case "test":
		return telegramTestCommand(e, rest)
	case "poll":
		return telegramPollCommand(e, rest)
	case "topics":
		return telegramTopicsCommand(e, rest)
	case "disconnect":
		return telegramDisconnectCommand(e, rest)
	default:
		e.errf("telegram: unknown subcommand %q; see telegram --help", sub)
		return exitUsage
	}
}

func resolveTelegramDir(flagDir string) (string, error) {
	if strings.TrimSpace(flagDir) != "" {
		return filepath.Abs(flagDir)
	}
	return userdir.State("telegram")
}

func telegramSetupCommand(e env, args []string, stdin io.Reader) int {
	fs := flag.NewFlagSet("telegram setup", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dirFlag := fs.String("dir", "", "private state directory for Telegram pairing (default: user state dir)")
	regFlag := fs.String("registry", "", "private loop registry directory for optional topic creation")
	modeFlag := fs.String("mode", telegram.ModeAlerts, "channel mode: alerts (redacted default) or chat (explicit opt-in)")
	acceptNonE2E := fs.Bool("accept-non-e2e", false, "acknowledge Telegram Bot API is not end-to-end encrypted (required for --mode chat)")
	tokenEnv := fs.String("token-env", telegram.TokenEnv, "environment variable to read the @BotFather token from")
	tokenStdin := fs.Bool("token-stdin", false, "read the @BotFather token from standard input instead of the environment")
	apiURL := fs.String("api-url", "", "override Telegram Bot API base URL (https, or loopback http for tests)")
	timeout := fs.Duration("timeout", 2*time.Minute, "how long to wait for the one-tap pairing message")
	pollWait := fs.Duration("poll-wait", 2*time.Second, "per-request getUpdates timeout during pairing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		e.errf("telegram setup takes no positional arguments")
		return exitUsage
	}
	mode := strings.TrimSpace(*modeFlag)
	if mode != telegram.ModeAlerts && mode != telegram.ModeChat {
		e.errf("telegram setup: --mode must be %q or %q", telegram.ModeAlerts, telegram.ModeChat)
		return exitUsage
	}
	if mode == telegram.ModeChat && !*acceptNonE2E {
		e.errf("telegram setup: --mode chat sends steering replies and turn summaries through Telegram's servers without end-to-end encryption; pass --accept-non-e2e to opt in, or use --mode alerts")
		return exitUsage
	}
	if *timeout <= 0 || *pollWait <= 0 {
		e.errf("telegram setup: --timeout and --poll-wait must be positive")
		return exitUsage
	}
	dir, err := resolveTelegramDir(*dirFlag)
	if err != nil {
		e.errf("telegram setup: %v", err)
		return exitFailed
	}

	token := ""
	if !*tokenStdin && strings.TrimSpace(*tokenEnv) != "" {
		token = strings.TrimSpace(os.Getenv(strings.TrimSpace(*tokenEnv)))
	}
	if token == "" {
		if !*tokenStdin {
			fmt.Fprintln(e.stdout, "Create a free personal bot with @BotFather (/newbot), then paste its token below:")
		}
		if stdin == nil {
			e.errf("telegram setup: no input stream available for bot token")
			return exitFailed
		}
		sc := bufio.NewScanner(stdin)
		if sc.Scan() {
			token = strings.TrimSpace(sc.Text())
		}
		if err := sc.Err(); err != nil {
			e.errf("telegram setup: failed reading token from stdin")
			return exitFailed
		}
	}
	if token == "" {
		e.errf("telegram setup: bot token is required (set %s or pass --token-stdin)", *tokenEnv)
		return exitUsage
	}

	base := strings.TrimSpace(*apiURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv(telegram.APIURLEnv))
	}
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   token,
		BaseURL: base,
		Timeout: *pollWait + 10*time.Second,
	})
	if err != nil {
		e.errf("%v", err)
		return exitUsage
	}

	me, err := client.GetMe(e.ctx)
	if err != nil {
		e.errf("telegram setup: failed to validate bot token: %v", err)
		return exitFailed
	}
	if err := telegram.SaveToken(dir, token); err != nil {
		e.errf("telegram setup: failed to save private token file: %v", err)
		return exitFailed
	}
	_ = client.SetMyCommands(e.ctx, telegram.DefaultBotCommands)

	nonce, err := telegram.NewPairingNonce()
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}
	link, err := telegram.DeepLink(me.Username, nonce)
	if err != nil {
		e.errf("%v", err)
		return exitFailed
	}

	fmt.Fprintf(e.stdout, "Validated bot @%s (private-chat topics enabled: %t).\n", me.Username, me.HasTopicsEnabled)
	fmt.Fprintf(e.stdout, "Open this one-tap link in Telegram and press Start (expires in %s):\n  %s\n", timeout.Round(time.Second), link)
	fmt.Fprintln(e.stdout, "Waiting for pairing confirmation...")

	now := time.Now().UTC()
	deadline := now.Add(*timeout)
	pollCtx, cancel := context.WithDeadline(e.ctx, deadline)
	defer cancel()

	var offset int64
	pollSec := int((*pollWait).Seconds())
	if pollSec < 1 {
		pollSec = 1
	}

	var matched telegram.PairingMatch
	paired := false
	for !paired {
		if time.Now().UTC().After(deadline) {
			break
		}
		updates, err := client.GetUpdates(pollCtx, telegram.GetUpdatesRequest{
			Offset:         offset,
			Limit:          20,
			TimeoutSeconds: pollSec,
			AllowedUpdates: []string{"message"},
		})
		if err != nil {
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
				break
			}
			e.errf("telegram setup: polling failed: %v", err)
			return exitFailed
		}
		for _, u := range updates {
			if u.UpdateID >= offset {
				offset = u.UpdateID + 1
			}
			if m, ok := telegram.MatchPairingUpdate(u, nonce, time.Now().UTC(), deadline); ok {
				matched = m
				paired = true
				break
			}
		}
	}
	if !paired {
		e.errf("telegram setup: timed out waiting for pairing message")
		return exitFailed
	}

	pairedAt := time.Now().UTC()
	rec := telegram.PairingRecord{
		SchemaVersion:    telegram.PairingSchemaVersion,
		Mode:             mode,
		BotID:            me.ID,
		BotUsername:      me.Username,
		AllowedUserID:    matched.UserID,
		AllowedChatID:    matched.ChatID,
		TopicsEnabled:    me.HasTopicsEnabled,
		LastUpdateOffset: matched.UpdateID,
		PairedAt:         pairedAt,
		UpdatedAt:        pairedAt,
	}
	if err := telegram.SavePairing(dir, rec); err != nil {
		e.errf("telegram setup: failed to save pairing record: %v", err)
		return exitFailed
	}

	if rec.TopicsEnabled {
		if regDir, err := registryDirectory(*regFlag); err == nil {
			if names := registeredLoopNames(regDir); len(names) > 0 {
				if synced, res, syncErr := telegram.EnsureLoopTopics(e.ctx, client, dir, rec, names, pairedAt); syncErr == nil {
					rec = synced
					if len(res.Created) > 0 {
						var createdLoops []string
						for name := range res.Created {
							createdLoops = append(createdLoops, name)
						}
						sort.Strings(createdLoops)
						for _, name := range createdLoops {
							tid := res.Created[name]
							if link, err := telegram.TopicLink(rec.BotUsername, tid); err == nil {
								fmt.Fprintf(e.stdout, "Topic for %s: %s\n", name, link)
							}
						}
					}
				}
			}
		}
	}

	host, _ := os.Hostname()
	confirmText := telegram.FormatAlert(telegram.AlertSummary{
		Device:  host,
		Loop:    "telegram",
		Event:   "paired",
		Summary: fmt.Sprintf("Paired in %s mode. Configure loop.report.format: telegram to receive alerts here.", mode),
		At:      pairedAt,
	})
	_, _ = client.SendMessage(e.ctx, telegram.SendMessageRequest{
		ChatID: matched.ChatID,
		Text:   confirmText,
	})

	fmt.Fprintf(e.stdout, "Paired @%s with chat %d (mode: %s).\n", me.Username, matched.ChatID, mode)
	return exitOK
}

type telegramStatusView struct {
	Paired        bool   `json:"paired"`
	BotUsername   string `json:"botUsername,omitempty"`
	Mode          string `json:"mode,omitempty"`
	TopicsEnabled bool   `json:"topicsEnabled,omitempty"`
	LoopTopics    int    `json:"loopTopics,omitempty"`
	TokenPresent  bool   `json:"tokenPresent"`
	PairedAt      string `json:"pairedAt,omitempty"`
}

func telegramStatusCommand(e env, args []string) int {
	fs := flag.NewFlagSet("telegram status", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dirFlag := fs.String("dir", "", "private state directory for Telegram pairing")
	asJSON := fs.Bool("json", false, "print machine-readable JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	dir, err := resolveTelegramDir(*dirFlag)
	if err != nil {
		e.errf("telegram status: %v", err)
		return exitFailed
	}
	rec, ok, err := telegram.LoadPairing(dir)
	if err != nil {
		e.errf("telegram status: %v", err)
		return exitFailed
	}
	tokenPresent := strings.TrimSpace(os.Getenv(telegram.TokenEnv)) != "" || telegram.HasTokenFile(dir)
	v := telegramStatusView{
		Paired:       ok,
		TokenPresent: tokenPresent,
	}
	if ok {
		v.BotUsername = rec.BotUsername
		v.Mode = rec.Mode
		v.TopicsEnabled = rec.TopicsEnabled
		v.LoopTopics = len(rec.LoopTopics)
		v.PairedAt = rec.PairedAt.UTC().Format(time.RFC3339)
	}
	if *asJSON {
		b, _ := json.MarshalIndent(v, "", "  ")
		fmt.Fprintln(e.stdout, string(b))
		return exitOK
	}
	if !ok {
		fmt.Fprintf(e.stdout, "Telegram: not paired (token present: %t)\n", tokenPresent)
		return exitOK
	}
	fmt.Fprintf(e.stdout, "Telegram: paired to @%s\n", v.BotUsername)
	fmt.Fprintf(e.stdout, "Mode: %s\n", v.Mode)
	fmt.Fprintf(e.stdout, "Private-chat topics enabled: %t (%d loop topics)\n", v.TopicsEnabled, v.LoopTopics)
	fmt.Fprintf(e.stdout, "Token present: %t\n", v.TokenPresent)
	fmt.Fprintf(e.stdout, "Paired at: %s\n", v.PairedAt)
	return exitOK
}

func telegramTestCommand(e env, args []string) int {
	fs := flag.NewFlagSet("telegram test", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dirFlag := fs.String("dir", "", "private state directory for Telegram pairing")
	apiURL := fs.String("api-url", "", "override Telegram Bot API base URL")
	dryRun := fs.Bool("dry-run", false, "print the formatted test alert without sending")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	dir, err := resolveTelegramDir(*dirFlag)
	if err != nil {
		e.errf("telegram test: %v", err)
		return exitFailed
	}
	rec, ok, err := telegram.LoadPairing(dir)
	if err != nil {
		e.errf("telegram test: %v", err)
		return exitFailed
	}
	if !ok {
		e.errf("telegram test: not paired; run `looptender telegram setup` first")
		return exitFailed
	}
	host, _ := os.Hostname()
	text := telegram.FormatAlert(telegram.AlertSummary{
		Device:  host,
		Loop:    "test",
		Event:   "test",
		Summary: "Test alert from looptender",
		At:      time.Now().UTC(),
	})
	if *dryRun {
		fmt.Fprintln(e.stdout, text)
		return exitOK
	}
	token, err := telegram.LoadToken(dir)
	if err != nil {
		e.errf("telegram test: %v", err)
		return exitFailed
	}
	base := strings.TrimSpace(*apiURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv(telegram.APIURLEnv))
	}
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   token,
		BaseURL: base,
	})
	if err != nil {
		e.errf("telegram test: %v", err)
		return exitFailed
	}
	if _, err := client.SendMessage(e.ctx, telegram.SendMessageRequest{
		ChatID: rec.AllowedChatID,
		Text:   text,
	}); err != nil {
		e.errf("telegram test: %v", err)
		return exitFailed
	}
	fmt.Fprintln(e.stdout, "telegram alert HTTP accepted; human receipt unverified")
	return exitOK
}

func telegramDisconnectCommand(e env, args []string) int {
	fs := flag.NewFlagSet("telegram disconnect", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dirFlag := fs.String("dir", "", "private state directory for Telegram pairing")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	dir, err := resolveTelegramDir(*dirFlag)
	if err != nil {
		e.errf("telegram disconnect: %v", err)
		return exitFailed
	}
	if err := telegram.RemovePairing(dir); err != nil {
		e.errf("telegram disconnect: %v", err)
		return exitFailed
	}
	if err := telegram.RemoveToken(dir); err != nil {
		e.errf("telegram disconnect: %v", err)
		return exitFailed
	}
	fmt.Fprintln(e.stdout, "Removed local Telegram pairing and saved token file. To invalidate the token remotely, send /revoke to @BotFather.")
	return exitOK
}

type telegramTopicEntry struct {
	Loop     string `json:"loop"`
	ThreadID int64  `json:"threadId"`
	Link     string `json:"link,omitempty"`
	Status   string `json:"status,omitempty"`
}

type telegramTopicsView struct {
	TopicsEnabled bool                 `json:"topicsEnabled"`
	Synced        bool                 `json:"synced"`
	Topics        []telegramTopicEntry `json:"topics"`
	FailedLoops   []string             `json:"failedLoops,omitempty"`
}

func registeredLoopNames(regDir string) []string {
	f, err := registry.Load(regDir)
	if err != nil {
		return nil
	}
	seen := map[string]bool{}
	var names []string
	for _, entry := range f.Entries {
		name := strings.TrimSpace(entry.Loop)
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

func telegramTopicsCommand(e env, args []string) int {
	fs := flag.NewFlagSet("telegram topics", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dirFlag := fs.String("dir", "", "private state directory for Telegram pairing")
	regFlag := fs.String("registry", "", "private loop registry directory")
	apiURL := fs.String("api-url", "", "override Telegram Bot API base URL (https, or loopback http for tests)")
	syncFlag := fs.Bool("sync", false, "create missing private-chat forum topics for registered loops")
	asJSON := fs.Bool("json", false, "print topic mappings as JSON")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		e.errf("telegram topics takes no positional arguments")
		return exitUsage
	}
	dir, err := resolveTelegramDir(*dirFlag)
	if err != nil {
		e.errf("telegram topics: %v", err)
		return exitFailed
	}
	// Topic synchronization writes the complete pairing record. Serialize it
	// with polling before reading so it cannot roll back an update offset or
	// lose mappings created by another synchronization.
	if *syncFlag {
		l, err := lock.AcquireNamed(dir, telegramPollLock)
		if err != nil {
			e.errf("telegram topics: stop the poller or wait for topic synchronization to finish before syncing")
			return exitSkipped
		}
		defer l.Release()
	}
	rec, ok, err := telegram.LoadPairing(dir)
	if err != nil {
		e.errf("telegram topics: %v", err)
		return exitFailed
	}
	if !ok {
		e.errf("telegram topics: not paired; run `looptender telegram setup` first")
		return exitFailed
	}

	view := telegramTopicsView{
		TopicsEnabled: rec.TopicsEnabled,
		Synced:        *syncFlag,
		Topics:        []telegramTopicEntry{},
	}

	var created map[string]int64
	if *syncFlag && rec.TopicsEnabled {
		regDir, err := registryDirectory(*regFlag)
		if err != nil {
			e.errf("telegram topics: %v", err)
			return exitFailed
		}
		token, err := telegram.LoadToken(dir)
		if err != nil {
			e.errf("telegram topics: %v", err)
			return exitFailed
		}
		base := strings.TrimSpace(*apiURL)
		if base == "" {
			base = strings.TrimSpace(os.Getenv(telegram.APIURLEnv))
		}
		client, err := telegram.NewClient(telegram.ClientConfig{
			Token:   token,
			BaseURL: base,
		})
		if err != nil {
			e.errf("telegram topics: %v", err)
			return exitUsage
		}
		names := registeredLoopNames(regDir)
		synced, res, err := telegram.EnsureLoopTopics(e.ctx, client, dir, rec, names, time.Now().UTC())
		if err != nil {
			e.errf("telegram topics: %v", err)
			return exitFailed
		}
		rec = synced
		created = res.Created
		for loopName := range res.Failed {
			view.FailedLoops = append(view.FailedLoops, loopName)
		}
		sort.Strings(view.FailedLoops)
	}

	var loopNames []string
	for name := range rec.LoopTopics {
		loopNames = append(loopNames, name)
	}
	sort.Strings(loopNames)
	for _, name := range loopNames {
		tid := rec.LoopTopics[name]
		link, _ := telegram.TopicLink(rec.BotUsername, tid)
		status := "mapped"
		if created != nil && created[name] == tid {
			status = "created"
		}
		view.Topics = append(view.Topics, telegramTopicEntry{
			Loop:     name,
			ThreadID: tid,
			Link:     link,
			Status:   status,
		})
	}

	if *asJSON {
		enc := json.NewEncoder(e.stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(view)
		return exitOK
	}
	if !view.TopicsEnabled {
		fmt.Fprintln(e.stdout, "Telegram topics: disabled (using single private DM)")
		return exitOK
	}
	if len(view.Topics) == 0 {
		fmt.Fprintln(e.stdout, "Telegram topics: enabled (no loop topics mapped yet)")
	} else {
		fmt.Fprintf(e.stdout, "Telegram topics: enabled (%d mapped)\n", len(view.Topics))
		for _, item := range view.Topics {
			if item.Link != "" {
				fmt.Fprintf(e.stdout, "  %s: thread %d (%s) — %s\n", item.Loop, item.ThreadID, item.Status, item.Link)
			} else {
				fmt.Fprintf(e.stdout, "  %s: thread %d (%s)\n", item.Loop, item.ThreadID, item.Status)
			}
		}
	}
	if len(view.FailedLoops) > 0 {
		fmt.Fprintf(e.stdout, "Fallback to single DM for loops that could not create topics: %s\n", strings.Join(view.FailedLoops, ", "))
	}
	return exitOK
}
