// Command ovid is the Ovid toolchain. Run `ovid help` for the overview.
package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"ovid/internal/module"
	"ovid/internal/tool"
)

// args is a parsed command line: positionals, flags with values, and bools.
type args struct {
	pos   []string
	vals  map[string]string
	bools map[string]bool
	rest  []string // after --
}

func usageErr(cmd, msg string) {
	r := map[string]any{"ok": false, "error": "usage", "message": msg}
	if cmd != "" {
		r["hint"] = "ovid help commands"
	} else {
		r["hint"] = "ovid help"
	}
	b, _ := json.Marshal(r)
	fmt.Println(string(b))
	os.Exit(tool.ExitUsage)
}

// parse splits argv given the flags that take values and the bool flags.
func parse(cmd string, argv []string, valued, boolean []string) args {
	a := args{vals: map[string]string{}, bools: map[string]bool{}}
	has := func(list []string, s string) bool {
		for _, x := range list {
			if x == s {
				return true
			}
		}
		return false
	}
	for i := 0; i < len(argv); i++ {
		s := argv[i]
		if s == "--" {
			a.rest = argv[i+1:]
			break
		}
		if !strings.HasPrefix(s, "-") || s == "-" {
			a.pos = append(a.pos, s)
			continue
		}
		name, val, hasVal := strings.Cut(strings.TrimLeft(s, "-"), "=")
		switch {
		case has(valued, name):
			if !hasVal {
				if i+1 >= len(argv) {
					usageErr(cmd, "flag -"+name+" needs a value")
				}
				i++
				val = argv[i]
			}
			a.vals[name] = val
		case has(boolean, name):
			a.bools[name] = true
		default:
			usageErr(cmd, fmt.Sprintf("%s: unknown flag %s", cmd, s))
		}
	}
	return a
}

// pageArg reads --offset and --limit: the default page is the first
// tool.PageLimit records, and --limit 0 asks for all of them.
func pageArg(cmd string, a args) tool.Page {
	pg := tool.Page{Limit: tool.PageLimit}
	for k, p := range map[string]*int{"offset": &pg.Offset, "limit": &pg.Limit} {
		if v, ok := a.vals[k]; ok {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				usageErr(cmd, "--"+k+" takes a count, got "+strconv.Quote(v))
			}
			*p = n
		}
	}
	return pg
}

