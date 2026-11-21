package process

import (
	"context"
	"io"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/looptender/looptender/internal/config"
	"github.com/looptender/looptender/internal/decide"
)

func TestOutputLimitCopy(t *testing.T) {
	for _, size := range []int{maxOutput - 1, maxOutput, maxOutput + 1, 3 * maxOutput} {
		var b boundedOutput
		n, err := io.Copy(&b, strings.NewReader(strings.Repeat("x", size)))
		if err != nil || n != int64(size) || len(b.Bytes()) != min(size, maxOutput) || b.over != (size > maxOutput) {
			t.Fatalf("size %d: copied=%d retained=%d overflow=%v err=%v", size, n, len(b.Bytes()), b.over, err)
		}
	}
}

func TestOutputLimitHelper(t *testing.T) {
	if os.Getenv("LOOPTENDER_TEST_OUTPUT_HELPER") != "1" {
		return
	}
	size, _ := strconv.Atoi(os.Getenv("LOOPTENDER_TEST_OUTPUT_SIZE"))
	_, _ = io.WriteString(os.Stdout, `{"state":"idle"}`+strings.Repeat(" ", size-len(`{"state":"idle"}`)))
	code, _ := strconv.Atoi(os.Getenv("LOOPTENDER_TEST_OUTPUT_EXIT"))
	os.Exit(code)
}

func TestObserveOutputLimit(t *testing.T) {
	for _, tc := range []struct{ size, code int }{{maxOutput, 0}, {maxOutput + 1, 0}, {maxOutput + 1, 75}} {
		a := New(config.Process{StatusCommand: []string{os.Args[0], "-test.run=^TestOutputLimitHelper$"}, Env: map[string]string{
			"LOOPTENDER_TEST_OUTPUT_HELPER": "1", "LOOPTENDER_TEST_OUTPUT_SIZE": strconv.Itoa(tc.size), "LOOPTENDER_TEST_OUTPUT_EXIT": strconv.Itoa(tc.code),
		}, ExitCodes: map[int]string{75: "rate_limit"}})
		obs, err := a.Observe(context.Background())
		if tc.size <= maxOutput {
			if err != nil || obs.State != decide.Idle {
				t.Fatalf("boundary rejected: %v %v", obs, err)
			}
		} else if err == nil || obs.State != decide.Unknown || obs.ErrorClass == decide.ErrRateLimit || strings.Contains(err.Error(), `"state"`) {
			t.Fatalf("overflow must stay unknown without output: %v %v", obs, err)
		}
	}
}
