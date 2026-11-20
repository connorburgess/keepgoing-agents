package main

import (
	"context"
	"errors"
	"flag"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/looptender/looptender/internal/control"
	"github.com/looptender/looptender/internal/lock"
	"github.com/looptender/looptender/internal/registry"
	"github.com/looptender/looptender/internal/telegram"
)

const (
	telegramPollLock       = "telegram-poll"
	telegramMinBackoff     = time.Second
	telegramMaxBackoff     = 30 * time.Second
	telegramMaxRetryAfter  = 60 * time.Second
	telegramDefaultPollSec = 25 * time.Second
)

type telegramPollConfig struct {
	Dir         string
	RegistryDir string
	Client      *telegram.Client
	PollWait    time.Duration
	StaleAfter  time.Duration
	Offline     bool
	Once        bool
	Host        string
	Now         func() time.Time
	Sleep       func(context.Context, time.Duration) error
	DaemonQuery clientDaemonQuery
}

func telegramPollCommand(e env, args []string) int {
	fs := flag.NewFlagSet("telegram poll", flag.ContinueOnError)
	fs.SetOutput(e.stderr)
	dirFlag := fs.String("dir", "", "private state directory for Telegram pairing (default: user state dir)")
	regFlag := fs.String("registry", "", "private loop registry directory (default: user registry dir)")
	apiURL := fs.String("api-url", "", "override Telegram Bot API base URL (https, or loopback http for tests)")
	pollWait := fs.Duration("poll-wait", telegramDefaultPollSec, "per-request getUpdates long-poll timeout (1s-50s)")
	staleAfter := fs.Duration("stale-after", telegram.DefaultStalenessWindow, "discard inbound commands or callbacks older than this duration")
	offline := fs.Bool("offline", false, "skip local daemon checks when building /status snapshots")
	once := fs.Bool("once", false, "run one getUpdates poll cycle and exit")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return exitOK
		}
		return exitUsage
	}
	if fs.NArg() > 0 {
		e.errf("telegram poll takes no positional arguments")
		return exitUsage
	}
	if *pollWait <= 0 || *staleAfter <= 0 {
		e.errf("telegram poll: --poll-wait and --stale-after must be positive")
		return exitUsage
	}
	dir, err := resolveTelegramDir(*dirFlag)
	if err != nil {
		e.errf("telegram poll: %v", err)
		return exitFailed
	}
	regDir, err := registryDirectory(*regFlag)
	if err != nil {
		e.errf("telegram poll: %v", err)
		return exitFailed
	}
	_, paired, err := telegram.LoadPairing(dir)
	if err != nil {
		e.errf("telegram poll: %v", err)
		return exitFailed
	}
	if !paired {
		e.errf("telegram poll: not paired; run `looptender telegram setup` first")
		return exitFailed
	}
	token, err := telegram.LoadToken(dir)
	if err != nil {
		e.errf("telegram poll: %v", err)
		return exitFailed
	}
	pollLock, err := lock.AcquireNamed(dir, telegramPollLock)
	if err != nil {
		if errors.Is(err, lock.ErrHeld) {
			e.errf("telegram poll: another Telegram poller is already running")
			return exitSkipped
		}
		e.errf("telegram poll: failed to acquire poller lock")
		return exitFailed
	}
	defer pollLock.Release()

	base := strings.TrimSpace(*apiURL)
	if base == "" {
		base = strings.TrimSpace(os.Getenv(telegram.APIURLEnv))
	}
	effectivePoll := *pollWait
	if effectivePoll < time.Second {
		effectivePoll = time.Second
	}
	if effectivePoll > 50*time.Second {
		effectivePoll = 50 * time.Second
	}
	client, err := telegram.NewClient(telegram.ClientConfig{
		Token:   token,
		BaseURL: base,
		Timeout: effectivePoll + 15*time.Second,
	})
	if err != nil {
		e.errf("telegram poll: %v", err)
		return exitUsage
	}
	host, _ := os.Hostname()
	return runTelegramPoller(e, telegramPollConfig{
		Dir:         dir,
		RegistryDir: regDir,
		Client:      client,
		PollWait:    effectivePoll,
		StaleAfter:  *staleAfter,
		Offline:     *offline,
		Once:        *once,
		Host:        host,
		Now:         time.Now,
		Sleep:       telegramSleepContext,
		DaemonQuery: inspectDaemon,
	})
}

func telegramSleepContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

func runTelegramPoller(e env, cfg telegramPollConfig) int {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.Sleep == nil {
		cfg.Sleep = telegramSleepContext
	}
	if cfg.DaemonQuery == nil {
		cfg.DaemonQuery = inspectDaemon
	}
	pollSec := int(cfg.PollWait.Seconds())
	if pollSec < 1 {
		pollSec = 1
	}
	if initialRec, ok, err := telegram.LoadPairing(cfg.Dir); err == nil && ok && initialRec.TopicsEnabled {
		if names := registeredLoopNames(cfg.RegistryDir); len(names) > 0 {
			_, _, _ = telegram.EnsureLoopTopics(e.ctx, cfg.Client, cfg.Dir, initialRec, names, cfg.Now().UTC())
		}
	}

	backoff := telegramMinBackoff

	for {
		if e.ctx.Err() != nil {
			return exitOK
		}
		rec, ok, err := telegram.LoadPairing(cfg.Dir)
		if err != nil || !ok {
			e.errf("telegram poll: pairing record unavailable")
			return exitFailed
		}
		updates, err := cfg.Client.GetUpdates(e.ctx, telegram.GetUpdatesRequest{
			Offset:         rec.LastUpdateOffset + 1,
			Limit:          20,
			TimeoutSeconds: pollSec,
			AllowedUpdates: []string{"message", "callback_query"},
		})
		if err != nil {
			if e.ctx.Err() != nil {
				return exitOK
			}
			e.errf("telegram poll: %v", err)
			if cfg.Once {
				return exitFailed
			}
			wait := backoff
			var apiErr *telegram.APIError
			if errors.As(err, &apiErr) && apiErr.RetryAfter > 0 {
				wait = time.Duration(apiErr.RetryAfter) * time.Second
				if wait > telegramMaxRetryAfter {
					wait = telegramMaxRetryAfter
				}
			} else {
				backoff *= 2
				if backoff > telegramMaxBackoff {
					backoff = telegramMaxBackoff
				}
			}
			if cfg.Sleep(e.ctx, wait) != nil {
				return exitOK
			}
			continue
		}
		backoff = telegramMinBackoff

		for _, u := range updates {
			if e.ctx.Err() != nil {
				return exitOK
			}
			prior := rec
			if u.UpdateID > rec.LastUpdateOffset {
				rec.LastUpdateOffset = u.UpdateID
				rec.UpdatedAt = cfg.Now().UTC()
				// Persist offset atomically before acting so a crash or restart
				// never replays a remote control button tap.
				if err := telegram.SavePairing(cfg.Dir, rec); err != nil {
					e.errf("telegram poll: failed to persist update offset: %v", err)
					return exitFailed
				}
			}
			handleTelegramUpdate(e, cfg, prior, rec, u)
		}

		if cfg.Once {
			return exitOK
		}
	}
}

