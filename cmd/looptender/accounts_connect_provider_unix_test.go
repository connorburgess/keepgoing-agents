//go:build darwin || linux

package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
	"golang.org/x/text/unicode/norm"
)

func TestAccountConnectClaudeNativeCommandContract(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	cli := filepath.Join(root, "claude-fixture")
	contextDir := filepath.Join(root, "account")
	if err := os.Mkdir(contextDir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(root, "commands")
	write(t, cli, `#!/bin/sh
[ "$BB_THREAD_ID" = 'thr_example_owner' ] || exit 9
[ "$CLAUDE_CONFIG_DIR" = '`+contextDir+`' ] || exit 10
[ "$1" = '--setting-sources' ] && [ "$2" = '' ] && [ "$3" = '--settings' ] && [ "$4" = '{"disableAllHooks":true}' ] || exit 11
shift 4
printf '%s\n' "$*" >> '`+marker+`'
if [ "$*" = 'auth login --claudeai' ]; then exit 0; fi
[ "$*" = 'auth status --json' ] || exit 12
printf '%s' '{"loggedIn":true,"authMethod":"claude.ai","configDirectory":"'"$CLAUDE_CONFIG_DIR"'","email":"private@example.com","token":"fictional-secret"}'
`, 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	err := accountConnectClaude(ctx, accounts.Entry{CLI: cli, Directory: contextDir, Workdir: root}, []string{"BB_THREAD_ID=thr_example_owner", "CLAUDE_CONFIG_DIR=" + contextDir})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := os.ReadFile(marker)
	if string(raw) != "auth login --claudeai\nauth status --json\n" {
		t.Fatal("unexpected provider operation", string(raw))
	}
}
func TestAccountConnectClaudeCancellationAndUnknownAuth(t *testing.T) {
	for _, kind := range []string{"cancel", "api-key", "wrong-directory", "failed-login"} {
		t.Run(kind, func(t *testing.T) {
			root, rootErr := filepath.EvalSymlinks(t.TempDir())
			if rootErr != nil {
				t.Fatal(rootErr)
			}
			cli := filepath.Join(root, "cli")
			body := "exit 7\n"
			switch kind {
			case "cancel":
				body = "exec sleep 10\n"
			case "api-key":
				body = "printf '%s' '{\"loggedIn\":true,\"authMethod\":\"api_key\",\"configDirectory\":\"" + root + "\"}'\n"
			case "wrong-directory":
				body = "printf '%s' '{\"loggedIn\":true,\"authMethod\":\"claude.ai\",\"configDirectory\":\"/example/another\"}'\n"
			}
			write(t, cli, "#!/bin/sh\n"+body, 0700)
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			start := time.Now()
			if err := accountConnectClaude(ctx, accounts.Entry{CLI: cli, Directory: root, Workdir: root}, []string{"PATH=" + os.Getenv("PATH")}); err == nil {
				t.Fatal("unverified completion accepted")
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("unbounded owned process")
			}
		})
	}
}
func TestAccountConnectAdmissionRefusesScriptsAndCancelledCopy(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	script := filepath.Join(root, "script")
	write(t, script, "#!/bin/sh\nexit 0\n", 0700)
	if _, err := accountConnectDigest(context.Background(), accounts.Entry{Provider: "codex", CLI: script}); err == nil {
		t.Fatal("script launch semantics admitted")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := accountConnectDigest(ctx, accounts.Entry{Provider: "codex", CLI: script}); err == nil {
		t.Fatal("cancelled admission")
	}
	// A native-looking file is only pinned to reviewed bytes, never executed by
	// this fixture. Staging must refuse replacements after that digest admission.
	native := filepath.Join(root, "native")
	if err := os.WriteFile(native, append([]byte{0x7f, 'E', 'L', 'F'}, []byte("fictional-native-image")...), 0700); err != nil {
		t.Fatal(err)
	}
	digest, err := accountConnectDigest(context.Background(), accounts.Entry{Provider: "codex", CLI: native})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(native, []byte("replaced"), 0700); err != nil {
		t.Fatal(err)
	}
	if image, cleanup, err := accountClaudeQuotaStage(context.Background(), native, digest); err == nil {
		cleanup()
		t.Fatal("replacement admitted", image)
	}
	if accountConnectAuthorizationURL("https://auth.openai.com.evil.example/authorize") || accountConnectAuthorizationURL("https://auth.openai.com:444/authorize") || accountConnectAuthorizationURL("https://user@auth.openai.com/authorize") || accountConnectAuthorizationURL("https://auth.openai.com/authorize#secret") || accountConnectAuthorizationURL(strings.Repeat("x", 9000)) {
		t.Fatal("untrusted continuation URL admitted")
	}
}

func TestAccountConnectClaudeProviderNormalizedDirectory(t *testing.T) {
	root, rootErr := filepath.EvalSymlinks(t.TempDir())
	if rootErr != nil {
		t.Fatal(rootErr)
	}
	decomposed := filepath.Join(root, "Cafe\u0301-account")
	reported := norm.NFC.String(decomposed)
	if err := os.Mkdir(decomposed, 0700); err != nil {
		t.Fatal(err)
	}
	want, err := os.Stat(decomposed)
	if err != nil {
		t.Fatal(err)
	}
	got, err := os.Stat(reported)
	if os.IsNotExist(err) {
		t.Skip("filesystem does not resolve Unicode normalization-equivalent names")
	}
	if err != nil || !os.SameFile(want, got) {
		t.Fatal("fixture paths do not identify same directory", err)
	}
	if decomposed == reported || !accountConnectClaudeDirectory(decomposed, reported) {
		t.Fatal("provider NFC normalization refused same directory")
	}

	// Exercise the full fictional auth command/status boundary, not just a path
	// helper: restoring raw equality must reject this same-directory success.
	cli := filepath.Join(root, "unicode-auth-fixture")
	status, _ := json.Marshal(map[string]any{"loggedIn": true, "authMethod": "claude.ai", "configDirectory": reported})
	write(t, cli, "#!/bin/sh\nshift 4\n[ \"$*\" = 'auth login --claudeai' ] && exit 0\nprintf '%s' '"+strings.ReplaceAll(string(status), "'", "'\\''")+"'\n", 0700)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := accountConnectClaude(ctx, accounts.Entry{CLI: cli, Directory: decomposed, Workdir: root}, []string{"CLAUDE_CONFIG_DIR=" + decomposed, "BB_THREAD_ID=thr_example_owner"}); err != nil {
		t.Fatal("full normalized auth status refused", err)
	}
	for _, bad := range []string{"", decomposed, root, reported + "/."} {
		if accountConnectClaudeDirectory(decomposed, bad) {
			t.Fatal("unproven destination accepted", bad)
		}
	}
	other := filepath.Join(root, "other")
	if err := os.Mkdir(other, 0700); err != nil {
		t.Fatal(err)
	}
	if accountConnectClaudeDirectory(decomposed, other) {
		t.Fatal("different native identity accepted")
	}
	alias := filepath.Join(root, "alias")
	if err := os.Symlink(decomposed, alias); err != nil {
		t.Fatal(err)
	}
	if accountConnectClaudeDirectory(alias, alias) {
		t.Fatal("arbitrary symlink alias accepted")
	}
}
func TestAccountConnectAuthorizationURLConsumerParity(t *testing.T) {
	if !accountConnectAuthorizationURL("https://auth.openai.com/oauth/authorize?state=fictional") {
		t.Fatal("supported provider continuation refused")
	}
	for _, bad := range []string{
		"https://auth.openai.com/authorize", "https://auth.openai.com/", "https://auth0.openai.com/oauth/authorize", "https://chatgpt.com/oauth/authorize", "https://auth.openai.com/%6fauth/authorize", "https://auth.openai.com/oauth/authorize/", "http://auth.openai.com/oauth/authorize", "https://auth.openai.com/oauth/authorize#fragment",
	} {
		if accountConnectAuthorizationURL(bad) {
			t.Fatal("unsupported consumer URL admitted", bad)
		}
	}
}
