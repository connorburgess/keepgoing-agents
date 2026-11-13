package decide

import "testing"

func TestRetryNoticeDead(t *testing.T) {
	certain := RetryService{DecisionKnown: true, Declined: true, ForCurrentTurn: true, PendingKnown: true}
	if !RetryNoticeDead(certain) {
		t.Fatal("a certain decline with nothing pending kept the notice")
	}
	for name, change := range map[string]func(*RetryService){
		"no decision read":           func(s *RetryService) { s.DecisionKnown = false },
		"the decision was to retry":  func(s *RetryService) { s.Declined = false },
		"an earlier turn's decision": func(s *RetryService) { s.ForCurrentTurn = false },
		"pending list not read":      func(s *RetryService) { s.PendingKnown = false },
		"one retry pending":          func(s *RetryService) { s.Pending = 1 },
		"several retries pending":    func(s *RetryService) { s.Pending = 4 },
		"nothing known":              func(s *RetryService) { *s = RetryService{} },
		"only a pending list":        func(s *RetryService) { *s = RetryService{PendingKnown: true} },
		"declined, facts not known":  func(s *RetryService) { *s = RetryService{Declined: true, ForCurrentTurn: true, PendingKnown: true} },
	} {
		s := certain
		change(&s)
		if RetryNoticeDead(s) {
			t.Errorf("%s: the notice was dropped", name)
		}
	}
}
