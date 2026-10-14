//go:build linux && amd64

package tool

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

const tempDirProg = `package demo

import ovid/io

func TestPasses(io *ovid/io.Cap) i64 {
  return 0
}

func main(io *ovid/io.Cap) i64 {
  ovid/io.Print(io, "ran\n")
  return 3
}
`

// helperRun is ovid run on dir in a child process (TestHelperRun), with
// extra environment.
func helperRun(t *testing.T, dir string, env ...string) (string, string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperRun$")
	cmd.Env = append(append(os.Environ(), "OVID_HELPER_RUN="+dir), env...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return stdout.String(), stderr.String(), code
}

// TestRunWithoutTempDir: with no temporary directory, run executes the
// program from memory.
func TestRunWithoutTempDir(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc to execute a program in memory through")
	}
	dir := mkmod(t, demo(tempDirProg))
	out, errs, code := helperRun(t, dir, "TMPDIR="+filepath.Join(dir, "missing"))
	if out != "ran\n" || code != 3 {
		t.Fatalf("exit %d, stdout %q, stderr %q; want the program's output and its exit 3", code, out, errs)
	}
}

// TestTestWithoutTempDir: test needs no temporary directory either. The
// program runs from memory and its output comes back through a pipe.
func TestTestWithoutTempDir(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc to execute a program in memory through")
	}
	dir := mkmod(t, demo(tempDirProg))
	t.Setenv("TMPDIR", filepath.Join(dir, "missing"))
	var b bytes.Buffer
	code := Test(dir, "", false, &b)
	rs := lines(t, b.String())
	if code != ExitOK || len(rs) != 2 || rs[0]["id"] != "fn:demo.TestPasses" || rs[0]["ok"] != true || rs[1]["passed"] != float64(1) {
		t.Fatalf("exit %d:\n%s", code, b.String())
	}
}

// TestStageInMemory: without a directory the program is held in memory,
// named by the descriptor the child will have it on, and not inheritable.
func TestStageInMemory(t *testing.T) {
	if _, err := os.Stat("/proc/self/fd"); err != nil {
		t.Skip("no /proc to execute a program in memory through")
	}
	st, err := stage([]byte("x"), "", "p", 5)
	if err != nil {
		t.Fatal(err)
	}
	defer st.done()
	if st.path != "/proc/self/fd/5" || st.extra == nil {
		t.Fatalf("%+v", st)
	}
	// This descriptor must not leak into the program or anything else
	// started meanwhile; the child gets its own copy as fd 5.
	fl, _, e := syscall.Syscall(syscall.SYS_FCNTL, st.extra.Fd(), syscall.F_GETFD, 0)
	if e != 0 || fl&syscall.FD_CLOEXEC == 0 {
		t.Fatalf("the in-memory program's descriptor is inheritable: flags %#x, %v", fl, e)
	}
}

// TestNoexecTempDir runs ovid run and ovid test with TMPDIR on a tmpfs
// mounted noexec, the usual shape of a sandbox's /tmp. The mount needs a
// user namespace, which this re-executes itself into; where the host does
// not allow one, the test is skipped.
func TestNoexecTempDir(t *testing.T) {
	dir := mkmod(t, demo(tempDirProg))
	tmp := t.TempDir()
	cmd := exec.Command(os.Args[0], "-test.run=^TestHelperNoexec$")
	cmd.Env = append(os.Environ(), "OVID_HELPER_NOEXEC="+dir, "OVID_HELPER_TMP="+tmp)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Cloneflags:  syscall.CLONE_NEWUSER | syscall.CLONE_NEWNS,
		UidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getuid(), Size: 1}},
		GidMappings: []syscall.SysProcIDMap{{ContainerID: 0, HostID: os.Getgid(), Size: 1}},
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); !ok && err != nil {
		t.Skipf("no user namespace here: %v", err)
	} else if ok && ee.ExitCode() == 77 {
		t.Skipf("cannot mount in a user namespace here: %s", stderr.String())
	}
	// The helper prints test's records, then the program's own output, and
	// exits with the program's code.
	got := stdout.String()
	i := strings.Index(got, "ran\n")
	if i < 0 || cmd.ProcessState.ExitCode() != 3 {
		t.Fatalf("exit %d, stdout %q, stderr %q", cmd.ProcessState.ExitCode(), got, stderr.String())
	}
	rs := lines(t, got[:i])
	if len(rs) != 2 || rs[0]["id"] != "fn:demo.TestPasses" || rs[0]["ok"] != true || rs[1]["passed"] != float64(1) {
		t.Fatalf("test under a noexec TMPDIR:\n%s", got[:i])
	}
}

// TestHelperNoexec is the child of TestNoexecTempDir; alone it does nothing.
func TestHelperNoexec(t *testing.T) {
	dir, tmp := os.Getenv("OVID_HELPER_NOEXEC"), os.Getenv("OVID_HELPER_TMP")
	if dir == "" {
		t.Skip("run by TestNoexecTempDir")
	}
	if err := syscall.Mount("tmpfs", tmp, "tmpfs", syscall.MS_NOEXEC, ""); err != nil {
		os.Stderr.WriteString(err.Error())
		os.Exit(77)
	}
	os.Setenv("TMPDIR", tmp)
	if code := Test(dir, "", false, os.Stdout); code != 0 {
		os.Exit(100 + code)
	}
	os.Exit(Run(dir, nil, os.Stdout))
}
