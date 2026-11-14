package netretry

import (
	"context"
	"errors"
	"os"
	"strconv"
	"testing"
	"time"
)

func TestRealFailureWhileOnlineIsNotRetried(t *testing.T) {
	runs := 0
	err := Do(context.Background(), Options{Online: func(context.Context) bool { return true }}, func() error {
		runs++
		return errors.New("push rejected")
	})
	if err == nil || runs != 1 {
		t.Errorf("runs=%d err=%v", runs, err)
	}
}

func TestRetriesAfterNetworkReturns(t *testing.T) {
	online := false
	runs, checks := 0, 0
	opts := Options{
		Online: func(context.Context) bool {
			checks++
			if checks >= 3 {
				online = true
			}
			return online
		},
		Sleep: func(context.Context, time.Duration) {},
	}
	err := Do(context.Background(), opts, func() error {
		runs++
		if online {
			return nil
		}
		return errors.New("dial tcp: no route")
	})
	if err != nil || runs != 2 {
		t.Errorf("runs=%d err=%v", runs, err)
	}
}

func TestGivesUpWhenNetworkNeverReturns(t *testing.T) {
	opts := Options{MaxWait: time.Millisecond, Online: func(context.Context) bool { return false }, Sleep: func(context.Context, time.Duration) { time.Sleep(2 * time.Millisecond) }}
	err := Do(context.Background(), opts, func() error { return errors.New("offline") })
	if !errors.Is(err, ErrNetworkGone) {
		t.Errorf("err = %v", err)
	}
}

func TestAttemptsCap(t *testing.T) {
	runs, checks := 0, 0
	// Down right after each failure, back on the next check.
	online := func(context.Context) bool { checks++; return checks%2 == 0 }
	opts := Options{Attempts: 3, Online: online, Sleep: func(context.Context, time.Duration) {}}
	err := Do(context.Background(), opts, func() error { runs++; return errors.New("flaky") })
	if err == nil || runs != 3 {
		t.Errorf("runs = %d", runs)
	}
}

// exitWith re-runs this test binary as a command that exits with code.
func exitWith(t *testing.T, code int) []string {
	t.Setenv("NETRETRY_HELPER_EXIT", strconv.Itoa(code))
	return []string{os.Args[0], "-test.run=TestHelperExit"}
}

func TestHelperExit(t *testing.T) {
	if v := os.Getenv("NETRETRY_HELPER_EXIT"); v != "" {
		n, _ := strconv.Atoi(v)
		os.Exit(n)
	}
}

func TestCommandExitCodes(t *testing.T) {
	on := Options{Online: func(context.Context) bool { return true }}
	if code := Command(context.Background(), on, exitWith(t, 7)); code != 7 {
		t.Errorf("code = %d", code)
	}
	off := Options{MaxWait: time.Millisecond, Online: func(context.Context) bool { return false }, Sleep: func(context.Context, time.Duration) { time.Sleep(2 * time.Millisecond) }}
	if code := Command(context.Background(), off, exitWith(t, 1)); code != ExitNetworkGone {
		t.Errorf("code = %d", code)
	}
}

func TestNetProbeCommand(t *testing.T) {
	t.Setenv("LOOPTENDER_NET_PROBE", "exit 0")
	if !DefaultOnline(context.Background()) {
		t.Error("exit 0 probe should be online")
	}
	t.Setenv("LOOPTENDER_NET_PROBE", "exit 3")
	if DefaultOnline(context.Background()) {
		t.Error("exit 3 probe should be offline")
	}
}

// The probe variables are still read under their earlier names, and the
// current name wins when both are set.
func TestNetProbeEarlierName(t *testing.T) {
	t.Setenv("LOOPTENDER_NET_PROBE", "")
	t.Setenv("KEEPGOING_NET_PROBE", "exit 3")
	if DefaultOnline(context.Background()) {
		t.Error("the earlier name was not read")
	}
	if (Probe{}).Check(context.Background()) != CommandFailed {
		t.Error("a probe with no settings did not read the earlier name")
	}
	t.Setenv("LOOPTENDER_NET_PROBE", "exit 0")
	if !DefaultOnline(context.Background()) {
		t.Error("the current name did not win")
	}
}
