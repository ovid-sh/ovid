# The ovid command protocol

What a program driving `ovid` can rely on. `ovid help commands` lists each
command's fields; this file is the contract they share. It describes the Go
toolchain (`cmd/ovid`); the self-hosted compiler in `prog/` implements only
`check`, `build`, and `dump`.

## Output

Every command except `run`, `help`, and `show` without `--json` writes JSON
lines to stdout, one object per line, and nothing else. `show` prints the
source with an id comment on each line; its failures are JSON like any
other. The **last line**
always has a boolean `"ok"`; earlier lines are records (a diagnostic, a
match, a test result, a decl). Key order is not significant. A consumer
should read every line, take the last as the result, and ignore keys it does
not know: new keys are added without notice, existing ones keep their
meaning.

`run` passes the program's stdin, stdout, stderr, and exit code through. It
writes JSON to stdout only when the build fails (the diagnostics, then
`{"ok":false,"errors":N}`). If the program is killed by a signal, it writes
one line to **stderr**, after whatever the program wrote there:

```json
{"ok":false,"error":"killed","signal":"segmentation fault","exit":139,
 "at":{"id":"st:app.Get:1","file":"app/main.ov","line":11,"source":"  return n.v"},
 "stack":[AT, CALLER...],"fault_addr":"0x0","hint":TEXT}
```

`at` and `stack` (innermost first, frames outside the module left out) are
there for a fault (SIGSEGV, SIGBUS, SIGFPE, SIGILL) on Linux, where the
program runs under ptrace; `fault_addr` for SIGSEGV and SIGBUS. `ovid test`
reports crashes with the same fields. A ^C reaches the program, and ovid
stays to report it (`"signal":"interrupt"`).

`dump` writes the program tree as one JSON object. A `strptr`/`strlen`
literal's bytes are its `"value"`, or, when they are not valid UTF-8 (a
`\x` escape can make any byte), `"value_hex"`: two lowercase hex digits per
byte. Any other JSON string, such as a diagnostic's `source`, has each
byte that is not UTF-8 replaced by U+FFFD.

## Exit codes

| code | meaning |
|---|---|
| 0 | ok |
| 1 | the program has errors, or the request failed (see `error`) |
| 2 | stale: an edit's `expect` hash or `revision` no longer matches; nothing was written |
| 64 | bad command line (`"error":"usage"`) |
| 125 | `run` could not build the program, or could not place or start it |

`run` otherwise exits with the program's own code, or 128 + the signal
number if a signal killed it, so any value is possible there.

An Ovid program that the kernel refuses memory, for its heap's first region
at startup or for a later `Alloc`, writes `out of memory` to stderr and
exits 71. `run` passes that through, and `test` reports the test with
`"error":"out_of_memory"` and no `returned_by`.

## What `run` and `test` need

Both compile the program and execute it. They write it to a temporary
directory (`TMPDIR`, else `/tmp`) and run it from there. On Linux x86-64,
where that directory is missing or mounted `noexec`, they hold the program
in memory instead and execute it through `/proc/self/fd`, so `/proc` must be
mounted for that. `test` also keeps each test's output in the temporary
directory, so it needs a writable one even then.

When neither works, the request fails once with `"error":"run"` and a
`hint` naming `TMPDIR`: `run` exits 125, and `test` exits 1 without
reporting any test. Tracing a crash to its statement needs ptrace; without
it the program still runs, and a crash is reported without `at` and `stack`.

## Failures

A failed request ends with

```json
{"ok":false,"error":CODE,"message":TEXT,"hint":TEXT}
```

`hint` is optional and says what to do next. Some codes add fields, listed
below. Codes:

| code | when | extra fields |
|---|---|---|
| `usage` | bad flags or arguments (exit 64) | |
| `load` | no `ovid.mod` above the directory, or a module or std dir unreadable | |
| `read` | an edit file or a module file could not be read | |
| `write` | writing failed | edit: `written_files`, the files already renamed into place |
| `syntax` | the module does not parse; refs, rename, and move need a parsed module | edit: `op`, `file`, `line`, `col` of the op whose text broke it |
| `check` | an edit would add check errors (or leave any, with `--require-clean`); nothing was written | `errors`, `errors_before`; the new diagnostics precede it |
| `stale` | exit 2. An `expect` hash no longer matches, or the module `revision` moved, or a file changed on disk while the edit ran | `id`, `hash`, `text` (the current source), `decl`, `decl_hash`; `revision` for a revision guard |
| `expect_required` | an edit names a `st:`/`ex:` id without `expect` (pass `--force` to skip) | `op` |
| `not_found` | an id or name resolves to nothing | |
| `ambiguous` | a name resolves to several decls | the hint lists the full ids |
| `bad_edit` | a malformed op | `op` |
| `overlap` | two ops of one edit touch the same source | |
| `std` | the target is in a shipped package | |
| `bad_name` | a rename target or package path is not an identifier | |
| `conflict` | a rename or move would collide with an existing name | |
| `bad_move` | a move to the same package, of `main`, or into a file outside the package dir | |
| `unsupported` | the command does not work on this kind of node | |
| `rolled_back` | one move of several failed and every file was put back | `moved_before_failure` |
| `restore` | putting files back after a failed move failed; the module may be half-moved | |
| `bad_pattern` | grep's regexp does not compile | |
| `compile`, `run`, `dump` | the backend, the launch (the program could not be placed or started; see above), or the dump failed | |
| `init`, `exists` | init could not write, or `ovid.mod` already exists | |

## Diagnostics

`check`, `build`, `run`, `test`, and a refused edit print each error as

```json
{"fact":"error","code":CODE,"message":TEXT,"id":ID,"file":PATH,"line":N,"col":N,
 "end_line":N,"end_col":N,"source":LINE,"expected":TEXT,"got":TEXT,"hint":TEXT}
```

Only `fact`, `code`, and `message` are always present. Lines and columns are
1-based; columns count bytes. After 40 errors a
`{"fact":"truncated","more":N}` line replaces the rest.

Codes from parsing and loading: `syntax`, `layout` (a file's package clause
does not match its directory, or a file sits in the module root). From the checker: `type_mismatch`,
`unknown_name`, `unknown_field`, `unknown_package`, `missing_import`,
`missing_return`, `missing_expr`, `arity`, `bad_type`, `bad_op`,
`struct_value`, `duplicate_name`, `duplicate_id`, `duplicate_package`,
`import_self`, `syscall_forbidden`, `bad_main`, `bad_abi`, `no_entry`,
`bad_module`.

## Ids and hashes

`ovid help ids` gives the id forms. Decl ids (`fn:`, `ty:`, `cn:`, `fld:`,
`pa:`, `pkg:`, `im:`) are names and survive edits elsewhere. `st:` and `ex:`
ids are positions, counted per function, and are renumbered by any insert or
delete above them; an edit to one must carry `expect`. `show` of a
statement lists the `ex:` ids inside it with their hashes (`--exprs` does it
for a whole decl), so one expression can be replaced on its own.

A hash is 12 hex digits. A decl's hash covers its text; a statement's or
expression's also covers its kind, its decl, and which occurrence of that
text it is, so two identical statements hash differently. `revision`
(16 hex digits) covers everything a build reads: `ovid.mod`, every file of
the module, and the files of the shipped packages the module imports, so a
new toolchain with a changed standard library moves it too. Both are the
leading digits of a sha256; the tools keep the whole digest internally.

## Receipts

A successful edit, rename, or move ends with

```json
{"ok":true,"written":BOOL,"files":[PATH...],"check_ok":BOOL,"errors":N,
 "errors_before":N,"revision":REV, ...}
```

`written` is false for `--dry-run`. `errors` counts the errors the module has
afterwards (nonzero only with `--allow-broken`). Edit adds
`"ops":[{"ids":[ID...],"decls":[{"id":ID,"hash":H}]}]`, one per op: the ids
it wrote and the new hashes of the decls it touched, so a follow-up edit can
`expect` them without reading again. Rename adds `from`, `id`, `to`, `refs`,
`edits`; move adds `from`, `to` (the new id), `file`, `refs`, `edits`.

## Concurrency

Every command that writes takes an exclusive lock on `ovid.mod` (flock, on
Unix) for the whole read-plan-check-write, so writers are serialized. Files
are written to temp files, fsynced, and renamed into place. A write still
starts from what the command read: an agent that read a node before another
agent changed it is caught by `expect` (exit 2), not by the lock.