func handleTelegramUpdate(e env, cfg telegramPollConfig, prior, current telegram.PairingRecord, u telegram.Update) {
	now := cfg.Now().UTC()
	if u.Message != nil {
		if !telegram.AuthorizeMessage(prior, u, now, cfg.StaleAfter) {
			return
		}
		cmd, _, ok := telegram.ParseCommand(u.Message.Text, current.BotUsername)
		if !ok {
			return
		}
		threadID := u.Message.MessageThreadID
		switch cmd {
		case "status":
			onlyLoop, _ := telegram.LookupLoopByThreadID(current, threadID)
			text, markup := buildTelegramStatus(e.ctx, cfg, now, onlyLoop)
			_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
				ChatID:             current.AllowedChatID,
				MessageThreadID:    threadID,
				Text:               text,
				LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
				ReplyMarkup:        markup,
			})
		case "help", "start":
			_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
				ChatID:             current.AllowedChatID,
				MessageThreadID:    threadID,
				Text:               telegram.FormatHelpMessage(current.Mode),
				LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
			})
		case "pause", "stop":
			_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
				ChatID:             current.AllowedChatID,
				MessageThreadID:    threadID,
				Text:               "Send /status and tap Pause or Stop on the loop you want to hold.",
				LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
			})
		case "resume":
			_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
				ChatID:             current.AllowedChatID,
				MessageThreadID:    threadID,
				Text:               "Send /status and tap Prepare resume on the held loop you want to review.",
				LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
			})
		}
		return
	}

	if u.CallbackQuery != nil {
		// Silently drop unauthorized senders, bots, wrong chats, or replayed offsets.
		if !telegram.AuthorizeCallback(prior, u) {
			return
		}
		cbID := u.CallbackQuery.ID
		if telegram.CallbackStale(u, now, cfg.StaleAfter) {
			_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
				CallbackQueryID: cbID,
				Text:            "Expired; send /status for fresh controls.",
			})
			return
		}
		action, ok := telegram.DecodeControlCallback(u.CallbackQuery.Data)
		if !ok {
			_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
				CallbackQueryID: cbID,
				Text:            "Unsupported control action.",
			})
			return
		}
		var threadID int64
		if u.CallbackQuery.Message != nil {
			threadID = u.CallbackQuery.Message.MessageThreadID
		}
		loopName := action.RegistrationID
		if entry, found := clientEntry(cfg.RegistryDir, action.RegistrationID); found && entry.Loop != "" {
			loopName = entry.Loop
		}
		if threadID == 0 && current.TopicsEnabled && current.LoopTopics != nil {
			if tid := current.LoopTopics[loopName]; tid > 0 {
				threadID = tid
			}
		}

		switch action.Action {
		case telegram.ActionPrepareResume:
			expect := ""
			if action.ExpectPrefix != "" {
				expect = controlExpectPrefixTag + action.ExpectPrefix
			}
			report, code := clientResume(e.ctx, cfg.RegistryDir, action.RegistrationID, "prepare-resume", "", false, "", "telegram", expect, cfg.Now, nil)
			if code != exitOK || report.Ticket == "" {
				toast, summary := formatTelegramPrepareResumeFailure(report, code)
				_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
					CallbackQueryID: cbID,
					Text:            toast,
				})
				text := telegram.FormatAlert(telegram.AlertSummary{
					Device:  cfg.Host,
					Loop:    loopName,
					Event:   "control_prepare_resume",
					Summary: summary,
					At:      now,
				})
				_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
					ChatID:             current.AllowedChatID,
					MessageThreadID:    threadID,
					Text:               text,
					LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
				})
				return
			}
			rev := telegram.ResumeReview{
				Device:         cfg.Host,
				Loop:           loopName,
				RegistrationID: action.RegistrationID,
				TicketID:       report.Ticket,
			}
			if report.ExpiresAt != nil {
				rev.ExpiresAt = *report.ExpiresAt
			}
			if report.Snapshot != nil {
				rev.ControlState = report.Snapshot.ControlState
				if report.Snapshot.Controls != nil {
					rev.Paused = report.Snapshot.Controls.Paused
					rev.Stop = report.Snapshot.Controls.Stop
					rev.AwaitingUser = report.Snapshot.Controls.AwaitingUser
					rev.Completed = report.Snapshot.Controls.Completed
				}
				if report.Snapshot.Runtime != nil {
					rev.Freshness = report.Snapshot.Runtime.Freshness
				}
				if report.Snapshot.LastCheck != nil {
					rev.LastAction = report.Snapshot.LastCheck.Action
					rev.LastOutcome = report.Snapshot.LastCheck.Outcome
				}
			}
			text, markup, err := telegram.FormatResumeReviewMessage(rev)
			if err != nil {
				_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
					CallbackQueryID: cbID,
					Text:            "Resume unavailable",
				})
				return
			}
			_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
				CallbackQueryID: cbID,
				Text:            "Review resume",
			})
			_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
				ChatID:             current.AllowedChatID,
				MessageThreadID:    threadID,
				Text:               text,
				LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
				ReplyMarkup:        markup,
			})
			return

		case telegram.ActionResume:
			report, code := clientResume(e.ctx, cfg.RegistryDir, action.RegistrationID, "resume", action.TicketID, false, "", "telegram", "", cfg.Now, nil)
			toast, summary := formatTelegramResumeOutcome(report, code)
			_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
				CallbackQueryID: cbID,
				Text:            toast,
			})
			text := telegram.FormatAlert(telegram.AlertSummary{
				Device:  cfg.Host,
				Loop:    loopName,
				Event:   "control_resume",
				Summary: summary,
				At:      now,
			})
			_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
				ChatID:             current.AllowedChatID,
				MessageThreadID:    threadID,
				Text:               text,
				LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
			})
			return
		}

		expect := ""
		if action.ExpectPrefix != "" {
			expect = controlExpectPrefixTag + action.ExpectPrefix
		}
		report, code := applyClientHoldFrom(e.ctx, cfg.RegistryDir, action.RegistrationID, action.Action, "telegram", expect)
		toast, summary := formatTelegramHoldOutcome(action.Action, report, code)
		_ = cfg.Client.AnswerCallbackQuery(e.ctx, telegram.AnswerCallbackQueryRequest{
			CallbackQueryID: cbID,
			Text:            toast,
		})
		text := telegram.FormatAlert(telegram.AlertSummary{
			Device:  cfg.Host,
			Loop:    loopName,
			Event:   "control_" + action.Action,
			Summary: summary,
			At:      now,
		})
		_, _ = cfg.Client.SendMessage(e.ctx, telegram.SendMessageRequest{
			ChatID:             current.AllowedChatID,
			MessageThreadID:    threadID,
			Text:               text,
			LinkPreviewOptions: &telegram.LinkPreviewOptions{IsDisabled: true},
		})
	}
}

