package tool

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"ovid/internal/check"
	"ovid/internal/module"
	"ovid/internal/syntax"
	"ovid/std"
)

const helpOverview = `ovid: a small compiled language and its toolchain, built for agents.

Source is plain .ov text. A module is a directory with ovid.mod; each
subdirectory holding .ov files is one package, and its path is the package
name. Every command prints JSON lines; the last line always has "ok".
Exit codes: 0 ok, 1 errors, 2 stale edit, 64 usage, 124 run: timeout,
125 run: could not build or start the program.
Paths in records are relative to the working directory; with
OVID_PATHS=module in the environment, to the module root.

Start here:
  ovid init <dir>              new module with a hello-world entry and a test
  ovid check                   errors with file:line:col, expected/got, hint
  ovid run [-- args]           build to a temp file and run it
  ovid run --json [--timeout 5s]
                               the same, with exit, signal, and output as JSON
  ovid test [--run Name]       run Test* funcs, one process each

Read without opening whole files:
  ovid outline [--pkg P]       packages, or one package's decls with hashes
                               (outline, refs, grep print 200 records a page)
  ovid show <id|name>... [--plain] [--json] [--exprs]
                               source of a decl or node, lines tagged with ids
  ovid refs <id|name>          every use of a func/type/field/const/param/var
  ovid grep <regexp>           text matches, each with its decl and stmt id

Change code (or edit the .ov files directly; both are fine):
  ovid replace <id> --expect H <<'EOF'
                               one edit, code from stdin (no JSON escaping);
                               also insert --after/--before <id>, append <id>,
                               delete <id>
  ovid edit <file|->           id-addressed batch edit, all or nothing
  ovid rename <id|name> <new>  rename a decl and all its uses
  ovid move <id|name>... <pkg> move funcs/types/consts to another package

Also: ovid build [-o out], ovid dump (program as JSON), ovid version
(commit and binary hash: which ovid is this?), ovid help <topic>.
All commands take -C <dir> (default: the module containing the cwd).

The language at a glance (all of it: ovid help language):
  i64, bool, *T; var x i64 = 0; if/else if/else; while; no for/break/continue
  var p *T = ovid/io.Alloc(io, sizeof(T)) as *T     structs live on the heap
  ovid/io.Print(strptr("hi\n")); ovid/io.PrintInt(io, n)   output
  var sp bool = c == 32 || c == 9                   && || ! work anywhere

Topics: ovid help language | commands | edit | std | ids
`

