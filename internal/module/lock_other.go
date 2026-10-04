//go:build !unix

package module

import "time"

// LockWait is a no-op where flock is not available: it never waits, so it
// never times out.
func LockWait(dir string, timeout time.Duration, waiting func(path string)) (unlock func(), err error) {
	if _, err := Find(dir); err != nil {
		return nil, err
	}
	return func() {}, nil
}
