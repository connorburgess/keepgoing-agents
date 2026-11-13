package bb

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/connorburgess/keepgoing-agents/internal/adapter"
	"github.com/connorburgess/keepgoing-agents/internal/config"
)

func retryFake(out string, err error) (*Adapter, *[]call) {
	a := New(config.BB{Thread: "thr_example123"})
	var calls []call
	a.run = func(_ context.Context, stdin string, args ...string) ([]byte, error) {
		calls = append(calls, call{stdin, args})
		return []byte(out), err
	}
	return a, &calls
}

func TestRetryFailedTurnReadsOnlyTheStructuredResult(t *testing.T) {
	exit1 := errors.New("exit status 1")
	for name, tc := range map[string]struct {
		out         string
		err         error
		want        adapter.TurnRetry
		unsupported bool
		failure     string
	}{
		"sent":                  {`{"threadId":"thr_example123","ok":true,"delivery":"sent","turnRequestId":"req_example1","attempt":2}`, nil, adapter.TurnRetry{Outcome: adapter.TurnRetried, Attempt: 2}, false, ""},
		"queued":                {`{"threadId":"thr_example123","ok":true,"delivery":"queued","turnRequestId":"req_example1","attempt":4,"queuedMessageId":"qm_example","waitingOn":{"kind":"thread-busy"},"sendAt":null}`, nil, adapter.TurnRetry{Outcome: adapter.TurnRetryQueued, Attempt: 4}, false, ""},
		"no failed turn":        {`{"ok":false,"error":{"code":"no_failed_turn","message":"HTTP 409: PRIVATE_MESSAGE"}}`, exit1, adapter.TurnRetry{Outcome: adapter.TurnRetryNothing}, false, ""},
		"already queued":        {`{"ok":false,"error":{"code":"retry_already_queued","message":"PRIVATE_MESSAGE"}}`, exit1, adapter.TurnRetry{Outcome: adapter.TurnRetryAlreadyQueued}, false, ""},
		"older BB":              {`{"ok":false,"error":{"code":"unknown_command","message":"unknown command 'retry'"}}`, exit1, adapter.TurnRetry{}, true, ""},
		"refused":               {`{"ok":false,"error":{"code":"thread_not_found","message":"PRIVATE_MESSAGE"}}`, exit1, adapter.TurnRetry{}, false, "bb thread retry: refused (thread_not_found)"},
		"refused, no code":      {`{"ok":false}`, exit1, adapter.TurnRetry{}, false, "bb thread retry: refused (unknown)"},
		"prose as a code":       {`{"ok":false,"error":{"code":"PRIVATE MESSAGE with spaces"}}`, exit1, adapter.TurnRetry{}, false, "bb thread retry: refused (unknown)"},
		"ok without a delivery": {`{"ok":true}`, nil, adapter.TurnRetry{}, false, "bb thread retry: unrecognised delivery"},
		"unknown delivery":      {`{"ok":true,"delivery":"PRIVATE_DELIVERY","attempt":2}`, nil, adapter.TurnRetry{}, false, "bb thread retry: unrecognised delivery"},
		"not JSON":              {`Thread thr_example123 retrying turn req_example1 (attempt 2)`, nil, adapter.TurnRetry{}, false, "bb thread retry: unreadable result"},
		"empty, failed":         {``, exit1, adapter.TurnRetry{}, false, "bb thread retry: exit status 1"},
		"an array":              {`[]`, nil, adapter.TurnRetry{}, false, "bb thread retry: unreadable result"},
		"negative attempt":      {`{"ok":true,"delivery":"sent","attempt":-3}`, nil, adapter.TurnRetry{Outcome: adapter.TurnRetried}, false, ""},
	} {
		a, calls := retryFake(tc.out, tc.err)
		got, err := a.RetryFailedTurn(context.Background())
		var unsupported adapter.ErrUnsupported
		switch {
		case tc.unsupported:
			if !errors.As(err, &unsupported) {
				t.Errorf("%s: %v", name, err)
			}
		case tc.failure != "":
			if err == nil || err.Error() != tc.failure || errors.As(err, &unsupported) {
				t.Errorf("%s: %v", name, err)
			}
		default:
			if err != nil || got != tc.want {
				t.Errorf("%s: %+v %v", name, got, err)
			}
		}
		if err != nil && (strings.Contains(err.Error(), "PRIVATE") || got != (adapter.TurnRetry{})) {
			t.Errorf("%s: leaked or returned a result with an error: %+v %v", name, got, err)
		}
		// One command, addressed to the configured thread, with no text on stdin.
		c := (*calls)[0]
		if len(*calls) != 1 || c.stdin != "" || strings.Join(c.args, " ") != "thread retry thr_example123 --reason "+retryReason+" --json" {
			t.Errorf("%s: %+v", name, *calls)
		}
	}
	var _ adapter.TurnRetrier = New(config.BB{Thread: "thr_example123"})
}

func TestStillFailedReadsOnlyTheThreadStatus(t *testing.T) {
	for name, tc := range map[string]struct {
		out    string
		err    error
		want   bool
		failed bool
	}{
		"in error":                  {`{"thread":{"status":"error"}}`, nil, true, false},
		"in error, retry queued":    {`{"thread":{"status":"error","queuedMessageCount":1}}`, nil, false, false},
		"active again":              {`{"thread":{"status":"active"}}`, nil, false, false},
		"idle again":                {`{"thread":{"status":"idle"}}`, nil, false, false},
		"archived":                  {`{"thread":{"status":"error","archivedAt":1}}`, nil, false, false},
		"unknown status":            {`{"thread":{"status":"PRIVATE_STATUS"}}`, nil, false, false},
		"not found":                 {`{"ok":false,"error":{"code":"thread_not_found","message":"PRIVATE_MESSAGE"}}`, errors.New("exit status 1"), false, true},
		"not JSON":                  {`PRIVATE_MESSAGE`, nil, false, true},
		"command failed, no output": {``, errors.New("exit status 1"), false, true},
	} {
		a, calls := retryFake(tc.out, tc.err)
		got, err := a.StillFailed(context.Background())
		if got != tc.want || (err != nil) != tc.failed || (err != nil && strings.Contains(err.Error(), "PRIVATE")) {
			t.Errorf("%s: %t %v", name, got, err)
		}
		// One status read: no log, no children, no interactions, no send.
		if len(*calls) != 1 || strings.Join((*calls)[0].args, " ") != "thread show thr_example123 --json" || (*calls)[0].stdin != "" {
			t.Errorf("%s: %+v", name, *calls)
		}
	}
}