func buildTelegramStatus(ctx context.Context, cfg telegramPollConfig, now time.Time, onlyLoop string) (string, *telegram.InlineKeyboardMarkup) {
	f, err := registry.Load(cfg.RegistryDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return telegram.FormatStatusMessage(cfg.Host, nil, now)
		}
		return telegram.FormatAlert(telegram.AlertSummary{
			Device:  cfg.Host,
			Loop:    "status",
			Event:   "unavailable",
			Summary: "Loop registry is unavailable on this device.",
			At:      now,
		}), nil
	}
	entries := append([]registry.Entry(nil), f.Entries...)
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Loop != entries[j].Loop {
			return entries[i].Loop < entries[j].Loop
		}
		return entries[i].ID < entries[j].ID
	})
	items := make([]telegram.LoopStatusItem, 0, len(entries))
	for _, entry := range entries {
		if onlyLoop != "" && entry.Loop != onlyLoop {
			continue
		}
		st := readClientStatus(ctx, cfg.RegistryDir, entry.ID, false, cfg.Offline, defaultStaleAfter, now, cfg.DaemonQuery)
		item := telegram.LoopStatusItem{
			RegistrationID: entry.ID,
			Loop:           entry.Loop,
			Outcome:        st.Outcome,
		}
		if st.Snapshot != nil {
			item.ControlState = st.Snapshot.ControlState
			if st.Snapshot.Controls != nil {
				item.Paused = st.Snapshot.Controls.Paused
				item.Stop = st.Snapshot.Controls.Stop
				item.AwaitingUser = st.Snapshot.Controls.AwaitingUser
				item.Completed = st.Snapshot.Controls.Completed
			}
			if st.Snapshot.Runtime != nil {
				item.Freshness = st.Snapshot.Runtime.Freshness
			}
			if st.Snapshot.LastCheck != nil {
				item.LastAction = st.Snapshot.LastCheck.Action
				item.LastOutcome = st.Snapshot.LastCheck.Outcome
			}
		}
		var ctl control.File
		if st.Outcome != "unavailable" && readTransferRecord(entry.StateDir, control.FileName, &ctl) == nil {
			d := controlDigest(ctl)
			if len(d) >= telegram.ExpectDigestPrefixLen && d != "unavailable" {
				item.ExpectPrefix = d[:telegram.ExpectDigestPrefixLen]
			}
			if !ctl.Stop {
				item.CanStop = true
				if !ctl.Paused {
					item.CanPause = true
				}
			}
			if ctl.Paused || ctl.Stop || ctl.AwaitingUser || ctl.Completed {
				item.CanResume = true
			}
		}
		items = append(items, item)
	}
	return telegram.FormatStatusMessage(cfg.Host, items, now)
}