func main() {
	if tool.ConfineMain() {
		return
	}
	argv := os.Args[1:]
	dir := ""
	// A leading -C dir applies to every command.
	for len(argv) >= 2 && argv[0] == "-C" {
		dir, argv = argv[1], argv[2:]
	}
	if len(argv) == 0 {
		tool.Help("", os.Stdout)
		os.Exit(tool.ExitUsage)
	}
	cmd, argv := argv[0], argv[1:]
	if v := os.Getenv(module.PathsEnv); v != "" && v != "cwd" && v != "module" {
		usageErr("", module.PathsEnv+" is \"cwd\" (the default) or \"module\", got "+strconv.Quote(v))
	}
	w := os.Stdout
	// dirArg picks the module dir: -C, else an optional positional.
	dirArg := func(a args, maxPos int) string {
		if v, ok := a.vals["C"]; ok {
			dir = v
		}
		if len(a.pos) > maxPos {
			if len(a.pos) > maxPos+1 || dir != "" {
				usageErr(cmd, fmt.Sprintf("%s: unexpected argument %q", cmd, a.pos[maxPos]))
			}
			dir = a.pos[maxPos]
		}
		return dir
	}
	switch cmd {
	case "help", "-h", "--help":
		topic := ""
		if len(argv) > 0 {
			topic = argv[0]
		}
		os.Exit(tool.Help(topic, w))
	case "version":
		os.Exit(version(w))
	case "init":
		a := parse(cmd, argv, []string{"name", "C"}, nil)
		if len(a.pos) != 1 {
			usageErr(cmd, "usage: ovid init <dir> [--name N]")
		}
		os.Exit(tool.Init(a.pos[0], a.vals["name"], w))
	case "check":
		a := parse(cmd, argv, []string{"C"}, []string{"facts"})
		os.Exit(tool.Check(dirArg(a, 0), a.bools["facts"], w))
	case "build":
		a := parse(cmd, argv, []string{"C", "o"}, nil)
		os.Exit(tool.Build(dirArg(a, 0), a.vals["o"], w))
	case "run":
		a := parse(cmd, argv, []string{"C", "timeout", "max-output"}, []string{"json", "confine"})
		o := tool.RunOpts{JSON: a.bools["json"], Confine: a.bools["confine"]}
		if v, ok := a.vals["timeout"]; ok {
			d, err := time.ParseDuration(v)
			if err != nil || d <= 0 {
				usageErr(cmd, "--timeout takes a duration such as 5s or 500ms, got "+strconv.Quote(v))
			}
			o.Timeout = d
		}
		if v, ok := a.vals["max-output"]; ok {
			n, err := strconv.Atoi(v)
			if err != nil || n <= 0 || !o.JSON {
				usageErr(cmd, "--max-output takes a byte count and goes with --json, got "+strconv.Quote(v))
			}
			o.MaxOutput = n
		}
		os.Exit(tool.RunWith(dirArg(a, 0), a.rest, o, w))
	case "test":
		a := parse(cmd, argv, []string{"C", "run"}, []string{"list", "confine"})
		os.Exit(tool.TestWith(dirArg(a, 0), tool.TestOpts{Filter: a.vals["run"], List: a.bools["list"], Confine: a.bools["confine"]}, w))
	case "dump":
		a := parse(cmd, argv, []string{"C", "pkg", "o"}, nil)
		os.Exit(tool.Dump(dirArg(a, 0), a.vals["pkg"], a.vals["o"], w))
	case "outline":
		a := parse(cmd, argv, []string{"C", "pkg", "offset", "limit"}, []string{"all", "uses"})
		os.Exit(tool.Outline(dirArg(a, 0), a.vals["pkg"], a.bools["all"], a.bools["uses"], pageArg(cmd, a), w))
	case "show":
		a := parse(cmd, argv, []string{"C"}, []string{"ids", "plain", "json", "exprs"})
		if len(a.pos) == 0 {
			usageErr(cmd, "usage: ovid show <id|name>... [--plain] [--json] [--exprs]")
		}
		os.Exit(tool.Show(dirArg(a, 1<<30), a.pos, !a.bools["plain"], a.bools["json"], a.bools["exprs"], w))
	case "refs":
		a := parse(cmd, argv, []string{"C", "offset", "limit"}, nil)
		if len(a.pos) != 1 {
			usageErr(cmd, "usage: ovid refs <id|name> [--offset N] [--limit N]")
		}
		os.Exit(tool.Refs(dirArg(a, 1), a.pos[0], pageArg(cmd, a), w))
	case "grep":
		a := parse(cmd, argv, []string{"C", "pkg", "offset", "limit"}, []string{"std"})
		if len(a.pos) != 1 {
			usageErr(cmd, "usage: ovid grep <regexp> [--pkg P] [--std] [--offset N] [--limit N]")
		}
		pg := pageArg(cmd, a)
		os.Exit(tool.Grep(dirArg(a, 1), a.pos[0], a.vals["pkg"], a.bools["std"], pg.Offset, pg.Limit, w))
	case "edit":
		a := parse(cmd, argv, []string{"C", "rev"}, []string{"dry-run", "require-clean", "allow-broken", "show", "force"})
		if len(a.pos) != 1 {
			usageErr(cmd, "usage: ovid edit <file|-> [--rev REV] [--dry-run] [--require-clean|--allow-broken] [--show] [--force]; see ovid help edit")
		}
		os.Exit(tool.Edit(dirArg(a, 1), a.pos[0], tool.EditOpts{DryRun: a.bools["dry-run"], RequireClean: a.bools["require-clean"], AllowBroken: a.bools["allow-broken"], Show: a.bools["show"], Force: a.bools["force"], Revision: a.vals["rev"]}, w))
	case "rename":
		a := parse(cmd, argv, []string{"C"}, []string{"dry-run"})
		if len(a.pos) != 2 {
			usageErr(cmd, "usage: ovid rename <id|name> <new> [--dry-run]")
		}
		os.Exit(tool.Rename(dirArg(a, 2), a.pos[0], a.pos[1], a.bools["dry-run"], w))
	case "move":
		a := parse(cmd, argv, []string{"C", "file"}, []string{"dry-run"})
		if len(a.pos) < 2 {
			usageErr(cmd, "usage: ovid move <id|name>... <pkg> [--file pkg/x.ov] [--dry-run]")
		}
		n := len(a.pos)
		os.Exit(tool.MoveMany(dirArg(a, 1<<30), a.pos[:n-1], a.pos[n-1], a.vals["file"], a.bools["dry-run"], w))
	case "replace", "insert", "append", "delete":
		// One edit op with its text on stdin (or --text-file), so a shell
		// heredoc carries code without JSON escaping.
		a := parse(cmd, argv, []string{"C", "expect", "rev", "before", "after", "file", "text-file"}, []string{"dry-run", "require-clean", "allow-broken", "show", "force"})
		op := tool.EditOp{Op: cmd, Expect: a.vals["expect"], File: a.vals["file"]}
		from := a.vals["text-file"]
		if from == "" && cmd != "delete" {
			from = "-"
		}
		// Every flag goes into the op, so one that does not belong to this
		// command is refused by the planner rather than dropped here.
		op.Before, op.After = a.vals["before"], a.vals["after"]
		for _, k := range []string{"before", "after", "file"} {
			if _, ok := a.vals[k]; ok {
				op.Given = append(op.Given, k)
			}
		}
		if _, ok := a.vals["text-file"]; ok {
			op.Given = append(op.Given, "text")
		}
		switch cmd {
		case "insert":
			if len(a.pos) != 0 || (op.Before == "") == (op.After == "") {
				usageErr(cmd, "usage: ovid insert --after <id> | --before <id> --expect H|--rev REV|--force <<'EOF' ... EOF")
			}
		case "append":
			if len(a.pos) != 1 {
				usageErr(cmd, "usage: ovid append <fn-or-pkg id> [--file pkg/x.ov] [--expect H|--rev REV|--force] <<'EOF' ... EOF")
			}
			op.Into = a.pos[0]
		default:
			if len(a.pos) != 1 {
				usageErr(cmd, "usage: ovid "+cmd+" <id> --expect H|--rev REV|--force"+map[bool]string{true: "", false: " <<'EOF' ... EOF"}[cmd == "delete"])
			}
			op.ID = a.pos[0]
		}
		os.Exit(tool.EditOne(dirArg(a, 1<<30), op, from, tool.EditOpts{DryRun: a.bools["dry-run"], RequireClean: a.bools["require-clean"], AllowBroken: a.bools["allow-broken"], Show: a.bools["show"], Force: a.bools["force"], Revision: a.vals["rev"]}, w))
	default:
		usageErr("", "unknown command "+cmd+"; commands: init check build run test outline show refs grep edit replace insert append delete rename move dump version help")
	}
}

// version identifies this binary: the source commit it was built from when
// known, and a hash of the executable itself, so two copies can be told
// apart even when neither came from a clean checkout.
func version(w io.Writer) int {
	r := map[string]any{"ok": true}
	if bi, ok := debug.ReadBuildInfo(); ok {
		r["go"] = bi.GoVersion
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				r["commit"] = s.Value
			case "vcs.modified":
				r["dirty"] = s.Value == "true"
			case "vcs.time":
				r["commit_time"] = s.Value
			}
		}
	}
	if exe, err := os.Executable(); err == nil {
		r["path"] = exe
		if b, err := os.ReadFile(exe); err == nil {
			sum := sha256.Sum256(b)
			r["binary"] = hex.EncodeToString(sum[:6])
		}
	}
	b, _ := json.Marshal(r)
	fmt.Fprintln(w, string(b))
	return tool.ExitOK
}
