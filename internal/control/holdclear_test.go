package control

import (
	"errors"
	"testing"
)

func TestCheckHoldClearer(t *testing.T) {
	const target, operator = "thr_exampletarget", "thr_exampleoperator"
	for _, tc := range []struct {
		name   string
		in     HoldClearer
		refuse bool
	}{
		{"a person at a terminal", HoldClearer{TargetThread: target}, false},
		{"an operator thread that is not the target", HoldClearer{Caller: operator, TargetThread: target}, false},
		{"the target thread", HoldClearer{Caller: target, TargetThread: target}, true},
		{"the target thread when it also owns the loop", HoldClearer{Caller: target, TargetThread: target}, true},
		{"a headless target, no thread known", HoldClearer{}, false},
		{"a process this loop's supervisor started", HoldClearer{Supervised: true}, true},
		{"a supervised process that also has the operator's thread", HoldClearer{Caller: operator, TargetThread: target, Supervised: true}, true},
		{"a thread caller while the target thread is not known", HoldClearer{Caller: target}, false},
	} {
		err := CheckHoldClearer(tc.in)
		if (err != nil) != tc.refuse {
			t.Errorf("%s: refused=%v, want %v (%v)", tc.name, err != nil, tc.refuse, err)
		}
		if err != nil && !errors.Is(err, ErrSupervisedSession) {
			t.Errorf("%s: %v is not ErrSupervisedSession", tc.name, err)
		}
	}
}