func formatTelegramHoldOutcome(action string, r clientHoldReport, code int) (toast, summary string) {
	if r.ControlSave == "confirmed" {
		if action == telegram.ActionPause {
			return "Paused", "Pause hold saved from Telegram. Provider action is not confirmed; send /status to inspect."
		}
		return "Stop saved", "Stop hold saved from Telegram and left to the loop's checks. Send /status to inspect."
	}
	for _, issue := range r.Issues {
		switch issue {
		case "observation_changed":
			return "State changed; send /status", "Hold refused because loop controls changed since this status message was sent. Send /status to review fresh controls."
		case "busy":
			return "Loop busy; retry shortly", "Hold not saved because another command holds the control lock. Send /status and retry."
		case "registration_unavailable", "registration_changed", "binding_unavailable", "binding_changed":
			return "Registration unavailable", "Hold refused because the loop registration or configuration binding is unavailable or changed."
		}
	}
	if code == exitSkipped {
		return "Loop busy; retry shortly", "Hold not saved because the loop control lock is busy."
	}
	return "Control unavailable", "Hold could not be saved; inspect loop status locally."
}

func formatTelegramPrepareResumeFailure(r clientResumeReport, code int) (toast, summary string) {
	for _, issue := range r.Issues {
		switch issue {
		case "observation_changed":
			return "State changed; send /status", "Resume review refused because loop controls changed since this status message was sent. Send /status to review fresh controls."
		case "busy":
			return "Loop busy; retry shortly", "Resume review unavailable because another command holds the loop lock. Retry shortly."
		case "supervised_session":
			return "Resume refused", "Resume refused because a supervised session cannot clear its own hold."
		}
	}
	if code == exitSkipped {
		return "Loop busy; retry shortly", "Resume review unavailable because the loop lock is busy."
	}
	return "Resume unavailable", "Could not prepare a single-use resume ticket; inspect loop status locally."
}

func formatTelegramResumeOutcome(r clientResumeReport, code int) (toast, summary string) {
	if r.ControlSave == "confirmed" {
		return "Resumed", "Resume saved from Telegram using the single-use ticket. Loop-wide limits are unchanged; send /status to inspect."
	}
	for _, issue := range r.Issues {
		switch issue {
		case "ticket_unavailable_or_expired":
			return "Ticket expired; prepare again", "Resume refused because the single-use ticket is expired, already used, or superseded. Send /status and tap Prepare resume again."
		case "observation_changed":
			return "State changed; prepare again", "Resume refused because loop state changed after the ticket was prepared. Send /status and tap Prepare resume again."
		case "busy":
			return "Loop busy; retry shortly", "Resume not saved because another command holds the loop lock. Retry shortly."
		case "supervised_session":
			return "Resume refused", "Resume refused because a supervised session cannot clear its own hold."
		}
	}
	if code == exitSkipped {
		return "Loop busy; retry shortly", "Resume not saved because the loop lock is busy."
	}
	return "Resume refused", "Resume could not be saved; inspect loop status locally."
}
