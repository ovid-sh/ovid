package tool

import (
	"fmt"
	"os"
	"strconv"
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
	syscalls []int64 // the program's list; execve is added for the launcher
	writable string  // the one writable directory, the program's cwd
	landlock bool    // whether the kernel can confine the file system
	argv0    string  // the program's name for itself
}

// confineEnv is the variable that makes ovid a launcher.
const confineEnv = "OVID_CONFINE"

// newConfine describes how to confine a program with the given system
// calls to the directory writable; "" means it may write nowhere.
func newConfine(syscalls []int64, writable, argv0 string) *confineSpec {
	return &confineSpec{syscalls: syscalls, writable: writable, landlock: landlockAvailable(), argv0: argv0}
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
	if c.writable == "" {
		return ""
	}
	if ents, err := os.ReadDir(c.writable); err == nil && len(ents) == 0 {
		os.Remove(c.writable)
		c.writable = ""
		return ""
	}
	return c.writable
}

// applied names what the kernel will enforce, for the record.
func (c *confineSpec) applied() []string {
	out := []string{"seccomp"}
	if c.landlock {
		out = append(out, "landlock")
	}
	return out
}

// encode is the spec as the launcher's environment variable.
func (c *confineSpec) encode() string {
	var nums []string
	for _, n := range c.syscalls {
		nums = append(nums, strconv.FormatInt(n, 10))
	}
	ll := "0"
	if c.landlock {
		ll = "1"
	}
	return strings.Join([]string{strings.Join(nums, ","), ll, c.argv0, c.writable}, ";")
}

func decodeConfine(s string) (*confineSpec, error) {
	parts := strings.SplitN(s, ";", 4)
	if len(parts) != 4 {
		return nil, fmt.Errorf("bad confine spec")
	}
	c := &confineSpec{landlock: parts[1] == "1", argv0: parts[2], writable: parts[3]}
	for _, f := range strings.Split(parts[0], ",") {
		if f == "" {
			continue
		}
		n, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, err
		}
		c.syscalls = append(c.syscalls, n)
	}
	return c, nil
}

// confineHint is what to do when the kernel killed the program for a system
// call outside its list.
const confineHint = "the program made a system call that its build receipt does not list, and --confine kills it for that; the list is in `ovid build`'s syscalls"

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
	confineAndExec(c, argv[0], append([]string{c.argv0}, argv[1:]...))
	os.Exit(114)
	return true
}
