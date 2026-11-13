package snapshot

import "errors"

// ErrNotRegular is returned by OpenRegular for anything but a regular file.
var ErrNotRegular = errors.New("not a regular file")
