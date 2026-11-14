package main

import (
	"encoding/json"
	"fmt"
	"testing"
)

// Reuse the existing phrase-table inputs, without the reader, filesystem,
// daemon challenge, provider observation, routes or retained history.
func scaleDockInputs(n int, mixed bool) []dockInput {
	inputs := make([]dockInput, n)
	for i := range inputs {
		ev := dockEvidence{controls: &clientControls{Enabled: true}}
		if mixed {
			switch i % 4 {
			case 1:
				ev.controls.Paused = true
			case 2:
				ev.controls.AwaitingUser = true
			case 3:
				ev.noSnap, ev.noStatus, ev.issues = true, true, []string{"cancelled"}
			}
		}
		in := ev.input()
		in.Entry.ID, in.Entry.Loop = fmt.Sprintf("loop_%032x", i+1), fmt.Sprintf("example-%03d", i)
		if in.Client.Snapshot != nil {
			in.Client.Snapshot.Loop = in.Entry.Loop
		}
		inputs[i] = in
	}
	return inputs
}

func BenchmarkDockProjection(b *testing.B) {
	for _, mixed := range []bool{false, true} {
		for _, n := range []int{1, 10, 100, 128} {
			b.Run(fmt.Sprintf("mixed=%t/registrations=%d", mixed, n), func(b *testing.B) {
				inputs := scaleDockInputs(n, mixed)
				b.ReportAllocs()
				b.ResetTimer()
				size := 0
				for i := 0; i < b.N; i++ {
					report := buildDockReport(inputs, dockNow, dockDefaultRefresh, defaultStaleAfter, false, true, 0, true)
					if len(report.Loops) != n {
						b.Fatal("projection lost a loop")
					}
					body, err := json.Marshal(report)
					if err != nil {
						b.Fatal(err)
					}
					size = len(body)
				}
				b.StopTimer()
				b.ReportMetric(float64(size), "output-bytes/op")
			})
		}
	}
}

func TestScaleDockProjectionKeepsUnavailableEvidence(t *testing.T) {
	inputs := scaleDockInputs(4, true)
	report := buildDockReport(inputs, dockNow, dockDefaultRefresh, defaultStaleAfter, false, true, 0, false)
	if len(report.Loops) != 4 || report.Unread != 1 || report.AllClear || report.Complete {
		t.Fatalf("loops=%d unread=%d allClear=%t complete=%t", len(report.Loops), report.Unread, report.AllClear, report.Complete)
	}
	if inputs[1].Client.Snapshot.Controls.Paused != true || inputs[3].Client.Snapshot != nil {
		t.Fatal("projection changed source evidence")
	}
}
