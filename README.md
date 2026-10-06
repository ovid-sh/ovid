# Ovid

Ovid is a small compiled language whose toolchain is built for agents. A
program is plain `.ov` text; every tool command answers in JSON lines; every
error carries a file, line, column, the offending source line, and when
known what was expected, what was found, and a hint. Edits can be made by
hand or through id-addressed batch edits that are checked before they are
written.

The compiler emits static Linux x86-64 ELF binaries with no libc. It is
written twice: in Go (`cmd/ovid`, the toolchain agents use today) and in
Ovid itself (`prog/`, the self-hosted compiler).

## Quick start

```sh
go build -o bin/ovid ./cmd/ovid
bin/ovid init hello && cd hello
ovid run            # hello, world
ovid test           # runs TestGreeting
ovid help           # overview; `ovid help language` is the full language
```

## A module

```text
ovid.mod              module <name> / entry <pkg>
<pkg path>/*.ov       one directory per package; any number of files
```

The package path is the directory path. `ovid/io`, `ovid/mem`, `ovid/test` (`Eq`, `True`: a failing check prints got/want), and `ovid/http` (an HTTP handler's request and response, and a host that serves one request over stdin and stdout) ship with
the toolchain (`std/`, embedded in the binary). A module cannot replace them:
a package of its own with one of those paths is an error (`reserved_path`),
and `ovid.mod` has no way to name another standard library. Only the shipped
`ovid/io` may call `syscall`, so what a checked program can ask of the kernel
is what `std/ovid/io` asks. `ovid build` lists the system calls the program
it wrote can reach (`"syscalls":[1,9,60]` for hello), which is enough to run
it under a seccomp filter that allows nothing else.

## Commands

Every command prints JSON lines and the last line has `"ok"`. Exit codes:
0 ok, 1 errors, 2 stale edit, 64 usage, 124 `run --timeout` ended the program, 125 `run` could not build or start it.

| command | what |
|---|---|
| `ovid init <dir>` | new module with a main and a test |
| `ovid check [--facts]` | errors, then a summary with the module revision |
| `ovid build [-o out]` / `ovid run [-- args]` | compile, leaving out `_test.ov` files; run passes stdio and the exit code through and reports a death by signal on stderr, on Linux with the statement and call stack |
| `ovid run --json [--timeout 5s] [--max-output N]` | run with the output captured: one last line with `exit` or `signal`, `stdout`, `stderr`, and `truncated` |
| `ovid test [--run substr] [--list]` | each `TestX(io *ovid/io.Cap) i64` in its own process; 0 passes; a failure names the `return` that produced it, a crash its signal (on Linux, the statement and call stack) |
| `ovid outline [--pkg P]` | packages, or a package's decls with signature, doc, struct size, lines, hash |
| `ovid show <id\|name>... [--plain] [--exprs]` | source of a node (a decl with its doc comment), each statement line tagged with its id; a statement's expressions listed with ids and hashes |
| `ovid refs <id\|name>` | every use the checker resolves to a func, type, field, const, param, or local |
| `ovid grep <regexp>` | text matches, each tagged with its enclosing decl and statement id |
| `--offset N`, `--limit N` | `outline`, `refs`, and `grep` print 200 records a page; the last line has `total`, `has_more`, and `next_offset` |
| `ovid edit <file\|-> [--show]` | batch of replace/delete/insert/append ops, all or nothing; returns new ids and hashes |
| `ovid replace <id>`, `insert --after <id>`, `append <id>`, `delete <id>` | one edit op with its code on stdin, so a heredoc needs no JSON escaping |
| `ovid rename <id\|name> <new>` | rewrites the declaration and the uses the checker resolved to it, never a field or local spelled the same; refuses collisions and new errors |
| `ovid move <id\|name>... <pkg>` | move decls to another (or a new) package, all or none; requalifies uses, adds imports |
| `ovid dump [--pkg P] [-o file]` | the program tree as one JSON document (large, for tools); `--pkg` keeps one package, `-o` writes it to a file |

All commands take `-C <dir>`; by default they use the module that contains
the working directory. `ovid help commands`, `ovid help edit`, and
`ovid help ids` document the output formats; [docs/PROTOCOL.md](docs/PROTOCOL.md)
is the contract they share: exit codes, error codes, and receipts.

An edit names nodes by id (`fn:pkg.Name`, `st:pkg.Func:3`, ...; see
`ovid help ids`) or by name (`Sum`, `util.Sum`, `Pair.next`), and carries
a guard: the hash that `outline`/`show` printed (`--expect`), the module
revision (`--rev`), or an explicit `--force`; only appending new decls to a
package goes without. If that node's text has changed since it was read,
the edit is refused with exit 2 and the current text.
Statement and expression ids are positions that an insert renumbers, so an
edit to one must carry a hash, and that hash covers the whole decl around
it: after any change to that decl the edit is refused, so it cannot land on
a statement that took the id, while edits to other decls are unaffected. After applying, the module is reparsed and
checked in memory. An edit that adds check errors is refused unless
`--allow-broken` is given; `--require-clean` also refuses one that leaves
any, and `--dry-run` never writes.

Agents can share a module. On Unix, writers take a lock on `ovid.mod` and
run one at a time, and files are replaced atomically, so no edit is lost to
another. A writer waits for the lock at most 10 s (`OVID_LOCK_TIMEOUT`),
saying so, then fails with `lock_timeout`. The lock does not make a stale read safe: when two agents read the
same code and both change it, only the hash catches the second one (exit 2,
re-read).

## Language, briefly

`i64`, `bool`, and pointers to structs (`*T`). Struct fields are 8 bytes and `sizeof(T)` gives a struct's size.
No struct values, globals, function pointers, methods, generics, or implicit
allocation. At most six parameters, one result. Operators follow Go
precedence; `>>` is arithmetic. String literals exist only as `strptr("…")`
and `strlen("…")`, and are read-only. Memory is `load8/32/64`, `store8/64`, and
`ovid/io.Alloc`. `main` is `func main(io *ovid/io.Cap) i64`; `io` is the
capability for argv, the heap, and syscalls, and `syscall` is only allowed in
`ovid/io`. Other packages' funcs and consts spell the import path:
`ovid/mem.Copy(d, s, n)`, `ovid/io.O_RDONLY`; a one-segment import may be
written `util.F()`. Imports may not form a cycle (`import_cycle`), so the
packages are a DAG.

## Self-hosting

`prog/` is the compiler again, written in Ovid (about 6,200 lines across seven
packages, including SHA-256, an x86-64 assembler, and an ELF writer). It reads `ovid.mod` and `.ov` files itself and offers only
`check`, `build`, and `dump`; give it the standard library with
`--std <dir>`, since only the Go binary embeds it. The agent commands
(`show`, `refs`, `grep`, `edit`, `rename`, `move`, `test`) exist only in the
Go toolchain.

```sh
bin/ovid build -C prog -o /tmp/s1          # Go compiles the Ovid compiler
/tmp/s1 build prog -o /tmp/s2 --std std    # which compiles itself
cmp /tmp/s1 /tmp/s2                        # byte-identical
```

The two compilers emit byte-identical binaries for the same source, and
`go test ./internal/tool -run TestSelfHost` checks it. The self-hosted
compiler is about 99 KB, and `TestSelfHostSize` fails if it outgrows its
budget. On starship (Ryzen 7 8745HS, 2026-10-04) it built `prog/` in 9 ms,
against 27 ms for the Go one, and a generated program of 100,000 funcs
(300,000 lines) in 0.57 s against 2.25 s. One command measures all of it:

```sh
go test ./internal/tool -run '^$' -bench .
```

## Tests

```sh
go test ./...
```

`tests/` is a corpus of Ovid programs with their expected exit code, output,
or diagnostics written as comments; add a test by adding a file. See
[tests/README.md](tests/README.md).

`tests/agent/` is an exercise for agents: tasks with goals a program
checks, from writing a small program to recovering from a stale edit. `go
test` checks that the reference solutions meet them;
`go run ./tests/agent/run` gives the tasks to a model and records how it
did. Re-run it when a command's output or defaults change, and record the
results in `docs/AGENT_FEEDBACK.md`. See
[tests/agent/README.md](tests/agent/README.md).

## Not yet

No HTTP client, no URL imports, no package
server. See `COMMITMENTS.md`.
