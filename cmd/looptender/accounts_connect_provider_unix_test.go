//go:build darwin || linux

package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/looptender/looptender/internal/accounts"
)

func TestAccountConnectClaudeNativeCommandContract(t *testing.T) {
	root := t.TempDir()
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
			root := t.TempDir()
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
	root := t.TempDir()
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
