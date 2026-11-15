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
		// Long codes: 20 or more characters with a letter and a digit.
		{"key sk-example0123456789abcdef then", "key [hidden] then"},
		{"hash 0123456789abcdef0123456789abcdef.", "hash [hidden]."},
		{"token=EXAMPLE_token_2b9f0c3e1d7a", "token=[hidden]"},
		// Long words, long numbers and short codes stay.
		{"internationalization-and-localization", "internationalization-and-localization"},
		{"order 123456789012345678901234", "order 123456789012345678901234"},
		{"build abc123 of example0123456789", "build abc123 of example0123456789"},
		// IPv4 addresses, with or without a port.
		{"Host 192.0.2.10 is down.", "Host [hidden] is down."},
		{"try 198.51.100.7:8443, then resume", "try [hidden], then resume"},
		// Versions are not addresses.
		{"on v0.2.0 and 1.2.3, not 1.2.3.4.5.6", "on v0.2.0 and 1.2.3, not 1.2.3.4.5.6"},
		{"build 1234.5.6.7", "build 1234.5.6.7"},
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
