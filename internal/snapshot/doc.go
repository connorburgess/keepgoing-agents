// Package snapshot reads and atomically replaces small runtime records so that
// a reader and a writer in different processes do not block each other. The
// rules differ only on Windows, where an ordinary open handle prevents the
// file from being replaced.
//
// # Choosing Open or OpenRecord
//
// On Windows, while a record is being replaced by rename, its existing name is
// reported missing for a moment. OpenRecord, ReadRecord and LstatRecord wait
// that out only when a persistent zero-byte marker says the record has been
// published. Replace and ReplaceStrict create path+".present" before its first
// rename and hold it against deletion through publication. Remove acquires
// DELETE access to the marker first, then deletes the record before the marker. Never-written records
// return missing immediately. A marked record still absent after about 255 ms
// returns an unavailable error, never a not-found that could reset state.
// Open, ReadFile and Lstat do not wait out not-found. Choose by the harm a
// false "missing" would do:
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
// Older records gain a marker on their first save by this build. Concurrent
// writers from an older build remain outside this guarantee. A failed first
// publication can leave a marker; reads then refuse until the record is
// restored or deliberately removed with its marker.
//
// Elsewhere both kinds are the plain operating-system calls.
package snapshot
