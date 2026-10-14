package module

import (
	"fmt"
	"os"
	"time"
)

// DefaultLockTimeout is how long a writer waits for another one to release
// the module lock before it gives up.
const DefaultLockTimeout = 10 * time.Second

// LockTimeoutEnv overrides DefaultLockTimeout with a Go duration ("30s",
// "500ms"); "0" fails at once if the lock is held.
const LockTimeoutEnv = "OVID_LOCK_TIMEOUT"

// LockTimeout is the wait the environment asks for, or the default.
func LockTimeout() (time.Duration, error) {
	v := os.Getenv(LockTimeoutEnv)
	if v == "" {
		return DefaultLockTimeout, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil || d < 0 {
		return 0, fmt.Errorf("%s=%q: want a duration such as 30s or 500ms", LockTimeoutEnv, v)
	}
	return d, nil
}

// LockTimeoutError is returned when another process held the lock for the
// whole wait.
type LockTimeoutError struct {
	Path   string // the locked file, ovid.mod
	Waited time.Duration
}

func (e *LockTimeoutError) Error() string {
	return fmt.Sprintf("another process (another ovid writer, normally) held the lock on %s for %s", e.Path, e.Waited)
}
