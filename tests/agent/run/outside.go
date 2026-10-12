package main

import (
	"encoding/json"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strings"
)

// scope is what one agent's tool inputs may name without looking outside
// its directory, and where its shell is.
type scope struct {
	repo  string   // the repository: a tool input that names it at all looked outside
	work  string   // the work directory
	home  string   // what ~ and $HOME stand for
	allow []string // other directories it may name: ovid's bin
	// scratch are temporary directories (/tmp) the files in which it may
	// name, except those in deny (the output directory) and other runs'
	// (ovid-agent-*); not the directory itself, which lists the others.
	scratch, deny []string
	// gitUp says a git command run in work reaches a repository above it,
	// so any git command looks outside.
	gitUp bool
	cwd   string // the Bash tool's directory, which persists between calls
	prev  string // the one before the last cd, for cd -
}

// inspects are the commands that read or list what their path arguments
// name, and cd, which moves the shell there.
var inspects = map[string]bool{
	"find": true, "ls": true, "cat": true, "git": true, "head": true, "tail": true,
	"less": true, "more": true, "grep": true, "egrep": true, "fgrep": true, "rg": true,
	"tree": true, "stat": true, "file": true, "du": true, "wc": true, "diff": true,
	"cmp": true, "readlink": true, "realpath": true, "strings": true, "od": true,
	"xxd": true, "hexdump": true, "sort": true,
	"cd": true, "pushd": true, "ovid": true,
}

// outside reports whether one tool call looked outside the agent's
// directory: its input names the repository or the exercise, a Read,
// Write, or Edit names a file outside, or a Bash command lists, reads, or
// cds to a path outside (find, ls, cat, git, ...), or runs an ovid other
// than the one it was given.
func (sc *scope) outside(tool string, input json.RawMessage) bool {
	s := string(input)
	if sc.repo != "" && namesDir(s, sc.repo) || strings.Contains(s, "tests/agent") || strings.Contains(s, "ovid-sh") {
		return true
	}
	var in struct {
		Command  string
		FilePath string `json:"file_path"`
		Path     string
	}
	json.Unmarshal(input, &in)
	if sc.cwd == "" {
		sc.cwd = sc.work
	}
	switch tool {
	case "Bash":
		return sc.bashOutside(in.Command)
	default:
		for _, p := range []string{in.FilePath, in.Path} {
			if p != "" && !sc.allowed(sc.resolve(p, sc.work)) {
				return true
			}
		}
	}
	return false
}

// bashOutside reads a shell command the way a shell would split it, well
// enough to find the paths each simple command is given.
func (sc *scope) bashOutside(command string) bool {
	out := false
	// Claude Code keeps the shell's directory from call to call, unless
	// it is outside the work directory: then the next call starts in work.
	if !within(sc.cwd, sc.work) {
		sc.cwd = sc.work
	}
	for _, words := range shellCommands(command) {
		// Input redirections read, whatever the command.
		var args []string
		for i := 0; i < len(words); i++ {
			switch words[i] {
			case "<":
				if i+1 < len(words) && !sc.allowed(sc.resolve(words[i+1], sc.cwd)) {
					out = true
				}
				i++
			case ">", ">>":
				i++
			default:
				args = append(args, words[i])
			}
		}
		// Assignments and the commands that run another.
		for len(args) > 0 && (assignment(args[0]) || wrappers[args[0]]) {
			args = args[1:]
		}
		if len(args) == 0 {
			continue
		}
		name := path.Base(args[0])
		if name == "ovid" && strings.Contains(args[0], "/") && !sc.allowed(sc.resolve(args[0], sc.cwd)) {
			out = true // another ovid than the one it was given
		}
		if !inspects[name] {
			continue
		}
		if name == "git" && sc.gitUp {
			out = true
		}
		var operands []string
		for i, a := range args[1:] {
			if name == "ovid" && (a == "--" || a == "run") {
				// What follows is the program's: a missing file it is
				// tested on is not one the agent looked at.
				if a == "run" {
					operands = append(operands, collectC(args[i+2:])...)
				}
				break
			}
			if !strings.HasPrefix(a, "-") {
				operands = append(operands, a)
			}
		}
		switch name {
		case "cd", "pushd":
			if slices.Contains(args, "-") {
				sc.cwd, sc.prev = sc.prev, sc.cwd // checked when it was there
				if sc.cwd == "" {
					sc.cwd = sc.work
				}
				continue
			}
			p := sc.home
			if len(operands) > 0 {
				p = sc.resolve(operands[0], sc.cwd)
			}
			// Into a scratch directory is not looking; listing it is.
			if !sc.allowed(p) && !sc.scratchRoot(p) {
				out = true
			}
			sc.cwd, sc.prev = p, sc.cwd
			continue
		case "ls", "find", "tree", "du":
			// No operand lists the shell's directory.
			if len(operands) == 0 && !sc.allowed(sc.cwd) {
				out = true
			}
		}
		for _, a := range operands {
			if pathLike(a) && !sc.allowed(sc.resolve(a, sc.cwd)) {
				out = true
			}
		}
	}
	return out
}

