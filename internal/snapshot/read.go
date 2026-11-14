package snapshot

import "io"

// ReadFile reads the whole of path through Open.
func ReadFile(path string) ([]byte, error) {
	f, err := Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// ReadRecord reads the whole of path through OpenRecord.
func ReadRecord(path string) ([]byte, error) {
	f, err := OpenRecord(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}
