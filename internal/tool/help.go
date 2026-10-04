package tool

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ovid/internal/check"
	"ovid/internal/syntax"
	"ovid/std"
)

const helpOverview = `ovid: a small compiled language and its toolchain, built for agents.

Source is plain .ov text. A module is a directory with ovid.mod; each
subdirectory holding .ov files is one package, and its path is the package
name. Every command prints JSON lines; the last line always has "ok".
Exit codes: 0 ok, 1 errors, 2 stale edit, 64 usage, 125 run: build failed.

Start here:
  ovid init <dir>              new module with a hello-world entry and a test
  ovid check                   errors with file:line:col, expected/got, hint
  ovid run [-- args]           build to a temp file and run it
  ovid test [--run Name]       run Test* funcs, one process each

Read without opening whole files:
  ovid outline [--pkg P]       packages, or one package's decls with hashes
  ovid show <id|name>... [--plain] [--json]
                               source of a decl or node, lines tagged with ids
  ovid refs <id|name>          every use of a func/type/field/const/param/var
  ovid grep <regexp>           text matches, each with its decl and stmt id

Change code (or edit the .ov files directly; both are fine):
  ovid replace <id> <<'EOF'    one edit, code from stdin (no JSON escaping);
                               also insert --after/--before <id>, append <id>,
                               delete <id>
  ovid edit <file|->           id-addressed batch edit, all or nothing
  ovid rename <id|name> <new>  rename a decl and all its uses
  ovid move <id|name>... <pkg> move funcs/types/consts to another package

Also: ovid build [-o out], ovid dump (program as JSON), ovid version
(commit and binary hash: which ovid is this?), ovid help <topic>.
All commands take -C <dir> (default: the module containing the cwd).

Topics: ovid help language | commands | edit | std | ids
`

const helpLanguage = `Ovid language reference (v0).

File:
  package app/util          // must equal the directory path
  import ovid/io            // one import per line, the full path
  const Limit i64 = 64      // consts are i64
  type Pair struct {        // fields are i64, bool, or *T; one per line
    a i64
    next *Pair
  }
  func Sum(p *Pair, n i64) i64 {
    var t i64 = 0           // every local is declared with a type and value
    while n > 0 {
      t = t + p.a
      p = p.next
      n = n - 1
    }
    return t
  }

Types: i64, bool, *T (T a struct in this package or path.T from an import).
No struct values, slices, arrays, strings, generics, methods, globals, or
closures. At most 6 params; exactly one result type.

Statements: var x T = e | x = e | p.f = e | if c { } else if c { } else { }
| while c { } | return e | store8(addr, v) | store64(addr, v) | call(...).
Every path through a func must return.

Expressions: integers (decimal, 0x hex), true/false, names, calls f(a),
other packages' funcs and consts by import path: ovid/mem.Copy(d, s, n),
ovid/io.O_RDONLY (a one-segment import may also be written util.F()),
field reads p.f, casts e as *T (i64 address to pointer and back),
load8/load32/load64(addr), strptr("lit") / strlen("lit"), sizeof(T).
Binary operators, Go precedence: || && == != < <= > >= + - | ^ * / % << >> &
(>> is arithmetic). Unary: ! (bool), - and ^ (i64). Comparisons give bool;
if/while conditions must be bool.

Strings: there is no string type. strptr("hi\n") is the address of an
interned NUL-terminated literal and strlen("hi\n") is its length (3),
computed by the compiler, so never count bytes by hand:
  ovid/io.Stdout(strptr("total: "), strlen("total: "))
Literals are NUL-terminated, so ovid/io.Print(strptr("total: ")) works too;
ovid/io.PrintInt(io, n) prints a number, Eprint writes to stderr.

Memory: no implicit allocation. ovid/io.Alloc(io, nbytes) returns an i64
address from the heap the runtime maps; cast it: var p *Pair = raw as *Pair.
Each struct field takes 8 bytes, so a struct is 8 * fields bytes; never
count them by hand, write sizeof(T) (T a struct; path.T for another
package's), a compile-time i64:
  var p *Pair = ovid/io.Alloc(io, sizeof(Pair)) as *Pair
There is no address-of (&x): locals live in registers or the stack and
cannot be pointed at. When a callee must write a value back, allocate a
cell and pass its address (the out-param pattern ovid/io.ReadFile uses):
  var pp i64 = ovid/io.Alloc(io, 8)       // receives the data address
  var nn i64 = ovid/io.Alloc(io, 8)       // receives the length
  if ovid/io.ReadFile(io, path, ovid/io.CLen(path), pp, nn) != 0 {
    return 1
  }
  var data i64 = load64(pp)
  var n i64 = load64(nn)
Or return a struct: func Read(...) *Result, with the fields you need.

Control flow, all of it:
  if a < b {
    return 1
  } else if a == b {
    return 0
  } else {
    return -1
  }
  while i < n { i = i + 1 }   // no for, break, or continue: use the condition

Programs: the entry package (ovid.mod "entry") has
  func main(io *ovid/io.Cap) i64    // result is the exit code
io is the capability for argv, heap, and syscalls. syscall(...) is only
allowed inside ovid/io; everyone else calls ovid/io funcs.

Tests: any func TestX(io *ovid/io.Cap) i64 in any module package; 0 passes,
anything else fails (the value is reported as the exit code).
`

