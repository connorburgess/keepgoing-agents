package bb

import (
	"slices"
	"testing"
)

func TestSendEnvDropsOnlyTheThreadIdentity(t *testing.T) {
	in := []string{"PATH=/example/bin", "BB_THREAD_ID=thr_exampleoperator", "bb_thread_id=thr_examplelower", "BB_PROJECT_ID=proj_example", "BB_THREAD_IDX=kept", "HOME=/example/home"}
	want := []string{"PATH=/example/bin", "BB_PROJECT_ID=proj_example", "BB_THREAD_IDX=kept", "HOME=/example/home"}
	if got := sendEnv(in); !slices.Equal(got, want) {
		t.Fatalf("got %q, want %q", got, want)
	}
}
