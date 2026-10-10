# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Ovid is a small compiled language whose toolchain is built for agents: commands answer in JSON lines (except `help`, plain `show`, and a running program's own output under `run`), and source can be edited through id-addressed, hash-guarded edits. The compiler emits static Linux x86-64 ELF binaries with no libc. It exists twice:

- **Go toolchain** (`cmd/ovid`, `internal/`): the full toolchain, including the agent commands (`outline`, `show`, `refs`, `grep`, `edit`, `rename`, `move`, `test`).
- **Self-hosted compiler** (`prog/`, written in Ovid): only `check`, `build`, `dump`, and `refs` (its checker records the uses it resolves, as the Go checker's `Result.Uses` does). It needs `--std <dir>` because only the Go binary embeds the standard library.

## Commands

```sh
go build -o bin/ovid ./cmd/ovid        # bin/ is gitignored
go vet ./...
go test ./...                           # everything, a few seconds (most of it internal/tool)
go test ./internal/tool -run TestCorpus                 # the tests/ corpus
go test ./internal/tool -run TestCorpusRun/run/hello    # one corpus case
go test ./internal/tool -run TestSelfHost               # Go and Ovid compilers agree byte for byte
go test ./internal/tool -run 'TestProgChecks|TestProgTests'   # prog/ checks, and its Ovid tests pass

bin/ovid check -C prog                  # typecheck the self-hosted compiler
bin/ovid test -C prog [--run substr]    # run its TestX funcs (*_test.ov)
bin/ovid help language                  # the full language reference
```

The self-hosting loop by hand:

```sh
bin/ovid build -C prog -o /tmp/s1          # Go compiles the Ovid compiler
/tmp/s1 build prog -o /tmp/s2 --std std    # which compiles itself
cmp /tmp/s1 /tmp/s2                        # must be byte-identical
```

`flake.nix` packages the same (`nix flake check` runs the Go suite everywhere, and on x86_64-linux, where Ovid output can run, also the `prog/` checks, the self-hosting fixed point, and a NixOS VM test). Nix sees only git-tracked files, so `git add` new files before any `nix` command; a new top-level directory the Go tests read must also join `testSrc` there.

No third-party Go dependencies (`go.mod` has none). Built binaries only execute on Linux x86-64. On other hosts, tests that execute compiled programs (the corpus, `internal/asm`, `internal/compile`, `internal/tool`) still build and check, then skip the run; the `*_linux_amd64_test.go` files build only on Linux x86-64.

## Architecture

The Go pipeline, one package per stage under `internal/`:

```
.ov text → syntax (parse) → ir (tree, ids + spans) → check (types) → lower (bytes → pairs of i64, on a copy) → compile (x86-64 via asm) → elf (link)
```

- `module` sits in front: finds `ovid.mod`, loads one directory per package, resolves shipped packages from the embedded `std/` (`std/std.go`), indexes every node by id to file:line:col, computes hashes and the module `revision`, and owns durable writes (`write.go`: temp file, fsync, rename) and the `ovid.mod` flock (`lock_unix.go`: non-blocking, retried until `OVID_LOCK_TIMEOUT`, default 10s, then `lock_timeout`).
- `ir` is the program tree. `.ov` text is the source of truth; the JSON form (`ovid dump`) is a derived view.
- `lower` runs inside `compile.CompileAll` on a deep copy of the checked program: every `bytes` becomes a pair of `i64` locals/params/fields/results (`b`, `b#n`), `b[i]` a `bload`, the checks `chk` statements, `(bytes, i64)` a third result word in `rcx`. The code generator keeps one word per value; the module's own tree, which the tools and crash reports read by id, is untouched. `prog/ovid/lower` is the same pass in Ovid.
- `tool` implements the commands; `cmd/ovid/main.go` is flag parsing and dispatch, plus `version`, which it implements itself. Each command function takes a dir and an `io.Writer` and returns the exit code, which is how `tool_test.go` drives them in-process.
- `tool/help.go` holds the `ovid help` texts, including the language reference.

### Two compilers that must stay identical

`prog/ovid/{parse,check,lower,cg,asm,elf}` mirror `internal/{syntax,check,lower,compile,asm,elf}` (plus `sha`, `cli`). `TestSelfHost` requires the two to emit **byte-identical** binaries for the same source and compares the self-hosted checker's diagnostics. So a change to the language, the checker's diagnostics, codegen, the assembler, or the ELF layout must be made in both `internal/` and `prog/`, and `std/` changes affect both. The self-hosted compiler is the language (`COMMITMENTS.md`): design such a change in `prog/` first and have `internal/` follow it in the same PR. The README also describes the language and commands; keep it in step. It gives the self-hosted compiler's size and build time only roughly and dated: do not update them for an ordinary change, since every PR editing that line conflicts with every other. Exact numbers come from the bench: `go test ./internal/tool -run '^$' -bench .` (`internal/tool/bench_test.go`), which CI runs and records in the job summary. `TestSelfHostSize` fails when the self-hosted compiler outgrows `selfHostBudget`.

### The output contract

`docs/PROTOCOL.md` is the contract consumers rely on: JSON lines with a final `"ok"` line (`help` and `show` without `--json` print text; `run` passes the program's stdio and exit code through and writes JSON only when the build fails), exit codes (0 ok, 1 errors, 2 stale edit, 64 usage, 125 `run` could not build), the failure `error` codes, diagnostic `code`s, id forms, hashes, and edit receipts. Adding or changing an error code, diagnostic code, or receipt field means updating that file (and `ovid help commands`/`ids`/`edit` in `help.go`). New keys may be added; existing ones keep their meaning.

### Ids, hashes, and edits

Decl ids (`fn:pkg.Name`, `ty:`, `cn:`, `fld:`, `pa:`, `pkg:`, `im:`) are names and stable. `st:`/`ex:` ids are positions numbered per function in parse order, so any insert or delete above renumbers them, and their hashes are bound to the enclosing decl's whole text. Every edit op must carry a guard (`expect`, the request `revision`/`--rev`, or `--force`), decl ids included; only `append` into a package path goes without. Request keys are decoded strictly (unknown or repeated keys are `bad_edit`). An edit is planned and checked as one: under the module lock it is planned, the module is reparsed and checked in memory (`module.LoadOverlay`), and it is refused, writing nothing, if it adds check errors (the guard compares the errors themselves, not their count) or if a hash/revision is stale (exit 2). Writing the files is not atomic across files (`module.WriteFiles` renames them one by one): a `write` failure lists `written_files` and `unwritten_files`, and `docs/PROTOCOL.md` says how to recover.

## Tests

- `tests/run/` and `tests/fail/` are a corpus of Ovid programs whose expectations are comments in the source (`// exit:`, `// stdout:`, `// args:`, and `// error: <code> [col]` at the end of the offending line). Add a test by adding a file; a single `.ov` file uses `package demo`, a directory brings its own `ovid.mod`. A `fail/` case must produce exactly the diagnostics named. Details in `tests/README.md`.
- `internal/tool/tool_test.go` covers the commands end to end on temp modules (`mkmod`, `demo` helpers).
- `prog/ovid/*/*_test.ov` are Ovid-language tests (`func TestX(io *ovid/io.Cap) i64`, 0 passes), run by `ovid test` and from Go by `TestProgTests`.

## Writing Ovid (for `prog/`, `std/`, `tests/`)

Only `i64`, `bool`, `bytes` (an address and a length, two words: `"lit"`, `bytes(p, n)`, `b[i]`, `b[i] = v`, `b[i:j]`, `len(b)`, all checked), `error`, and `*Struct`; every struct field is 8 bytes, a `bytes` field 16. No struct values, globals, function pointers, methods, or generics; at most six parameter words (a `bytes` is two) and one result, or a value and an error (`(T, error)`, received by `var v T, e error = f(...)` or `v, e = f(...)`, `_` discarding one; an unreceived call is `unused_result`). An `error` is one word of its own type: `0` is success, an error const (`const E_FULL error = 1`; `ovid/io.E_NOENT` and the rest are the kernel's errno) a failure; only `==` and `!=` against `0` or another error apply (no arithmetic, no `<`, no `if e`), an error never stands for an `i64` nor an `i64` for an error (`type_mismatch`), and `e as i64` / `n as error` convert on purpose. A `const Name [N]i64 = {...}` is a read-only table in rodata, read as `Name[i]` (bounds-checked, a failed check traps) and `len(Name)`. `strptr("…")`/`strlen("…")` are a literal's address and length as two `i64`, `ptr(b)` a bytes' address, for the syscall paths in `ovid/io`; `ovid/io` and `ovid/mem` take and return `bytes` (`ovid/http` still takes pairs). Memory is `load8/16/32/64`, `store8/16/32/64`, `bswap16/32/64`, and `ovid/io.Alloc` (returns zeroed memory). `io *ovid/io.Cap` is the capability for argv, heap, syscalls, and files and is threaded through explicitly (every `ovid/io` func that opens, names, or creates a file, or gets a stream, takes it: `Open(io, …)` returns a `*ovid/io.File`, which `Read`/`Write`/`Close` take; `Print(io, b)`, `Stdin(io)`); `syscall` is only allowed inside `ovid/io`. Cross-package names spell the import path: `ovid/mem.Copy(dst, src)`. The package path is the directory path, and a file may not sit in the module root.

## Scope

v0 resolves every import on the local filesystem. `COMMITMENTS.md` records what is planned next (HTTP as the first standard library, URL imports) and is not implemented.
