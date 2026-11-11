package runner

import "github.com/connorburgess/keepgoing-agents/internal/evidence"

func runnerEvidence(st Status) evidence.Detail {
	d := evidence.Detail{Source: "runner_state"}
	if st.Phase == Finished {
		d.Source = "runner_exit"
		if st.ExitCodeKnown {
			d.ExitCode = evidence.Number(st.ExitCode)
		}
	}
	return evidence.Clean(d)
}
