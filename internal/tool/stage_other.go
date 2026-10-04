//go:build !(linux && amd64)

package tool

func stageInMemory(exe []byte, name string, fd int) *staged { return nil }
