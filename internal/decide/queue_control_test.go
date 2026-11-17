package decide

import (
	"os"
	"testing"
)

// Queue control, never merge: this test passes on a pull request and fails
// only in a merge queue run, so the queue's own run is what rejects it.
func TestQueueControlFailsOnlyInTheMergeQueue(t *testing.T) {
	if os.Getenv("GITHUB_EVENT_NAME") == "merge_group" {
		t.Fatal("queue control: fails on purpose in the merge queue; never merge this change")
	}
}
