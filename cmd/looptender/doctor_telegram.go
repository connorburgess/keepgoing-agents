package main

import "github.com/looptender/looptender/internal/telegram"

// This is local setup evidence, not a token validation, delivery test, or
// poller health check. Never load the token or expose pairing identities.
func doctorTelegram(r *doctorReport, selectedDirectory string) {
	dir, err := resolveTelegramDir(selectedDirectory)
	if err != nil {
		r.add("telegram.pairing", "unavailable", "The local Telegram pairing directory could not be resolved.", "Check the selected Telegram directory and user directory settings.")
		return
	}
	_, paired, err := telegram.LoadPairing(dir)
	if err != nil {
		r.add("telegram.pairing", "unavailable", "Saved Telegram pairing could not be read or validated.", "Inspect private Telegram state; no pairing or token was changed.")
	} else if !paired {
		r.add("telegram.pairing", "warning", "No saved Telegram pairing was found in the selected directory.", "Use telegram setup when you intend to pair this device.")
	} else {
		r.add("telegram.pairing", "ok", "Saved Telegram pairing validates locally; remote authorization is unverified.", "")
	}
	if telegram.HasTokenFile(dir) {
		r.add("telegram.token_file", "ok", "A regular saved Telegram token file is present; its contents and validity were not checked.", "")
	} else {
		r.add("telegram.token_file", "warning", "A regular saved Telegram token file is not available; environment credentials were not inspected.", "Check your intended token source. Do not paste tokens into diagnostic reports.")
	}
	r.add("telegram.delivery", "unavailable", "Telegram delivery and polling health were not checked; no network request was made.", "Use telegram test only when you intend to send a test alert. Polling must be run separately for incoming controls.")
}