// collectC is the values of the -C flags among args, up to a "--".
func collectC(args []string) []string {
	var dirs []string
	for i := 0; i < len(args) && args[i] != "--"; i++ {
		if args[i] == "-C" && i+1 < len(args) {
			dirs = append(dirs, args[i+1])
			i++
		}
	}
	return dirs
}

// scratchRoot reports whether p is one of the scratch directories.
func (sc *scope) scratchRoot(p string) bool {
	for _, d := range sc.scratch {
		if p == strings.TrimSuffix(d, "/") {
			return true
		}
	}
	return false
}

var wrappers = map[string]bool{"sudo": true, "time": true, "env": true, "command": true, "exec": true, "nice": true, "nohup": true, "xargs": true, "builtin": true}

// assignment reports whether w is NAME=value.
func assignment(w string) bool {
	i := strings.IndexByte(w, '=')
	if i <= 0 {
		return false
	}
	for _, c := range w[:i] {
		if !(c == '_' || 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9') {
			return false
		}
	}
	return true
}

// pathLike reports whether a word names a place by itself: an absolute
// path, one in the home directory, or one that climbs out with "..".
func pathLike(w string) bool {
	return strings.HasPrefix(w, "/") || w == "~" || strings.HasPrefix(w, "~/") ||
		strings.HasPrefix(w, "$HOME") || strings.HasPrefix(w, "${HOME}") ||
		w == ".." || strings.HasPrefix(w, "../") || strings.Contains(w, "/../") || strings.HasSuffix(w, "/..")
}

// resolve makes a word an absolute, clean path, relative to dir.
func (sc *scope) resolve(w, dir string) string {
	switch {
	case w == "~" || w == "$HOME" || w == "${HOME}":
		w = sc.home
	case strings.HasPrefix(w, "~/"):
		w = sc.home + w[1:]
	case strings.HasPrefix(w, "$HOME/"):
		w = sc.home + w[5:]
	case strings.HasPrefix(w, "${HOME}/"):
		w = sc.home + w[7:]
	}
	if !strings.HasPrefix(w, "/") {
		w = dir + "/" + w
	}
	return path.Clean(w)
}

// allowed reports whether the clean absolute path p is in the work
// directory, in an allowed one, or a device (/dev/null).
func (sc *scope) allowed(p string) bool {
	for _, d := range append([]string{sc.work, "/dev"}, sc.allow...) {
		if within(p, d) {
			return true
		}
	}
	// Scratch files in a temporary directory are the agent's own (it
	// writes test inputs there), but not the directory's listing, the
	// output directory, or another run's.
	for _, d := range sc.deny {
		if within(p, d) {
			return false
		}
	}
	for _, d := range sc.scratch {
		if d = strings.TrimSuffix(d, "/"); d != "" && strings.HasPrefix(p, d+"/") && !strings.HasPrefix(p, d+"/ovid-agent-") {
			return true
		}
	}
	return false
}

// within reports whether the clean path p is d or under it.
func within(p, d string) bool {
	d = strings.TrimSuffix(d, "/")
	return d != "" && (p == d || strings.HasPrefix(p, d+"/"))
}

