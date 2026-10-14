package main

import (
	"errors"
	"os"
	"os/exec"
	"strings"
	"testing"
)

// TestMainArgs runs main with the arguments in OVID_TEST_ARGS, for the
// tests below to run as a separate process; it does nothing on its own.
func TestMainArgs(t *testing.T) {
	args := os.Getenv("OVID_TEST_ARGS")
	if args == "" {
		t.Skip("run by the other tests")
	}
	os.Args = append([]string{"ovid"}, strings.Split(args, "\x1f")...)
	main()
}

// ovid runs main with args in a child process and returns its output and
// exit code.
func ovid(t *testing.T, args ...string) (string, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestMainArgs$")
	cmd.Env = append(os.Environ(), "OVID_TEST_ARGS="+strings.Join(args, "\x1f"))
	out, err := cmd.CombinedOutput()
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return string(out), ee.ExitCode()
	} else if err != nil {
		t.Fatal(err)
	}
	return string(out), 0
}

// TestEmptyName: --name given empty is a usage error, not the flag left
// out, for refs and rename.
func TestEmptyName(t *testing.T) {
	for _, args := range [][]string{
		{"refs", "Two", "--name", ""},
		{"refs", "Two", "--name="},
		{"rename", "Two", "Three", "--name", ""},
	} {
		out, code := ovid(t, args...)
		if code != 64 || !strings.Contains(out, "--name needs a name") {
			t.Errorf("%q: exit %d: %s", args, code, out)
		}
	}
}