const helpCommands = `Commands. Each prints JSON lines; the last line has "ok".

ovid check [--facts]
  One {"fact":"error"} line per problem: code, message, id, file, line, col,
  end_line, end_col, source, and when known expected, got, hint. Summary
  last: {"fact":"summary","ok",errors,packages,funcs,revision,ms}.
ovid build [-o out]          default out: <module>/bin/<module name>
ovid run [--] [args...]      program stdio and exit code pass through;
                             if the build fails: errors as JSON, exit 125
ovid test [--run substr] [--list]
  {"fact":"test",id,ok,exit,ms,output} per test; a failure that returned a
  value adds "returned_by": the return statements that can produce it.
  A crash adds "signal", "at" (the statement that faulted), "stack" (it and
  each call leading to it, innermost first), and for a bad load or store
  "fault_addr"; a hung test reads "signal":"timeout".
  --list prints the tests without running them.
ovid outline [--pkg P] [--all]
  Per decl: id, kind, sig, file, line, end_line, hash, and when present
  doc (the // comment above it), size (struct bytes), test.
ovid show <id|name>... [--plain] [--json]
  Text: "// kind id file:a-b hash=H in=decl type=T" then the source, with
  "  // @id" after each line where a statement starts (--plain omits them).
ovid refs <id|name>          {id,kind,in,file,line,col,source} per use
ovid grep <regexp> [--pkg P] [--std]
  {file,line,col,match,source,decl,stmt} per match (RE2 syntax).
ovid edit <file|-> [--dry-run] [--require-clean] [--show]   see: ovid help edit
ovid replace <id> | insert --after <id> | insert --before <id> | append <id>
  | delete <id>   [--expect H] [--text-file F] [--dry-run] [--require-clean]
  [--show]
  One edit op; the text is read from stdin (or F), so a heredoc works:
    ovid replace st:app.main:3 <<'EOF'
    ovid/io.Stdout(strptr("a \"quoted\" line\n"), strlen("a \"quoted\" line\n"))
    EOF
  Same checks and result as ovid edit.
ovid rename <id|name> <new> [--dry-run]
  Rewrites only the tokens that name it; refuses collisions and changes that
  add check errors.
ovid move <id|name>... <pkg> [--file pkg/x.ov] [--dry-run]
  Moves funcs, types, or consts (with doc comments) to pkg, creating it if
  needed; requalifies every use and adds the imports files now need.
  Refuses changes that add check errors. Several names move in order, all or
  none: on a failure every file is put back.
ovid init <dir> [--name N]   writes ovid.mod, <N>/main.ov, <N>/main_test.ov
ovid dump                    the whole program as JSON (the self-hosted
                             compiler's input format)
ovid version                 {commit, dirty, binary (hash of the executable), path}
ovid help [topic]
`

