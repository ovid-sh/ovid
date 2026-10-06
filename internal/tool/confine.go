package tool

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// A confined program runs under two things the kernel enforces: a seccomp
// filter that allows exactly the system calls its build receipt lists and
// kills the process on any other, and, where the kernel has Landlock, a
// file system it can read everywhere and write only in one directory,
// which is its working directory. ovid applies them by starting itself
// as a launcher (confineEnv set) that puts the rules in place and then
// replaces itself with the program.
type confineSpec struct {
	Syscalls []int64 `json:"syscalls"` // the program's list; execve and exit_group are added for the launcher
	Writable string  `json:"writable"` // the one writable directory, the program's cwd; "" for none
	Landlock bool    `json:"landlock"` // whether the kernel can confine the file system
	Argv0    string  `json:"argv0"`    // the program's name for itself
	// StatusFD is where the launcher reports a failure to set up: a pipe
	// to the parent, closed on exec, so that nothing read means the program
	// was started as asked.
	StatusFD int `json:"status_fd"`

	status  *os.File // the parent's end of that pipe
	statusW *os.File
}

// confineEnv is the variable that makes ovid a launcher.
const confineEnv = "OVID_CONFINE"

// newConfine describes how to confine a program with the given system
// calls to the directory writable; "" means it may write nowhere.
func newConfine(syscalls []int64, writable, argv0 string) *confineSpec {
	return &confineSpec{Syscalls: syscalls, Writable: writable, Landlock: landlockAvailable(), Argv0: argv0}
}

// writableDir makes the one directory a confined program may write in, or
// returns "" when there is nowhere to make it (no temporary directory):
// the program then runs with nowhere to write, and still runs.
func writableDir() string {
	dir, err := os.MkdirTemp("", "ovid-writable-")
	if err != nil {
		return ""
	}
	return dir
}

// keepWritable returns the writable directory if the program left anything
// in it, and removes it and returns "" otherwise: what a program wrote is
// the point of running it, and an empty directory for every run is not.
// Once removed, it stays "".
func (c *confineSpec) keepWritable() string {
	if c.Writable == "" {
		return ""
	}
	if ents, err := os.ReadDir(c.Writable); err == nil && len(ents) == 0 {
		os.Remove(c.Writable)
		c.Writable = ""
		return ""
	}
	return c.Writable
}

// applied names what the kernel will enforce, for the record.
func (c *confineSpec) applied() []string {
	out := []string{"seccomp"}
	if c.Landlock {
		out = append(out, "landlock")
	}
	return out
}

// encode is the spec as the launcher's environment variable: JSON, so
// that a name or a path may hold anything.
func (c *confineSpec) encode() string {
	b, _ := json.Marshal(c)
	return string(b)
}

func decodeConfine(s string) (*confineSpec, error) {
	var c confineSpec
	if err := json.Unmarshal([]byte(s), &c); err != nil {
		return nil, err
	}
	return &c, nil
}

// openStatus makes the pipe the launcher reports a failure on. The write
// end is the extra file at index i of the command (fd 3+i in the child).
func (c *confineSpec) openStatus(i int) (*os.File, error) {
	// A pipe from an attempt that did not start (ptrace refused, and the
	// plain path tries again) would leak.
	c.closeStatus()
	r, w, err := os.Pipe()
	if err != nil {
		return nil, err
	}
	c.status, c.statusW, c.StatusFD = r, w, 3+i
	return w, nil
}

func (c *confineSpec) closeStatus() {
	if c.status != nil {
		c.status.Close()
		c.statusW.Close()
		c.status, c.statusW = nil, nil
	}
}

// unenforceable is why c cannot do what the record would claim, or nil.
// Without Landlock the file system is not made read-only, so "nowhere to
// write" needs a directory to point the program at.
func (c *confineSpec) unenforceable() error {
	if c.Writable == "" && !c.Landlock {
		return fmt.Errorf("no temporary directory for the program's writable directory, and this kernel has no Landlock to make the file system read-only instead")
	}
	return nil
}

// setupError, once the launcher has exited or become the program, is what
// the launcher reported, or nil: it writes only when it could not set up
// the confinement, and the pipe closes on exec.
func (c *confineSpec) setupError() error {
	if c.status == nil {
		return nil
	}
	c.statusW.Close()
	msg, _ := io.ReadAll(c.status)
	c.status.Close()
	c.status, c.statusW = nil, nil
	if len(msg) == 0 {
		return nil
	}
	return fmt.Errorf("could not confine the program: %s", strings.TrimSpace(string(msg)))
}

// confineHint is what to do when the kernel killed the program for a system
// call outside its list.
const confineHint = "the program made a system call that its build receipt does not list, and confinement (the default; --no-confine turns it off) kills it for that; the list is in `ovid build`'s syscalls"

// The launcher runs before main or any test does, in every binary that
// links this package: ovid, and the test binaries. A process started with
// confineEnv set is a launcher and nothing else. Were the variable left to
// main to notice, a test binary would ignore it and run its tests again,
// and those tests start launchers: that forked without bound once (#134).
func init() {
	if os.Getenv(confineEnv) != "" {
		ConfineMain()
		// ConfineMain does not return with the variable set; this is the
		// last line of defence should that ever change.
		fmt.Fprintln(os.Stderr, "ovid: "+confineEnv+" is set but the launcher returned")
		os.Exit(111)
	}
}

// ConfineMain is the launcher: when ovid is started with confineEnv set, it
// confines this process and replaces it with the program named after "--",
// and never returns. Otherwise it returns false at once. The package's init
// calls it; a main need not.
func ConfineMain() bool {
	spec := os.Getenv(confineEnv)
	if spec == "" {
		return false
	}
	c, err := decodeConfine(spec)
	if err != nil {
		os.Exit(111)
	}
	argv := os.Args[len(os.Args):]
	for i, a := range os.Args {
		if a == "--" {
			argv = os.Args[i+1:]
			break
		}
	}
	if len(argv) == 0 {
		os.Exit(111)
	}
	confineAndExec(c, argv[0], append([]string{c.Argv0}, argv[1:]...))
	os.Exit(114)
	return true
}
