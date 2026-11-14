// Package snapshot reads and atomically replaces small runtime records so that
// a reader and a writer in different processes do not block each other. The
// rules differ only on Windows, where an ordinary open handle prevents the
// file from being replaced.
//
// # Choosing Open or OpenRecord
//
// On Windows, while a record is being replaced by rename, its existing name is
// reported missing for a moment. OpenRecord, ReadRecord and LstatRecord wait
// that out (up to 250 ms) before they report a record missing; Open, ReadFile
// and Lstat report it at once. Waiting costs every read of a record that
// really is missing, so choose by what a false "missing" would do:
//
//   - Use the Record calls when believing an existing file missing could cause
//     a send or a duplicate send, a handoff or a change of route, a second
//     supervisor on one session, a stop or pause not being seen, or a count,
//     cap or history starting again from zero.
//   - Use the plain calls when a false "missing" only changes what is shown,
//     or fails safe; when missing is the normal case and the file is polled
//     (the daemon endpoint); or when the file is created once and never
//     replaced by rename, so there is no window.
//
// Elsewhere both kinds are the plain operating-system calls.
package snapshot