const helpEdit = `ovid edit: id-addressed edits, applied all or none.

For a single op, ovid replace/insert/append/delete read the text from
stdin and need no JSON; see ovid help commands.

Input (a file, or - for stdin) is {"ops":[...]} or a bare list of ops:
  {"op":"replace","id":ID,"text":SRC}
  {"op":"delete","id":ID}
  {"op":"insert","before":ID,"text":SRC}      or "after":ID
  {"op":"append","into":FUNC_OR_IF_OR_WHILE_ID,"text":STMTS}
  {"op":"append","into":"pkg/path","text":DECLS[,"file":"pkg/path/x.ov"]}
Any op may carry "expect":HASH (from outline/show); if that node's text has
changed the edit is refused with exit 2 and the current hash and text.
A top-level "revision" (from check/outline) guards the whole module instead.

ID may be a full id or a decl name (Sum, util.Sum). Text is plain Ovid; its
indentation is normalised to the target's. insert anchors on statements and
decls; to change part of an expression, replace the expression.

After applying, the module is reparsed (a syntax error rejects everything
and names the op) and checked. Result: {"ok":true,"written","files",
"check_ok","errors","errors_before","revision","ops":[{"ids":[...],
"decls":[{"id","hash"}]}]}: ids are the nodes the op wrote, decls the
top-level decls it touched with their new hashes (--show adds "text"), so a
follow-up edit can "expect" them without reading again.

Examples:
  fix one argument:   {"op":"replace","id":"ex:app.main:2","text":"2"}
  rewrite a statement: {"op":"replace","id":"st:app.main:3",
                        "text":"if n > 0 {\n  return n\n}"}
  replace a whole func by name, guarded:
    {"op":"replace","id":"Sum","expect":"c67681b88f86","text":"func Sum(...) i64 {...}"}
  add a func in a new file: {"op":"append","into":"app/util",
    "file":"app/util/extra.ov","text":"func Half(x i64) i64 {\n  return x / 2\n}"}
--require-clean refuses to write if any check error remains.
--dry-run applies in memory and reports, without writing.
`

const helpIDs = `Ids name every declaration and node. They are derived from source order,
so get fresh ones after edits (edit returns the new ones).

  pkg:P             package           im:P:Q      import of Q in P
  cn:P.Name         const             ty:P.Name   type
  fld:P.Type.f      field             fn:P.Name   func
  pa:P.Func.x       param
  st:P.Func:N       statement N in the func      ex:P.Func:N   expression

Commands that take an id also take a name: Sum, util.Sum (a trailing part of
the package path), app/util.Sum, Pair.next (a field), Sum.n (a param).
A hash is a short digest of a node's source text: edits use it to refuse
writing over text that changed since it was read.
`

// Help prints a help topic as plain text.
func Help(topic string, w io.Writer) int {
	switch topic {
	case "", "overview":
		fmt.Fprint(w, helpOverview)
	case "language", "lang":
		fmt.Fprint(w, helpLanguage)
	case "commands", "cmd":
		fmt.Fprint(w, helpCommands)
	case "edit":
		fmt.Fprint(w, helpEdit)
	case "ids", "id":
		fmt.Fprint(w, helpIDs)
	case "std":
		helpStd(w)
	default:
		fmt.Fprintf(w, "no help topic %q; topics: language commands edit std ids\n", topic)
		return ExitUsage
	}
	return ExitOK
}

