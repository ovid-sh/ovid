//go:build !(linux && amd64)

package tool

func stageInMemory(exe []byte, name string, fd int) *staged { return nil }

// mayExec does not check here: there is nowhere else to put the program,
// so starting it reports what is wrong.
func mayExec(path string) error { return nil }
