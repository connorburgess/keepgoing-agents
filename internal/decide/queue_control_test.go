package decide

import "testing"

// Queue control, never merge: this test fails on purpose so the merge queue
// can be shown refusing or ejecting a pull request whose checks are red.
func TestQueueControlFailsOnPurpose(t *testing.T) {
	t.Fatal("queue control: this test fails on purpose; never merge this change")
}