// helpStd lists the shipped packages' declarations from their source.
func helpStd(w io.Writer) {
	fmt.Fprintln(w, "Shipped packages. Import them by path; they are used unless the module has its own copy.")
	var pkgs []string
	fs.WalkDir(std.FS, ".", func(p string, d fs.DirEntry, err error) error {
		if err == nil && !d.IsDir() && strings.HasSuffix(p, ".ov") {
			pkgs = append(pkgs, p)
		}
		return nil
	})
	sort.Strings(pkgs)
	for i, p := range pkgs {
		src, _ := fs.ReadFile(std.FS, p)
		pkg, perr := syntax.ParseFile(i, src)
		if perr != nil {
			continue
		}
		fmt.Fprintf(w, "\npackage %s\n", pkg.Path)
		for _, t := range pkg.Types {
			var fl []string
			for _, f := range t.Fields {
				fl = append(fl, f.Name+" "+check.ShowType(pkg.Path, f.Type))
			}
			fmt.Fprintf(w, "  type %s struct { %s }\n", t.Name, strings.Join(fl, "; "))
		}
		var cs []string
		for _, c := range pkg.Consts {
			cs = append(cs, c.Name)
		}
		if len(cs) > 0 {
			fmt.Fprintf(w, "  const %s\n", strings.Join(cs, " "))
		}
		for fi := range pkg.Funcs {
			fn := &pkg.Funcs[fi]
			doc := docComment(src, fn.Span.Off)
			fmt.Fprintf(w, "  %s", check.Signature(pkg.Path, fn))
			if doc != "" {
				fmt.Fprintf(w, "  // %s", doc)
			}
			fmt.Fprintln(w)
		}
	}
}

// docComment returns the // comment lines directly above off, joined.
func docComment(src []byte, off int) string {
	lines := strings.Split(string(src[:lineBegin(src, off)]), "\n")
	var doc []string
	for i := len(lines) - 2; i >= 0; i-- {
		t := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(t, "//") {
			break
		}
		doc = append([]string{strings.TrimSpace(strings.TrimPrefix(t, "//"))}, doc...)
	}
	return strings.Join(doc, " ")
}

// Init creates a module with an entry package, a main, and a test.
func Init(dir, name string, w io.Writer) int {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return fail(w, "init", err.Error(), "")
	}
	if name == "" {
		name = filepath.Base(abs)
	}
	if !isIdent(strings.ReplaceAll(name, "/", "_")) {
		return fail(w, "bad_name", fmt.Sprintf("%q is not a usable package path", name), "use letters, digits, _ and /, e.g. --name hello")
	}
	if exists(filepath.Join(abs, "ovid.mod")) {
		return fail(w, "exists", filepath.Join(abs, "ovid.mod")+" already exists", "")
	}
	files := map[string]string{
		"ovid.mod": "module " + name + "\nentry " + name + "\n",
		filepath.Join(name, "main.ov"): "package " + name + `

import ovid/io

func Greeting() i64 {
  return strptr("hello, world\n")
}

func main(io *ovid/io.Cap) i64 {
  ovid/io.Stdout(Greeting(), strlen("hello, world\n"))
  return 0
}
`,
		filepath.Join(name, "main_test.ov"): "package " + name + `

import ovid/io
import ovid/mem

func TestGreeting(io *ovid/io.Cap) i64 {
  if ovid/mem.EqC(Greeting(), strptr("hello, world\n"), strlen("hello, world\n")) {
    return 0
  }
  return 1
}
`,
	}
	var written []string
	for _, rel := range []string{"ovid.mod", filepath.Join(name, "main.ov"), filepath.Join(name, "main_test.ov")} {
		p := filepath.Join(abs, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return fail(w, "init", err.Error(), "")
		}
		if err := os.WriteFile(p, []byte(files[rel]), 0o644); err != nil {
			return fail(w, "init", err.Error(), "")
		}
		written = append(written, p)
	}
	emit(w, map[string]any{"ok": true, "root": abs, "files": written,
		"next": "cd " + dir + " && ovid run && ovid test; ovid help language"})
	return ExitOK
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}
