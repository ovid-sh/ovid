//go:build !(linux && amd64)

package tool

import "os"

// Confinement needs seccomp and Landlock: Linux. Programs are linux/amd64
// binaries, so elsewhere there is nothing to confine.
const confineSupported = false

func landlockAvailable() bool { return false }

func confineAndExec(c *confineSpec, bin string, argv []string) { os.Exit(114) }