// claudeConfigDir is Claude Code's configuration directory:
// $CLAUDE_CONFIG_DIR, or ~/.claude.
func claudeConfigDir(home string) string {
	if d := os.Getenv("CLAUDE_CONFIG_DIR"); d != "" {
		return filepath.Clean(d)
	}
	return filepath.Join(home, ".claude")
}

// scratchDirs are the temporary directories: /tmp and $TMPDIR.
func scratchDirs() []string {
	d := []string{"/tmp"}
	if t := filepath.Clean(os.TempDir()); t != "/tmp" {
		d = append(d, t)
	}
	return d
}

// gitAbove reports whether a git command run in work would find a
// repository above it (work inside a checkout), and not one of its own.
func gitAbove(work string) bool {
	for d := work; ; {
		if _, err := os.Stat(filepath.Join(d, ".git")); err == nil {
			return d != work
		}
		up := filepath.Dir(d)
		if up == d {
			return false
		}
		d = up
	}
}

// shellCommands splits a shell command into its simple commands, each a
// list of words with quotes removed. The operators that end a command
// (; & | ( ) newline ` $() end one; < > >> stand as words of their own; a
// here-document's body is skipped. It is not a shell: it does not expand
// anything but what resolve does, and it is good enough to find paths.
func shellCommands(s string) [][]string {
	var cmds [][]string
	var words []string
	var w strings.Builder
	inWord := false
	endWord := func() {
		if inWord {
			words = append(words, w.String())
			w.Reset()
			inWord = false
		}
	}
	endCmd := func() {
		endWord()
		if len(words) > 0 {
			cmds = append(cmds, words)
			words = nil
		}
	}
	var heredocs []string // delimiters whose bodies start at the next newline
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\'':
			inWord = true
			j := strings.IndexByte(s[i+1:], '\'')
			if j < 0 {
				j = len(s) - i - 1
			}
			w.WriteString(s[i+1 : i+1+j])
			i += j + 1
		case c == '"':
			inWord = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) {
					i++
				}
				w.WriteByte(s[i])
			}
		case c == '\\' && i+1 < len(s):
			inWord = true
			i++
			if s[i] != '\n' {
				w.WriteByte(s[i])
			}
		case c == '$' && i+1 < len(s) && s[i+1] == '(':
			endCmd()
			i++
		case c == '\n':
			endCmd()
			for _, d := range heredocs {
				// The body runs to a line that is the delimiter alone
				// (tabs before it allowed, for <<-).
				for i+1 < len(s) {
					e := strings.IndexByte(s[i+1:], '\n')
					if e < 0 {
						e = len(s) - i - 1
					}
					ln := s[i+1 : i+1+e]
					i += e + 1
					if strings.TrimLeft(ln, "\t") == d {
						break
					}
				}
			}
			heredocs = nil
		case strings.IndexByte(";&|()`", c) >= 0:
			endCmd()
		case c == '<' && i+1 < len(s) && s[i+1] == '<':
			endWord()
			i += 2
			if i < len(s) && s[i] == '<' { // <<< here-string: the word is data
				words = append(words, ">")
				continue
			}
			if i < len(s) && s[i] == '-' {
				i++
			}
			for i < len(s) && (s[i] == ' ' || s[i] == '\t') {
				i++
			}
			j := i
			for j < len(s) && strings.IndexByte(" \t\n;&|()<>", s[j]) < 0 {
				j++
			}
			heredocs = append(heredocs, strings.NewReplacer(`'`, "", `"`, "", `\`, "").Replace(s[i:j]))
			i = j - 1
		case c == '<' || c == '>':
			endWord()
			if c == '>' && i+1 < len(s) && s[i+1] == '>' {
				i++
			}
			if c == '>' && i+1 < len(s) && s[i+1] == '&' { // >&2: no file
				i++
				continue
			}
			words = append(words, string(c))
		case c == ' ' || c == '\t':
			endWord()
		default:
			inWord = true
			w.WriteByte(c)
		}
	}
	endCmd()
	return cmds
}
