# Ovid

Ovid is a small compiled language whose toolchain is built for agents. A
program is plain `.ov` text; every tool command answers in JSON lines
(the read commands as text, with `--json` for the records); every
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
ovid help           # overview; `ovid help agent` is everything before a first edit
```

## A module

```text
ovid.mod              module <name> / entry <pkg>
<pkg path>/*.ov       one directory per package; any number of files
```

The entry package has `func main(io *ovid/io.Cap) i64`, or, for an HTTP
handler, `func handle(io *ovid/io.Cap, req *ovid/http.Request, res
*ovid/http.Response) i64` and no main: `ovid build` then writes the main
itself, a host that reads requests from stdin one after another
(Content-Length framed), writes each response to stdout, and resets the heap
between them.

The package path is the directory path. `ovid/io`, `ovid/mem`, `ovid/test` (`Eq`, `True`: a failing check prints got/want), and `ovid/http` (an HTTP handler's request and response, and a host that serves requests over stdin and stdout) ship with
the toolchain (`std/`, embedded in the binary). A module cannot replace them:
a package of its own with one of those paths is an error (`reserved_path`),
and `ovid.mod` has no way to name another standard library. Only the shipped
`ovid/io` may call `syscall`, so what a checked program can ask of the kernel
is what `std/ovid/io` asks. `ovid build` lists the system calls the program
it wrote can reach (`"syscalls":[1,9,60]` for hello), which is enough to run
it under a seccomp filter that allows nothing else.

## Commands

Every command prints JSON lines and the last line has `"ok"`, except
`help`, `run`, `dump` without `-o` (one JSON document), and the read
commands (`outline`, `show`, `refs`, `grep`), which print text by default:
`outline`, `refs`, and `grep` still end with the JSON `"ok"` line, a
successful `show` prints a header and the source with no JSON line, and
`--json` makes any of the four print records. Exit codes:
0 ok, 1 errors, 2 stale edit, 64 usage, 124 `run --timeout` ended the program, 125 `run` could not build or start it.

| command | what |
|---|---|
| `ovid init <dir>` | new module with a main and a test |
| `ovid check [--facts]` | errors, then a summary with the module revision |
| `ovid build [-o out]` / `ovid run [-- args]` | compile, leaving out `_test.ov` files; run passes stdio and the exit code through and reports a death by signal on stderr, on Linux with the statement and call stack |
| `ovid run --json [--timeout 5s] [--max-output N]` | run with the output captured: one last line with `exit` or `signal`, `stdout`, `stderr`, and `truncated` |
| `ovid test [--run substr] [--list]` | each `TestX(io *ovid/io.Cap) i64` in its own process; 0 passes; a failure names the `return` that produced it, a crash its signal (on Linux, the statement and call stack) |
| `ovid outline [--pkg P] [--ids]` | packages, or a package's decls one line each: line and signature (a struct by its field count), with id and hash after `--ids` |
| `ovid show <id\|name>... [--ids] [--exprs]` | source of a node (a decl with its doc comment) under a header with its id and hash; `--ids` tags each statement line with its id; a statement's expressions are listed with ids and hashes |
| `ovid refs <id\|name>` | every use the checker resolves to a func, type, field, const, param, or local, grouped by the decl it is in (`--name` picks one of the two locals a two-result `var` declares) |
| `ovid grep <regexp>` | matching lines grouped by file and enclosing decl |
| `--json` | on `outline`, `show`, `refs`, `grep`: one JSON record per line instead of text |
| `--offset N`, `--limit N` | `outline`, `refs`, and `grep` print 200 records a page; the last line has `total`, `has_more`, and `next_offset` |
| `ovid edit <file\|-> [--show]` | batch of replace/delete/insert/append ops, planned and checked as one (a refused batch writes nothing); returns new ids and hashes |
| `ovid replace <id>`, `insert --after <id>`, `append <id>`, `delete <id>` | one edit op with its code on stdin, so a heredoc needs no JSON escaping |
| `ovid rename <id\|name> <new>` | rewrites the declaration and the uses the checker resolved to it, never a field or local spelled the same; refuses collisions and new errors |
| `ovid move <id\|name>... <pkg>` | move decls to another (or a new) package, planned and checked as one (a refused move writes nothing); requalifies uses, adds imports |
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

`i64`, `bool`, `bytes`, and pointers to structs (`*T`). Struct fields are 8 bytes (a `bytes` field 16) and `sizeof(T)` gives a struct's size.
A `bytes` is an address and a length: `"lit"` is one in read-only memory, `bytes(p, n)` one over memory of your own, `b[i]` the byte (and `b[i] = v` stores one), `b[i:j]` the subrange without a copy, `len(b)` the length; every index and bound is checked, and a failed check traps.
Read-only constant tables (`const T [N]i64 = {...}`, read as `T[i]` with a bounds check, `len(T)`).
No struct values, globals, function pointers, methods, generics, or implicit
allocation. Parameters take at most six words (a `bytes` is two); one result, or a value and an error code (`func F() (i64, i64)`, received as `var v i64, e i64 = F()`; a call that is not received is a check error). Operators follow Go
precedence and are signed; `>>` is arithmetic, and `ushr`, `umulhi`, `udiv`, `urem`, and `ult` are the unsigned forms. `strptr("…")`
and `strlen("…")` are a literal's address and length as two `i64`, and `ptr(b)` a bytes' address, for a syscall path. Memory is `load8/16/32/64`, `store8/16/32/64`, `bswap16/32/64`, and
`ovid/io.Alloc`. `main` is `func main(io *ovid/io.Cap) i64`; `io` is the
capability for argv, the heap, syscalls, and files, and `syscall` is only allowed in
`ovid/io`. Every `ovid/io` func that opens, names, or creates a file, or gets a
stream, takes `io`, and one that uses an open file takes its handle:
`ovid/io.Open(io, path, flags, mode)` returns a `*ovid/io.File` for `Read`,
`Write`, and `Close`, `ovid/io.Stdout(io)` and its kin are the standard streams,
and `ovid/io.Print(io, b)` writes to stdout. Other packages' funcs and consts spell the import path:
`ovid/mem.Copy(dst, src)`, `ovid/io.O_RDONLY`; a one-segment import may be
written `util.F()`. Imports may not form a cycle (`import_cycle`), so the
packages are a DAG.

## Self-hosting

`prog/` is the compiler again, written in Ovid (about 11,000 lines across eight
packages, including SHA-256, an x86-64 assembler, an ELF writer, and the `bytes` lowering). It reads `ovid.mod` and `.ov` files itself and offers only
`check`, `build`, `dump`, and `refs`; give it the standard library with
`--std <dir>`, since only the Go binary embeds it. Its `refs` prints what
the Go toolchain's does, from the uses its checker records; the other
agent commands (`show`, `grep`, `edit`, `rename`, `move`, `test`) exist
only in the Go toolchain.

```sh
bin/ovid build -C prog -o /tmp/s1          # Go compiles the Ovid compiler
/tmp/s1 build prog -o /tmp/s2 --std std    # which compiles itself
cmp /tmp/s1 /tmp/s2                        # byte-identical
```

The two compilers emit byte-identical binaries for the same source, and
`go test ./internal/tool -run TestSelfHost` checks it. The self-hosted
compiler is about 170 KB (2026-10-09; the `bytes` lowering added 15 KB on 2026-10-07), and `TestSelfHostSize` fails if it outgrows its
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

## Nix

```sh
nix run github:ovid-sh/ovid -- check   # the toolchain, nothing installed
nix develop                            # go, gopls, jq, and ovid
nix flake check                        # go test; on x86_64-linux also prog/, self-hosting, a NixOS VM
```

Ovid's output is static and has no libc, so it runs on NixOS as built: no
patchelf, no nix-ld. `lib.buildOvidProgram pkgs { pname; src; }` builds a
module into `$out/bin` and keeps the build receipt (size, system calls) in
`$out/share/ovid`; `packages.hello-image` is a container holding that
binary and nothing else. A flake sees only files git tracks: `git add` a
new `.ov` file before `nix build` can find it.

On NixOS, `nixosModules.default` runs such a program as a service, and the
receipt becomes its seccomp filter:

```nix
services.ovid.programs.cat = { package = catfile; args = [ "/etc/os-release" ]; };
```

`ovid-cat.service` runs sandboxed (`DynamicUser`, `ProtectSystem=strict`,
...) with `SystemCallFilter` set to the receipt's system calls by name,
plus the few systemd always allows; any other call kills it with SIGSYS.
`lib.syscallFilter` makes that filter for a unit of your own.

An HTTP handler serves the network with no code of its own for it:

```nix
services.ovid.programs.greet = { package = greet; listen = 8080; };
```

systemd listens on `ovid-greet.socket` and starts `ovid-greet@.service`
for each connection, with the connection as standard input and output,
which the host `ovid build` writes for a handler already reads and writes.
That process serves the connection's requests in turn and never gets a
system call to open a socket of its own. The host waits for a request
with no timeout, so each connection lives at most `RuntimeMaxSec` (60 s by
default), and on a port one IP address holds at most 8 of the socket's 64
connections. Behind a proxy every client is the proxy's address: raise
`MaxConnectionsPerSource` there (a unix socket path has no such limit).

## Not yet

No HTTP client, no URL imports, no package
server. See `COMMITMENTS.md`.
