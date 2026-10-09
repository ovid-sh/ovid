package main

import (
	"bufio"
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

// A jail confines one Claude Code process, and so every command its Bash
// tool runs, with bubblewrap: a new mount namespace (in a user namespace,
// so no root) that holds the system read-only (/usr, /etc, and the like),
// a private /proc, /dev, and /tmp, an empty home directory, and nothing
// else of the host but what the run needs: its work directory
// (read-write), ovid's bin directory, the claude executable, Claude
// Code's configuration directory (read-write), and, for Bedrock, the AWS
// configuration (read-only). Other runs, the output directory, the
// repository, and the rest of the home directory are not in it. It has
// its own process and IPC namespaces; the network is the host's, for the
// model's API.
type jail struct {
	bwrap string   // the bwrap executable
	exe   string   // claude, a real executable rather than a shim
	binds []string // bwrap arguments for what the jail holds besides the system and the run
	path  []string // PATH in the jail after ovid's bin
	warn  []string // what the jail lets the agent read that it would rather not
}

// newJail finds bubblewrap and checks that it can make a namespace here,
// and finds what Claude Code needs in the jail. claude is the -claude
// flag: a name to look up on PATH or a path.
func newJail(claude, home string) (*jail, error) {
	if runtime.GOOS != "linux" {
		return nil, fmt.Errorf("confinement uses bubblewrap, which needs Linux; this is %s", runtime.GOOS)
	}
	bw, err := exec.LookPath("bwrap")
	if err != nil {
		return nil, fmt.Errorf("bwrap (bubblewrap) is not on PATH: install it to confine the agents")
	}
	if b, err := exec.Command(bw, "--ro-bind", "/", "/", "--unshare-pid", "--", "true").CombinedOutput(); err != nil {
		return nil, fmt.Errorf("bwrap cannot make a namespace here (are unprivileged user namespaces off?): %v: %s", err, strings.TrimSpace(string(b)))
	}
	j := &jail{bwrap: bw}
	exe, err := realClaude(claude)
	if err != nil {
		return nil, err
	}
	j.exe = exe
	j.ro(filepath.Dir(exe))
	// A claude that is a Node script needs node, and its package (the
	// script's directory) holds what it loads.
	if sh, _ := shebang(exe); strings.Contains(sh, "node") {
		node, err := exec.LookPath("node")
		if err != nil {
			return nil, fmt.Errorf("%s runs on node, which is not on PATH", exe)
		}
		if node, err = filepath.EvalSymlinks(node); err != nil {
			return nil, err
		}
		j.ro(filepath.Dir(filepath.Dir(node)))
		j.path = append(j.path, filepath.Dir(node))
	}
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		j.binds = append(j.binds, "--bind", d, d)
	} else {
		for _, p := range []string{filepath.Join(home, ".claude"), filepath.Join(home, ".claude.json")} {
			j.binds = append(j.binds, "--bind-try", p, p)
		}
		j.warn = append(j.warn, "CLAUDE_CONFIG_DIR is not set, so the jail holds ~/.claude, with this user's own sessions; set it to a directory of its own")
	}
	if os.Getenv("CLAUDE_CODE_USE_BEDROCK") != "" || os.Getenv("AWS_PROFILE") != "" {
		for _, p := range []string{filepath.Join(home, ".aws"), os.Getenv("AWS_CONFIG_FILE"), os.Getenv("AWS_SHARED_CREDENTIALS_FILE")} {
			if p != "" {
				j.binds = append(j.binds, "--ro-bind-try", p, p)
			}
		}
	}
	return j, nil
}

func (j *jail) ro(dir string) {
	if !systemPath(dir) {
		j.binds = append(j.binds, "--ro-bind", dir, dir)
	}
}

// systemPath reports whether p is in a directory the jail already holds.
func systemPath(p string) bool {
	for _, d := range []string{"/usr", "/etc", "/nix/store"} {
		if within(p, d) {
			return true
		}
	}
	return false
}