const helpLanguage = `Ovid language reference (v0).

File:
  package app/util          // must equal the directory path
  import ovid/io            // one import per line, the full path
  const Limit i64 = 64      // consts are i64
  const Pow [3]i64 = {1, 10, 100}   // a table: read-only, Pow[i] and len(Pow)
  type Pair struct {        // fields are i64, bool, or *T; one per line
    a i64
    next *Pair
  }
  func Sum(p *Pair, n i64) i64 {
    var t i64 = 0           // every local is declared with a type; var t i64 is 0
    while n > 0 {
      t = t + p.a
      p = p.next
      n = n - 1
    }
    return t
  }

Imports may not form a cycle, directly or through other packages
(import_cycle): the packages are a DAG.

Types: i64, bool, *T (T a struct in this package or path.T from an import).
No struct values, slices, arrays, strings, generics, methods, globals, or
closures. At most 6 params; exactly one result type.

Tables: const Name [N]i64 = {e, ...} is a read-only table of N constant
expressions in the binary's data, which may run over several lines. It is
not a value: read Name[i] (i64; i outside 0..N-1 kills the program with
an illegal instruction, which ovid test reports at the statement) and
len(Name) (N, a compile-time constant), also as path.Name[i] from another
package. len is a keyword only before a ( (spaces or tabs may sit between);
elsewhere a variable may be named len.

Statements: var x T = e | var x T (zero: 0, false, or a null pointer) | x = e | p.f = e | if c { } else if c { } else { }
| while c { } | return e | store8/16/32/64(addr, v) (the low bits of v) | call(...).
Every path through a func must return.

Expressions: integers (decimal, 0x hex), true/false, names, calls f(a),
other packages' funcs and consts by import path: ovid/mem.Copy(d, s, n),
ovid/io.O_RDONLY (a one-segment import may also be written util.F()),
field reads p.f, casts e as *T (i64 address to pointer and back),
load8/16/32/64(addr) (zero-extended), bswap16/32/64(x) (reverses the low
bytes, zero-extended: a big-endian field is store32(p, bswap32(v)) and
bswap32(load32(p))), strptr("lit") / strlen("lit"), sizeof(T).
Binary operators, Go precedence: || && == != < <= > >= + - | ^ * / % << >> &
(>> is arithmetic). Unary: ! (bool), - and ^ (i64). Comparisons give bool;
if/while conditions must be bool. && and || short-circuit and are ordinary
values: var sp bool = c == 32 || c == 9 || c == 10.
The operators are signed. The unsigned ones are spelled as calls:
ushr(x, n) (logical shift, count masked to 0..63 like >>), umulhi(a, b)
(the high 64 bits of the 128-bit product), udiv(a, b), urem(a, b) (both
trap on 0 like / and %), ult(a, b) bool.

Strings: there is no string type. strptr("hi\n") is the address of an
interned NUL-terminated literal and strlen("hi\n") is its length (3),
computed by the compiler, so never count bytes by hand. Literals are
read-only: a store into one kills the program (SIGSEGV), so to change the
bytes, copy them first: var b i64 = ovid/io.Alloc(io, n) then
ovid/mem.Copy(b, strptr("..."), n). Literals are NUL-terminated, so
printing one needs only its address:
  ovid/io.Print(strptr("total: "))     // Eprint writes to stderr
  ovid/io.PrintInt(io, n)              // a number in decimal
  ovid/io.Stdout(p, n)                 // n bytes at p, for non-literals

Memory: no implicit allocation. ovid/io.Alloc(io, nbytes) returns an i64
address of zeroed bytes from the heap, which grows as needed and is never
freed; it does not return 0 (out of memory ends the program, exit 71).
Cast the address: var p *Pair = raw as *Pair.
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
allowed inside ovid/io; everyone else calls ovid/io funcs. ovid/io's types
are handles: outside ovid/io a pointer to one cannot be made by a cast, cast
to anything, or have its fields read or written (opaque_type).

Serving HTTP: write a handler in the entry package and no main; build
makes the program the stdio host around it, which reads one request from
stdin and writes the response to stdout (ovid help std, ovid/http), so a
platform that spawns the binary per request can run it:
  func handle(io *ovid/io.Cap, req *ovid/http.Request, res *ovid/http.Response) i64 {
    ovid/http.Write(io, res, strptr("hi"), strlen("hi"))
    return 0                        // anything else answers 500
  }
A main, if there is one, is the entry instead; the host it replaces is
  func main(io *ovid/io.Cap) i64 {
    var req *ovid/http.Request = ovid/http.ReadStdio(io)
    var res *ovid/http.Response = ovid/http.NewResponse(io)
    if ovid/http.Err(req) != 0 {
      return ovid/http.WriteStdio(io, req, res, 0)
    }
    return ovid/http.WriteStdio(io, req, res, handle(io, req, res))
  }
A test calls handle with ovid/http.NewRequest and reads ovid/http.Sent.

Tests: any func TestX(io *ovid/io.Cap) i64 in any module package; 0 passes,
anything else fails (the value is reported as the exit code). build and run
leave out _test.ov files: an error there stops check and test, not them, and
the program cannot call what they declare.
`

