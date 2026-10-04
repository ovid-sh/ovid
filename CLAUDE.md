# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Ovid is a small compiled language whose toolchain is built for agents: commands answer in JSON lines (except `help`, plain `show`, and a running program's own output under `run`), and source can be edited through id-addressed, hash-guarded edits. The compiler emits static Linux x86-64 ELF binaries with no libc. It exists twice:

- **Go toolchain** (`cmd/ovid`, `internal/`): the full toolchain, including the agent commands (`outline`, `show`, `refs`, `grep`, `edit`, `rename`, `move`, `test`).
- **Self-hosted compiler** (`prog/`, written in Ovid): only `check`, `build`, and `dump`. It needs `--std <dir>` because only the Go binary embeds the standard library.

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

No third-party Go dependencies (`go.mod` has none). Built binaries only execute on Linux x86-64. On other hosts, tests that execute compiled programs (the corpus, `internal/asm`, `internal/compile`, `internal/tool`) still build and check, then skip the run; the `*_linux_amd64_test.go` files build only on Linux x86-64.

## Architecture

The Go pipeline, one package per stage under `internal/`:

```
.ov text → syntax (parse) → ir (tree, ids + spans) → check (types) → compile (x86-64 via asm) → elf (link)
```

- `module` sits in front: finds `ovid.mod`, loads one directory per package, resolves shipped packages from the embedded `std/` (`std/std.go`), indexes every node by id to file:line:col, computes hashes and the module `revision`, and owns durable writes (`write.go`: temp file, fsync, rename) and the `ovid.mod` flock (`lock_unix.go`: non-blocking, retried until `OVID_LOCK_TIMEOUT`, default 10s, then `lock_timeout`).
- `ir` is the program tree. `.ov` text is the source of truth; the JSON form (`ovid dump`) is a derived view.
- `tool` implements the commands; `cmd/ovid/main.go` is flag parsing and dispatch, plus `version`, which it implements itself. Each command function takes a dir and an `io.Writer` and returns the exit code, which is how `tool_test.go` drives them in-process.
- `tool/help.go` holds the `ovid help` texts, including the language reference.

### Two compilers that must stay identical

`prog/ovid/{parse,check,cg,asm,elf}` mirror `internal/{syntax,check,compile,asm,elf}` (plus `sha`, `cli`). `TestSelfHost` requires the two to emit **byte-identical** binaries for the same source and compares the self-hosted checker's diagnostics. So a change to the language, the checker's diagnostics, codegen, the assembler, or the ELF layout must be made in both `internal/` and `prog/`, and `std/` changes affect both. The README also describes the language and commands; keep it in step. It gives the self-hosted compiler's size and build time only roughly and dated: do not update them for an ordinary change, since every PR editing that line conflicts with every other. Exact numbers belong to the bench (#11).

### The output contract

`docs/PROTOCOL.md` is the contract consumers rely on: JSON lines with a final `"ok"` line (`help` and `show` without `--json` print text; `run` passes the program's stdio and exit code through and writes JSON only when the build fails), exit codes (0 ok, 1 errors, 2 stale edit, 64 usage, 125 `run` could not build), the failure `error` codes, diagnostic `code`s, id forms, hashes, and edit receipts. Adding or changing an error code, diagnostic code, or receipt field means updating that file (and `ovid help commands`/`ids`/`edit` in `help.go`). New keys may be added; existing ones keep their meaning.

### Ids, hashes, and edits

Decl ids (`fn:pkg.Name`, `ty:`, `cn:`, `fld:`, `pa:`, `pkg:`, `im:`) are names and stable. `st:`/`ex:` ids are positions numbered per function in parse order, so any insert or delete above renumbers them; edits to them must carry an `expect` hash. An edit is all-or-nothing: under the module lock it is planned, the module is reparsed and checked in memory (`module.LoadOverlay`), and it is refused if it adds check errors (the guard compares the errors themselves, not their count) or if a hash/revision is stale (exit 2).

## Tests

- `tests/run/` and `tests/fail/` are a corpus of Ovid programs whose expectations are comments in the source (`// exit:`, `// stdout:`, `// args:`, and `// error: <code> [col]` at the end of the offending line). Add a test by adding a file; a single `.ov` file uses `package demo`, a directory brings its own `ovid.mod`. A `fail/` case must produce exactly the diagnostics named. Details in `tests/README.md`.
- `internal/tool/tool_test.go` covers the commands end to end on temp modules (`mkmod`, `demo` helpers).
- `prog/ovid/*/*_test.ov` are Ovid-language tests (`func TestX(io *ovid/io.Cap) i64`, 0 passes), run by `ovid test` and from Go by `TestProgTests`.

## Writing Ovid (for `prog/`, `std/`, `tests/`)

Only `i64`, `bool`, and `*Struct`; every struct field is 8 bytes. No struct values, globals, function pointers, methods, or generics; at most six parameters and one result. Strings exist only as `strptr("…")`/`strlen("…")`, so string data is passed as pointer + length pairs of `i64`. Memory is `load8/32/64`, `store8/64`, and `ovid/io.Alloc` (returns zeroed memory). `io *ovid/io.Cap` is the capability for argv, heap, and syscalls and is threaded through explicitly; `syscall` is only allowed inside `ovid/io`. Cross-package names spell the import path: `ovid/mem.Copy(d, s, n)`. The package path is the directory path, and a file may not sit in the module root.

## Scope

v0 resolves every import on the local filesystem. `COMMITMENTS.md` records what is planned next (HTTP as the first standard library, URL imports) and is not implemented.
