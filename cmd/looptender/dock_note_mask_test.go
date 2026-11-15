package main

import "testing"

// Fictional notes only.
func TestDockMaskNote(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Needs a decision on the refund table. Answer, then resume.", "Needs a decision on the refund table. Answer, then resume."},
		{"", ""},
		// BB IDs of every shape.
		{"Answer in thr_example0001, then resume.", "Answer in [hidden], then resume."},
		{"loop_0123456789abcdef0123456789abcdef is the other one", "[hidden] is the other one"},
		{"proj_example env_example-2 host_example", "[hidden] [hidden] [hidden]"},
		{"(see thr_example0001)", "(see [hidden])"},
		// Words that only contain an ID's prefix are kept.
		{"my_thr_x and thread_x and loops_x", "my_thr_x and thread_x and loops_x"},
		// Paths: absolute, home, Windows, quoted, and at the end of a sentence.
		{"Read /example/path/plan.md first.", "Read [hidden] first."},
		{"/example/path", "[hidden]"},
		{"See ~/example/notes.txt and C:\\example\\path.", "See [hidden] and [hidden]."},
		{`config at "/example/path" or (/example/other)`, `config at "[hidden]" or ([hidden])`},
		{"dir=/example/path", "dir=[hidden]"},
		// Slashes inside words or numbers are not paths.
		{"keep and/or migrate 1/2 of it", "keep and/or migrate 1/2 of it"},
		{"a lone / stays", "a lone / stays"},
		// URLs, whole.
		{"Spec: https://example.com/spec?x=1#y.", "Spec: [hidden]."},
		{"see www.example.com/a and ftp://example.com", "see [hidden] and [hidden]"},
		// Email addresses.
		{"Ask person@example.com, or ops+bb@mail.example.com.", "Ask [hidden], or [hidden]."},
		// Mixed.
		{"thr_example0001 at /example/path via https://example.com by a@example.com",
			"[hidden] at [hidden] via [hidden] by [hidden]"},
	} {
		got, masked := dockMaskNote(c.in)
		if got != c.want || masked != (c.in != c.want) {
			t.Errorf("dockMaskNote(%q) = %q, %t; want %q", c.in, got, masked, c.want)
		}
	}
}
