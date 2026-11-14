package control

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"regexp"
	"time"
)

// Question is the shared shape of a question the loop raises for a person
// (version 1). No words are stored: the text is rendered from the kind, the
// option and the evidence. There is one open question per loop; raising a
// new one withdraws the old one.
type Question struct {
	Version  int       `json:"version"`
	ID       string    `json:"id"`
	Kind     string    `json:"kind"`
	RaisedAt time.Time `json:"raisedAt"`
	Build    string    `json:"build"`
	// Holds is "loop" when the loop sends nothing until it is answered.
	Holds string `json:"holds"`
	// Basis identifies what the question is about: a hash over the kind,
	// the evidence and the control fields the answer depends on. An answer
	// reviewed against another basis is stale.
	Basis     string     `json:"basis"`
	Evidence  []Evidence `json:"evidence"`
	Options   []Option   `json:"options"`
	Answerers []string   `json:"answerers"`
}

// Evidence is one fact behind a question. Type is count, time, route or class.
type Evidence struct {
	Key   string `json:"key"`
	Type  string `json:"type"`
	Value string `json:"value"`
}

// Option is one answer, as a fixed code. Primary marks the suggested answer,
// Review that the answer is reviewed before it is applied, Sends that it
// sends something to a conversation.
type Option struct {
	ID      string `json:"id"`
	Primary bool   `json:"primary"`
	Review  bool   `json:"review"`
	Sends   bool   `json:"sends"`
}

// Question kinds this build raises.
const (
	QuestionRefused    = "refused"
	QuestionRestricted = "restricted"
)

// QuestionIDPattern is the form of a question ID.
var QuestionIDPattern = regexp.MustCompile(`^q_[0-9a-f]{32}$`)

// QuestionOptions are the answers each kind offers, in order.
func QuestionOptions(kind string) []Option {
	switch kind {
	case QuestionRefused:
		return []Option{{ID: "continue", Primary: true, Sends: true}, {ID: "fresh_session", Review: true, Sends: true}, {ID: "pause"}}
	case QuestionRestricted:
		return []Option{{ID: "resume", Review: true, Sends: true}, {ID: "pause"}, {ID: "stop"}}
	}
	return nil
}

// NewQuestion raises a question of kind about target with its evidence,
// against the control record f it will be saved into. Only a person may
// answer a refusal or a restriction.
func NewQuestion(kind, build, target string, at time.Time, evidence []Evidence, f File) (*Question, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return nil, err
	}
	q := &Question{Version: 1, ID: "q_" + hex.EncodeToString(b[:]), Kind: kind, RaisedAt: at.UTC(), Build: build, Holds: "loop",
		Evidence: evidence, Options: QuestionOptions(kind), Answerers: []string{"person"}}
	q.Basis = QuestionBasis(kind, target, evidence, f)
	return q, nil
}

// QuestionBasis hashes what an answer depends on: the kind, the evidence,
// the target and the stop and pause flags.
func QuestionBasis(kind, target string, evidence []Evidence, f File) string {
	h := sha256.New()
	fmt.Fprintf(h, "kind=%s\ntarget=%s\nstop=%t\npaused=%t\n", kind, target, f.Stop, f.Paused)
	for _, e := range evidence {
		fmt.Fprintf(h, "evidence=%s|%s|%s\n", e.Key, e.Type, e.Value)
	}
	return hex.EncodeToString(h.Sum(nil))
}
