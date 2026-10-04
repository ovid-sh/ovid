package std

import (
	"io/fs"
	"regexp"
	"strings"
	"testing"
)

// TestSyscallsNamed: every syscall in the shipped packages names its number
// with a SYS_ constant. Only the shipped ovid/io may call syscall, so those
// constants are the whole list of system calls a program can make, and a
// seccomp allowlist built from them must not miss one (#102).
func TestSyscallsNamed(t *testing.T) {
	call := regexp.MustCompile(`\bsyscall\(\s*([^,)]*)`)
	n := 0
	err := fs.WalkDir(FS, "ovid", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".ov") {
			return err
		}
		b, err := fs.ReadFile(FS, p)
		if err != nil {
			return err
		}
		for i, line := range strings.Split(string(b), "\n") {
			if c, _, _ := strings.Cut(line, "//"); call.MatchString(c) {
				n++
				if a := call.FindStringSubmatch(c)[1]; !strings.HasPrefix(a, "SYS_") {
					t.Errorf("%s:%d: syscall(%s, ...): name the number with a SYS_ constant", p, i+1, a)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no syscall found in the shipped packages")
	}
}
