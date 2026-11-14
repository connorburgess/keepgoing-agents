package main

import (
	"context"
	"crypto/sha256"
	"errors"
	"io"
	"os"
)

// Disk observations only: never execute the recorded path or expose its contents,
// digest, build flags, or path. The invoking CLI's on-disk file can differ from
// the image already loaded in this process.
type serviceBinary struct {
	State      string `json:"state"`      // available, missing, non_regular, no_execute_bits, unavailable
	Comparison string `json:"comparison"` // match, different, unknown
	Message    string `json:"message"`
}

const serviceBinaryLimit = 128 << 20

func inspectServiceBinary(ctx context.Context, recorded, current string) serviceBinary {
	digest, state := serviceBinaryDigest(ctx, recorded)
	r := serviceBinary{State: state, Comparison: "unknown"}
	switch state {
	case "missing":
		r.Message = "The recorded executable path does not resolve to an existing file."
	case "non_regular":
		r.Message = "The recorded executable path does not resolve to a regular file."
	case "no_execute_bits":
		r.Message = "The recorded executable file has no Unix execute bits set."
	case "available":
		other, otherState := serviceBinaryDigest(ctx, current)
		if otherState == "available" {
			r.Comparison = "different"
			if digest == other {
				r.Comparison = "match"
			}
		}
		r.Message = "The recorded file is readable with Unix execute bits. Comparison concerns file contents on disk, not a running process, build age, authenticity, or guaranteed execution."
	default:
		r.Message = "The recorded executable could not be inspected within the file-size bound, or it changed during inspection. Raw paths and errors are withheld."
	}
	return r
}

func serviceBinaryDigest(ctx context.Context, path string) ([sha256.Size]byte, string) {
	var zero [sha256.Size]byte
	if ctx.Err() != nil {
		return zero, "unavailable"
	}
	before, err := os.Stat(path) // Follow installed-binary symlinks; check target identity below.
	if err != nil {
		if os.IsNotExist(err) {
			return zero, "missing"
		}
		return zero, "unavailable"
	}
	if !before.Mode().IsRegular() {
		return zero, "non_regular"
	}
	if before.Mode().Perm()&0111 == 0 {
		return zero, "no_execute_bits"
	}
	if before.Size() < 0 || before.Size() > serviceBinaryLimit {
		return zero, "unavailable"
	}
	// Nonblocking open prevents a concurrent replacement with a FIFO from
	// waiting for a writer. The descriptor must still match the regular file.
	f, err := openServiceBinary(path)
	if err != nil {
		return zero, "unavailable"
	}
	defer f.Close()
	opened, err := f.Stat()
	if err != nil || !sameServiceBinary(before, opened) {
		return zero, "unavailable"
	}
	digest, err := hashServiceBinary(ctx, f, before.Size())
	if err != nil {
		return zero, "unavailable"
	}
	after, err := f.Stat()
	if err != nil || !sameServiceBinary(before, after) {
		return zero, "unavailable"
	}
	resolved, err := os.Stat(path)
	if err != nil || !sameServiceBinary(before, resolved) {
		return zero, "unavailable"
	}
	return digest, "available"
}

func sameServiceBinary(a, b os.FileInfo) bool {
	return a.Mode().IsRegular() && b.Mode().IsRegular() && os.SameFile(a, b) && a.Size() == b.Size() && a.Mode() == b.Mode() && a.ModTime().Equal(b.ModTime())
}

func hashServiceBinary(ctx context.Context, r io.Reader, size int64) ([sha256.Size]byte, error) {
	var digest [sha256.Size]byte
	if size < 0 || size > serviceBinaryLimit {
		return digest, errors.New("executable size outside inspection bound")
	}
	h := sha256.New()
	buf := make([]byte, 64<<10)
	var total int64
	for total < size {
		if err := ctx.Err(); err != nil {
			return digest, err
		}
		n := int64(len(buf))
		if size-total < n {
			n = size - total
		}
		read, err := io.ReadFull(r, buf[:n])
		if err != nil {
			return digest, err
		}
		h.Write(buf[:read])
		total += int64(read)
	}
	if err := ctx.Err(); err != nil {
		return digest, err
	}
	copy(digest[:], h.Sum(nil))
	return digest, nil
}