const helpCommands = `Commands. Each prints JSON lines; the last line has "ok".

ovid check [--facts]
  One {"fact":"error"} line per problem: code, message, id, file, line, col,
  end_line, end_col, source, and when known expected, got, hint. Summary
  last: {"fact":"summary","ok",errors,packages,funcs,revision,ms}.
ovid build [-o out]          default out: <module>/bin/<module name>;
                             _test.ov files are left out (so for run)
  {"ok",output,bytes,syscalls}: syscalls lists the system call numbers the
  program can make (reachable from main, plus startup's mmap, exit, write),
  enough to run it under a filter that allows nothing else.
ovid run [--] [args...]      program stdio and exit code pass through;
                             if the build fails: errors as JSON, exit 125.
  A program run by run or test gets its arguments and stdio and an empty
  environment: none of ovid's own variables reach it.
  run and test execute the program from TMPDIR (else /tmp), or from memory
  where that is missing or noexec and /proc is mounted; if neither works:
  {"ok":false,"error":"run",message,hint}, exit 125 (run) or 1 (test).
  Killed by a signal: exit 128+N and one line on stderr,
  {"ok":false,"error":"killed",signal,exit,at,stack,fault_addr,hint}, with
  at/stack (the statement and its callers) for a fault on Linux.
  --timeout D (5s, 500ms) ends the program: "signal":"timeout", exit 124.
  On Linux x86-64 the program is confined unless --no-confine: a seccomp
  filter of the system calls its build receipt lists (any other kills it:
  "signal":"bad system call") and, where the kernel has Landlock, writes
  only in a fresh directory, its working directory; everything else is
  read-only, its module included. The record (run --json, test's summary)
  adds "confined":["seccomp","landlock"] and, if the program wrote there,
  "writable":DIR, which is kept. Reads are not restricted.
ovid run --json [--timeout D] [--max-output N] [--no-confine] [--] [args...]
  captures the output; one last line, and ovid exits 0 if the program ran:
  {"ok":true,"exit":N,"ms",stdout,stderr}; a signal or timeout gives
  "signal" (with at/stack) in place of "exit". Each stream keeps N bytes
  (65536); past that "truncated":true and stdout_bytes/stderr_bytes.
  A build that fails ends {"ok":false,"errors":N}, exit 125.
ovid test [--run substr] [--list] [--no-confine]
  {"fact":"test",id,ok,exit,ms,output} per test; a failure that returned a
  value adds "returned_by": the return statements that can produce it;
  if !ovid/test.Eq(io, got, want) { return 1 } also puts "got X, want Y"
  in its output.
  Output past 4000 bytes is cut ("...(truncated)") and "output_bytes" gives
  its full size.
  If a test changed a file of the module, the summary is not ok and adds
  "module_changed":true, changed_files, revision_before, and, when the
  module still loads, revision_after; run --json adds the same fields.
  scan_error is there when the module's directory could not be read
  through: changed_files is then incomplete.
  A crash adds "signal", "at" (the statement that faulted), "stack" (it and
  each call leading to it, innermost first), and for a bad load or store
  "fault_addr"; a hung test reads "signal":"timeout".
  A test the kernel refused memory reads "error":"out_of_memory", exit 71.
  --list prints the tests without running them.
ovid outline [--pkg P] [--all] [--uses] [--offset N] [--limit N]
  Per decl: id, kind, sig, file, line, end_line, hash, and when present
  doc (its doc comment: the // lines directly above it, with no blank line
  between), size (struct bytes), test. --uses adds used_by: {package:
  refs}, so {} is dead code and a decl used by only one other package is a
  candidate to move there. Paged like grep: at most 200 records, and the
  last line says where the next page starts. A name declared twice in a
  package lists each copy where it is, with its own hash and id_copies:N
  (see ovid help ids).
ovid show <id|name>... [--plain] [--json] [--exprs]
  Text: "// kind id file:a-b hash=H in=decl type=T" then the source, with
  "  // @id" after each line where a statement starts (--plain omits them).
  A decl's source starts at its doc comment, and a-b covers it.
  For a statement or expression (a decl with --exprs), one line per
  expression inside it follows: "//   ex:id line:col text  hash=H type=T",
  in source order, outer before inner. --json: {id,kind,file,line,end_line,
  hash,decl,parent,text,sig,type}, plus doc and doc_line (where it starts;
  line is the decl's own first line) for a decl with a doc comment, and
  "exprs":[{id,line,col,text,hash,type}]. Replace one by id to change part
  of a statement.
ovid refs <id|name> [--offset N] [--limit N]
  {id,kind,in,file,line,col,source} per use, in
  source order: the names the checker resolved to it, so a field or local
  spelled like a type, func, or const is not a use of it; last:
  {"ok":true,target,files,by_pkg:{package: n},external} for all the uses,
  and the paging fields for the ones printed.
ovid grep <regexp> [--pkg P] [--std] [--offset N] [--limit N]
  {file,line,col,match,source,decl,stmt} per match (RE2 syntax).
  A match in a doc comment is in that comment's decl.
  Paging, for outline, refs, and grep: at most 200 records unless --limit
  (0: all), starting after --offset; last: {"ok",count,total,offset,
  has_more,next_offset,revision}. count is what was printed, total all there
  is; pass next_offset as --offset for the next page, and if revision has
  changed between pages, start again.
ovid edit <file|-> [--rev REV] [--dry-run] [--require-clean|--allow-broken] [--show]
  [--force]   see: ovid help edit
ovid replace <id> | insert --after <id> | insert --before <id> | append <id>
  | delete <id>   --expect H | --rev REV | --force   [--text-file F] [--dry-run]
  [--require-clean|--allow-broken] [--show]
  One edit op; the text is read from stdin (or F), so a heredoc works:
    ovid replace st:app.main:3 --expect 1f0c9a2b7d4e <<'EOF'
    ovid/io.Stdout(strptr("a \"quoted\" line\n"), strlen("a \"quoted\" line\n"))
    EOF
  Same checks and result as ovid edit. Every op needs a guard: --expect
  (the hash of the node it names), --rev (the module revision), or --force;
  only append <pkg> goes without.
ovid rename <id|name> <new> [--dry-run]
  Rewrites the declaration's name and each use refs lists, nothing else (a
  field, local, comment, or string spelled the same is left alone); refuses
  collisions and changes that add check errors. Like move it takes no
  --expect: it carries no code, is planned from the module as it is, and a
  replay is refused.
ovid move <id|name>... <pkg> [--file pkg/x.ov] [--dry-run]
  Moves funcs, types, or consts (with doc comments) to pkg, creating it if
  needed; requalifies every use and adds the imports files now need.
  Refuses changes that add check errors. Several names move in order, all or
  none: each move is planned in memory over the ones before it, and only
  when all pass are the files written, together; --dry-run writes nothing.
  One receipt per name, then {"ok":true,"moved":[...],"to","written"}.
ovid init <dir> [--name N]   writes ovid.mod, <N>/main.ov, <N>/main_test.ov
ovid dump [--pkg P] [-o file]
  the program as one JSON document, not paged and large (megabytes for a
  few thousand lines): for tools, not for reading. --pkg keeps one package;
  -o writes it to a file and prints {"ok",output,bytes,revision} instead.
  A string literal that is not UTF-8 is "value_hex", not "value".
ovid version                 {commit, dirty, binary (hash of the executable), path}
ovid help [topic|command]    a topic, or the entry above for one command
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
Keys are exact: one that is not listed here, one given twice, or one
that another op takes (a replace with "before", a delete with "text"), fails
with bad_edit and names it; nothing is written. The same holds for the
flags of ovid replace/insert/append/delete.
Every op needs a guard, or it is refused (expect_required, nothing
written): "expect":HASH, the hash of the node it names (from outline, show,
or a receipt); or a top-level "revision" (from check, outline, or a
receipt; --rev on the command line), which guards the whole module; or
--force. If the node changed since it was read the edit is refused with
exit 2 and its current hash and text. The one exception is append into a
package path: it names no node and overwrites nothing, and a replay is
refused as a duplicate name, so it needs no guard (and takes no expect).
A st:/ex: id is a position, renumbered by any insert above it. Its own hash
or its decl's (the one in the header ovid show prints) both work, and both
are bound to the decl as it was read: after any change to that decl the
edit is stale, so a retried or late edit never lands on the statement that
took its id, not even an identical twin. Edits to different decls do not
disturb each other. Re-read with ovid show (or use the decl hash in the
last receipt). A current hash passed with the wrong id is refused and names
its node. --force skips every guard.
An id that several nodes share (a name declared twice; check reports it)
needs the hash of the copy to edit as expect: without one the op fails
with ambiguous_id and "copies":[{file,line,end_line,hash}], and --rev or
--force does not pick one. Copies with the same hash are the same text;
the op takes the first.

ID may be a full id or a decl name (Sum, util.Sum). Text is plain Ovid; its
indentation is normalised to the target's. insert anchors on statements and
decls; to change part of a statement, replace one of its expressions (ovid
show <stmt> lists them with ids and hashes).

A func's, type's, or const's doc comment (the // lines directly above it,
no blank line between) is part of it, as ovid show prints it: replace puts
the text's own doc comment in its place, and text without one keeps it;
delete removes it; insert before puts the new text above it.

After applying, the module is reparsed (a syntax error rejects everything
and names the op) and checked. Result: {"ok":true,"written","files",
"check_ok","errors","errors_before","revision","ops":[{"ids":[...],
"decls":[{"id","hash"}]}]}: ids are the nodes the op wrote, decls the
top-level decls it touched with their new hashes (--show adds "text"), so a
follow-up edit can "expect" them without reading again: the decl it wrote
into, or each decl its text holds (append or insert of several, or a decl
replaced by several), in source order; a delete lists the decl it was in,
none for a whole decl.

Examples:
  fix one argument:   {"op":"replace","id":"ex:app.main:2","expect":H,"text":"2"}
  rewrite a statement: {"op":"replace","id":"st:app.main:3","expect":H,
                        "text":"if n > 0 {\n  return n\n}"}
  replace a whole func by name, guarded:
    {"op":"replace","id":"Sum","expect":"c67681b88f86","text":"func Sum(...) i64 {...}"}
  add a func in a new file: {"op":"append","into":"app/util",
    "file":"app/util/extra.ov","text":"func Half(x i64) i64 {\n  return x / 2\n}"}
An edit that adds check errors is refused and nothing is written;
--require-clean also refuses one that leaves any, and --allow-broken
writes it anyway (one step of a change that spans several edits).
--dry-run applies in memory and reports, without writing; it fails
(ok:false, exit 1) if the change would add errors.
Writers (edit, rename, move) take a lock on ovid.mod. If another holds it,
one {"fact":"waiting","for":"lock"} line is printed, and after 10s
(OVID_LOCK_TIMEOUT=30s, 500ms, 0 changes it) the command fails with
lock_timeout, writing nothing.
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
writing over text that changed since it was read. A func's, type's, or
const's text includes its doc comment, so editing the comment changes the
hash of the decl and of every statement in it. A st:/ex: id is a
position, so its hash also covers the whole decl it is in: any change to
that decl, anywhere in it, makes every statement hash read before it stale,
while a change to another decl leaves them alone (see ovid help edit).
A name declared twice in a package gives two nodes one id (check reports
it). outline and show list each copy at its own file:line with its own
hash (show types only the first copy, the one check checks); an edit
picks one by that hash as expect (else ambiguous_id), and
refs, rename, and move refuse the id until one copy is gone.
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
		if e := commandHelp(topic); e != "" {
			fmt.Fprint(w, e)
			fmt.Fprintln(w, "\nAll commands: ovid help commands. Topics: language commands edit std ids.")
			return ExitOK
		}
		fmt.Fprintf(w, "no help topic %q; topics: language commands edit std ids, or a command name\n", topic)
		return ExitUsage
	}
	return ExitOK
}

// commandHelp is the part of helpCommands about the command cmd: each
// entry whose heading (its "ovid ..." line and any "  | ..." lines after
// it) names cmd, so ovid help delete finds the entry shared by the edit
// ops. "" if there is none.
func commandHelp(cmd string) string {
	re := regexp.MustCompile(`(^ovid |\| )` + regexp.QuoteMeta(cmd) + `\b`)
	var out, entry, head strings.Builder
	flush := func() {
		if re.MatchString(head.String()) {
			out.WriteString(entry.String())
		}
		entry.Reset()
		head.Reset()
	}
	inHead := false
	for _, ln := range strings.SplitAfter(helpCommands, "\n") {
		switch {
		case strings.HasPrefix(ln, "ovid "):
			flush()
			inHead = true
		case inHead && strings.HasPrefix(ln, "  | "):
		default:
			inHead = false
		}
		if inHead {
			head.WriteString(ln)
		}
		entry.WriteString(ln)
	}
	flush()
	return out.String()
}

// helpStd lists the shipped packages' declarations from their source.
func helpStd(w io.Writer) {
	fmt.Fprintln(w, "Shipped packages. Import them by path; a module cannot have a package of the same path.")
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

// docComment returns the doc comment of the decl at off (module.DocStart)
// as one line: the text of its // lines, joined by spaces.
func docComment(src []byte, off int) string {
	lo := module.DocStart(src, off)
	if lo == off {
		return ""
	}
	var doc []string
	for _, ln := range strings.Split(string(src[lo:lineBegin(src, off)]), "\n") {
		if t := strings.TrimSpace(ln); t != "" {
			doc = append(doc, strings.TrimSpace(strings.TrimPrefix(t, "//")))
		}
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
  ovid/io.Print(Greeting())
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