// realClaude resolves claude to the executable itself. A version
// manager's shim (mise, asdf: a shell script that finds the real one in
// the home directory) cannot run in the jail; mise is asked where the real
// one is.
func realClaude(claude string) (string, error) {
	p, err := exec.LookPath(claude)
	if err != nil {
		return "", fmt.Errorf("claude: %v", err)
	}
	if p, err = filepath.Abs(p); err != nil {
		return "", err
	}
	if p, err = filepath.EvalSymlinks(p); err != nil {
		return "", err
	}
	sh, err := shebang(p)
	if err != nil || sh == "" || strings.Contains(sh, "node") {
		return p, err
	}
	if b, err := exec.Command("mise", "which", "claude").Output(); err == nil {
		if q, err := filepath.EvalSymlinks(strings.TrimSpace(string(b))); err == nil {
			if sh, _ := shebang(q); sh == "" {
				return q, nil
			}
		}
	}
	return "", fmt.Errorf("claude is %s, a script (%s) that finds the real claude elsewhere, which the jail cannot follow: pass -claude with the executable itself", p, sh)
}

// shebang is the first line of a script, or "" for a binary.
func shebang(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	ln, _ := bufio.NewReader(f).ReadString('\n')
	if !strings.HasPrefix(ln, "#!") {
		return "", nil
	}
	return strings.TrimSpace(ln), nil
}

// args is the bwrap command line that runs argv in the jail for a run in
// work with ovid in bin.
func (j *jail) args(work, bin string, argv []string) []string {
	a := []string{"--die-with-parent", "--new-session", "--unshare-pid", "--unshare-ipc", "--unshare-uts", "--unshare-cgroup-try"}
	a = append(a, "--ro-bind", "/usr", "/usr", "--ro-bind", "/etc", "/etc", "--ro-bind-try", "/nix/store", "/nix/store")
	// /bin, /lib, ... are links into /usr on a merged system, and
	// directories of their own elsewhere.
	for _, d := range []string{"/bin", "/sbin", "/lib", "/lib64", "/lib32"} {
		if t, err := os.Readlink(d); err == nil {
			a = append(a, "--symlink", t, d)
		} else if fi, err := os.Stat(d); err == nil && fi.IsDir() {
			a = append(a, "--ro-bind", d, d)
		}
	}
	// The resolver's file may be a link out of /etc (systemd-resolved's
	// /run/systemd/resolve).
	if r, err := filepath.EvalSymlinks("/etc/resolv.conf"); err == nil && !systemPath(r) {
		a = append(a, "--ro-bind-try", filepath.Dir(r), filepath.Dir(r))
	}
	a = append(a, "--proc", "/proc", "--dev", "/dev", "--tmpfs", "/tmp")
	if home := os.Getenv("HOME"); home != "" {
		a = append(a, "--dir", home)
	}
	a = append(a, j.binds...)
	a = append(a, "--ro-bind", bin, bin, "--bind", work, work, "--chdir", work, "--")
	return append(a, argv...)
}

// command is argv run in the jail.
func (j *jail) command(ctx context.Context, work, bin string, argv []string) *exec.Cmd {
	return exec.CommandContext(ctx, j.bwrap, j.args(work, bin, argv)...)
}

// jailEnv is env for a process in the jail: PATH is path, the shell and
// the temporary directory are ones the jail holds, and the working
// directory is work, not the one this was started in.
func jailEnv(env []string, path, work string) []string {
	set := map[string]string{"PATH": path, "TMPDIR": "/tmp", "PWD": work, "SHELL": "/bin/bash"}
	if _, err := os.Stat("/bin/bash"); err != nil {
		set["SHELL"] = "/bin/sh"
	}
	var out []string
	for _, kv := range env {
		k, _, _ := strings.Cut(kv, "=")
		if _, ok := set[k]; ok || k == "OLDPWD" {
			continue
		}
		out = append(out, kv)
	}
	for _, k := range []string{"PATH", "TMPDIR", "PWD", "SHELL"} {
		out = append(out, k+"="+set[k])
	}
	return out
}

// pathEnv is PATH in the jail: ovid's bin first, then the system's.
func (j *jail) pathEnv(bin string) string {
	return strings.Join(append(append([]string{bin}, j.path...), "/usr/local/bin", "/usr/bin", "/bin", "/usr/local/sbin", "/usr/sbin", "/sbin"), string(os.PathListSeparator))
}
