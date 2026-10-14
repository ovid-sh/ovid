//go:build !(linux && amd64)

package tool

import (
	"syscall"
	"time"
)

func runTraced(bin string, args []string, pio procIO, timeout time.Duration) (procResult, bool) {
	return procResult{}, false
}

func signalHint(sig syscall.Signal) string { return "" }
