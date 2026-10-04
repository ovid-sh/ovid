//go:build !linux

package tool

import "time"

func runTraced(bin string, args []string, pio procIO, timeout time.Duration) (procResult, bool) {
	return procResult{}, false
}
