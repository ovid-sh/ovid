package tool

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"

	"ovid/internal/module"
)

// tmpHint is what to do when run or test cannot place or start a program.
const tmpHint = "run and test write the program to a temporary directory and execute it there: set TMPDIR to a directory that is writable and not mounted noexec, or mount /proc so the program can run from memory"

// staged is a compiled program placed where it can be executed.
type staged struct {
	path  string   // what to exec
	extra *os.File // when set, the program itself: pass it as the child's descriptor fd
	done  func()
}

// stage writes exe under dir as name and checks that it may be executed
// there. Where dir is "" (there is none) or forbids exec (a noexec mount), it holds
// the program in memory instead, on hosts that can execute one from there;
// fd is the descriptor number the child will then have it on.
func stage(exe []byte, dir, name string, fd int) (*staged, error) {
	err := fmt.Errorf("no temporary directory")
	if dir != "" {
		bin := filepath.Join(dir, name)
		if _, err = module.WriteFiles(map[string][]byte{bin: exe}, 0o755); err == nil {
			if err = syscall.Access(bin, 1); err == nil { // X_OK: fails on a noexec mount
				return &staged{path: bin, done: func() {}}, nil
			}
			err = fmt.Errorf("%s may not be executed: %v", bin, err)
		}
	}
	if s := stageInMemory(exe, name, fd); s != nil {
		return s, nil
	}
	return nil, err
}
