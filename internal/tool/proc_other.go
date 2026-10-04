//go:build !linux

package tool

import (
	"os"
	"time"
)

func runTraced(bin string, args []string, out *os.File, timeout time.Duration) (procResult, bool) {
	return procResult{}, false
}
