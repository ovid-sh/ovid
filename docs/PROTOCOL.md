# The ovid command protocol

What a program driving `ovid` can rely on. `ovid help commands` lists each
command's fields; this file is the contract they share. It describes the Go
toolchain (`cmd/ovid`); the self-hosted compiler in `prog/` implements only
`check`, `build`, `dump`, and `refs` (text only, no paging).

## Output

Every command writes JSON lines to stdout, one object per line, and
nothing else, except `help`, `run` without `--json`, `dump` without `-o`
(one JSON document, described below), and the four read commands without
`--json`. `outline`, `show`, `refs`, and `grep` print
source and listings as text, since an agent reads them in place of `cat`
and `grep`, sized like what those print (within a fifth, measured on
`prog/`); their failures are JSON like any other, and `outline`, `refs`,
and `grep` still end with the JSON line below, while a successful `show`
prints a header line and the source and nothing after it.

For JSON output the **last line** always has a boolean `"ok"`; earlier
lines are records (a diagnostic, a match, a test result, a decl). Key
order is not significant. A consumer should read every line, take the
last as the result, and ignore keys it does not know: new keys are added
without notice, existing ones keep their meaning (the one exception so far
is `refs`'s `count`; see Paging). For the text listings, take the last
line the same way and treat the lines before it as text.

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

`dump` writes the program tree as one JSON object. A string literal's
bytes (a `str` node, or a `strptr`/`strlen`) are its `"value"`, or, when they are not valid UTF-8 (a
`\x` escape can make any byte), `"value_hex"`: two lowercase hex digits per
byte. Any other JSON string, such as a diagnostic's `source`, has each
byte that is not UTF-8 replaced by U+FFFD.

### `run --json`

With `--json`, `run` captures the program's output instead of passing it
through, and ends with one record on stdout:

```json
{"ok":true,"exit":3,"ms":12,"stdout":TEXT,"stderr":TEXT}
```

`ok` says the program was built and started, whatever became of it, and
ovid then exits 0: the record, not the exit code, says how the program
ended. A program killed by a signal has `"signal"` in place of `"exit"`,
with `at`, `stack`, `fault_addr`, and `hint` as above. One ended by
`--timeout <duration>` (`5s`, `500ms`; no limit without it) has
`"signal":"timeout"`.

Each of `stdout` and `stderr` keeps its first 65,536 bytes, or
`--max-output <bytes>`. Past that the record has `"truncated":true` and
`"stdout_bytes"` or `"stderr_bytes"`, the full size; the rest is discarded
as it is written. Bytes that are not UTF-8 are replaced, so the line is
always valid JSON.

On Linux x86-64 the program is **confined** unless `--no-confine` is given
(the same for `test`): it runs under a seccomp filter that allows exactly
the system calls its build receipt lists (`syscalls`) and kills it on any
other, reported as `"signal":"bad system call"` with a hint; and, where the
kernel has Landlock, under a file system it can read everywhere and write
only in one fresh directory, its working directory. The record adds
`"confined"`, the list of what was applied (`["seccomp"]`, or
`["seccomp","landlock"]`), and `"writable"`, that directory, when the
program left anything in it; an empty one is removed. Where no temporary
directory can be made, the program runs with nowhere to write if the kernel
has Landlock; without it there is no way to make the file system read-only,
and the request fails (`"error":"run"`) rather than run the program with
the caller's write rights: set `TMPDIR`, or pass `--no-confine`. `test`'s
summary carries the same fields. Reads are not restricted: a confined
program can still read any file the caller can. A program that must write
elsewhere, by an absolute path, needs `--no-confine`.
If the confinement itself cannot be set up (the kernel refuses a rule or
the filter), the program is not started and the request fails with
`"error":"run"` and the reason; `confined` is never claimed for a program
that did not run under it.

A module that does not build ends as without `--json`: the diagnostics,
then `{"ok":false,"errors":N}`, exit 125. So does a program that could not
be placed or started: `{"ok":false,"error":"run",...}`, exit 125.

Without `--json`, `--timeout` still ends the program: `run` writes
`{"ok":false,"error":"killed","signal":"timeout","exit":124,...}` to stderr
and exits 124.

## Exit codes

| code | meaning |
|---|---|
| 0 | ok |
| 1 | the program has errors, or the request failed (see `error`) |
| 2 | stale: an edit's `expect` hash or `revision` no longer matches; nothing was written |
| 64 | bad command line (`"error":"usage"`) |
| 124 | `run --timeout` ended the program (without `--json`) |
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
mounted for that. Neither needs the directory for anything else: a test's
output comes back through a pipe.

A program run by `run` or `test` is given its arguments and its standard
streams, and an **empty environment**. Ovid has no way to ask for an
environment variable, but the kernel puts the environment on the stack right
after `argv`, where a program can read it by address, and a caller's
environment is where its secrets usually are. ovid itself still reads its
own (`TMPDIR`, `OVID_PATHS`, `OVID_LOCK_TIMEOUT`).

A program can write files, the files of its own module among them. When
the module on disk is no longer the one ovid loaded, the last line says so:

```json
{"module_changed":true,"changed_files":["demo/planted.ov"],
 "revision_before":REV,"revision_after":REV,"hint":TEXT}
```

`changed_files` are the source files, and `ovid.mod`, that changed, went
away, or appeared; `revision_after` is missing when the module no longer
loads (its `ovid.mod` gone, for one). If the module's directory cannot be
read through, the module counts as changed, `"scan_error"` says why, and
`changed_files` holds only what was found before that. For `test` the summary is then `"ok":false` and the exit code 1 even
if every test passed: what passed is not what is on disk. For `run --json`
the fields are added and `ok` keeps its meaning (the program ran). An edit
by someone else while the program ran is reported the same way. Files
written anywhere else are not looked at.

`test` keeps the first 4,000 bytes of a test's output in its record. Longer
output ends in `...(truncated)` and the record adds `"output_bytes"`, the
size of all of it. The rest is discarded as it is written, so a test that
prints without end costs no memory or disk before its timeout ends it.

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
| `load` | no `ovid.mod` above the directory, a module unreadable, or a bad `ovid.mod` (a `std` line is no longer accepted) | |
| `read` | an edit file or a module file could not be read | |
| `lock_timeout` | another command held the module lock (see Concurrency) for the whole wait; nothing was written | `file` (the locked `ovid.mod`), `waited_ms` |
| `write` | writing failed | edit: `written_files`, the files already renamed into place |
| `syntax` | the module does not parse; refs, rename, and move need a parsed module | edit: `op`, `file`, `line`, `col` of the op whose text broke it |
| `check` | an edit would add check errors (or leave any, with `--require-clean`); nothing was written | `errors`, `errors_before`; the new diagnostics precede it |
| `stale` | exit 2. An `expect` hash no longer matches, or the module `revision` moved, or a file changed on disk while the edit ran (or one it would create appeared) | `id`, `hash`, `text` (the current source), `decl`, `decl_hash`; `revision` for a revision guard; `id`, `copies` (as for `ambiguous_id`) when no copy of a duplicated id has the hash |
| `expect_required` | an edit op has no guard: no `expect`, no request `revision` (`--rev`), no `--force`. Every op needs one, decl ids included; only `append` into a package path does not | `op` |
| `not_found` | an id or name resolves to nothing | |
| `ambiguous` | a name resolves to several decls | the hint lists the full ids |
| `ambiguous_id` | an id names several nodes (see Duplicated ids) and the edit op has no `expect` to pick one, or the command (refs, rename, move) cannot pick one | `id`, `copies`: `[{file,line,end_line,hash,decl_hash}]` (`decl_hash` for a `st:`/`ex:` id); edit: `op` |
| `bad_edit` | a malformed op, or a request or op with a key it does not have (misspelled, or in another case), with the same key twice, or with a field that belongs to another op (a replace with a `before`, a delete with a `text`); nothing was written | `op`, when one op is at fault |
| `overlap` | two ops of one edit touch the same source | |
| `std` | the target is in a shipped package | |
| `bad_name` | a rename target or package path is not an identifier | |
| `conflict` | a rename or move would collide with an existing name | |
| `bad_move` | a move to the same package, of `main`, or into a file outside the package dir | |
| `unsupported` | the command does not work on this kind of node | |
| `rolled_back` | one move of several failed; nothing was written | `moved_before_failure` |
| `restore` | no longer produced: a move of several decls is planned in memory and written once | |
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
does not match its directory, or a file sits in the module root),
`reserved_path` (the module has a package with the path of one the toolchain
ships, such as `ovid/io`). From the checker: `type_mismatch`,
`unknown_name`, `unknown_field`, `unknown_package`, `missing_import`, `unused_result`,
`missing_return`, `missing_expr`, `arity`, `bad_type`, `bad_op`,
`struct_value`, `duplicate_name`, `duplicate_id`, `duplicate_package`,
`import_self`, `import_cycle`, `syscall_forbidden`, `opaque_type`, `bad_main`,
`bad_handler` (an entry package with no main has a handle of the wrong
signature), `bad_abi`, `no_entry`, `bad_module`.

`import_cycle` is reported in the package whose path sorts first among a
cycle's, at its first import (in source order) that leads back to it, and
once per such package however many cycles pass through it; the message
spells one cycle (`import cycle: app/a -> app/b -> app/a`). Packages must
form a DAG so that each can be checked and compiled once its imports are,
which keeps per-package parallel and incremental builds possible.

`opaque_type`: outside `ovid/io`, a pointer to one of its types (`Cap` today)
was made by a cast, cast to something else, or had a field read or written.
Those types are handles on what the program may do; they come from `main` or
from an `ovid/io` func, and only `ovid/io` looks inside them.

`syscall_forbidden` is decided by where a package came from, not only by its
name: `syscall` is valid in `ovid/io` as the toolchain ships it. A module
cannot supply that package (`reserved_path`), and `ovid.mod` cannot name
another standard library, so the system calls a checked program can make are
those of the shipped `ovid/io`.

## What a program can ask of the kernel

`build` ends with

```json
{"ok":true,"output":PATH,"bytes":N,"syscalls":[1,9,60]}
```

`syscalls` are the numbers (Linux x86-64) of the system calls the program
can make, in increasing order: every `syscall` in a func reachable from
`main`, and the three the startup code makes (`mmap` for the heap, `exit`,
and the `write` that says `out of memory`). It is what the program can
reach, not what a given run makes, so a filter that allows exactly these
(and the `execve` that starts the program) never stops it, and a program
that never calls `ovid/io.WriteFile` does not list `rename`.

The list is exact because only the `ovid/io` the toolchain ships may call
`syscall` (see `syscall_forbidden`), and its numbers are constants. Were one
not a constant, the receipt would add `"syscalls_unknown":N`, the count of
such calls, and the list would be incomplete. Both compilers report the
same list for the same source.

## Memory

What a command needs grows with the module. Measured on Linux x86-64 at
dc1f0aa plus the changes of #105, peak resident memory on a generated
module of 40,000 lines, and on `prog/` (7,600 lines):

| command | 40,000 lines | `prog/` | about |
|---|---|---|---|
| `check`, `build`, `test` | 62 MB | 27 MB | 25 MB + 0.9 KB a line |
| `outline` | 51 MB | 23 MB | `check` less the typecheck, plus the id index |
| `show`, `refs` | 98 to 118 MB | 39 to 44 MB | `check` plus the id index and every expression's type |
| `dump` | 66 MB | 29 MB | `check` + 4 MB |
| `edit`, `rename`, `move` | 155 to 189 MB | 52 to 54 MB | two to three times `check`: the module is loaded again with the change; `move` of several names costs the same as one |

A sandbox of 96 MiB runs every command on a module of `prog/`'s size; 72
MiB runs `check`, `build`, and `test`. The self-hosted compiler checks the
same module in 5.5 MB. #105 tracks what is left: the tree's own size.

## Paging

`outline`, `refs`, and `grep` print one page of their records: at most 200
unless `--limit N` says otherwise (`--limit 0` is all of them), starting
after the first `--offset N`. Their last line carries

```json
{"ok":true,"count":N,"total":N,"offset":N,"has_more":BOOL,"next_offset":N,"revision":REV}
```

`count` is the number of records printed and `total` the number there are.
`next_offset` is present when `has_more` is true: pass it as `--offset` for
the next page. Pages follow one another without overlap, so following
`next_offset` until `has_more` is false visits every record once, in the
order of an unpaged run. `revision` is the module's: if it differs between
two pages, the module changed in between and the offsets no longer line up.

`refs` also gives `files`, `by_pkg`, and `external` in its last line; these
describe every use, on the page or not.

One key changed its meaning when `refs` became paged, the exception to the
rule under Output: `refs`'s `count` used to be the number of uses and is now
the number printed. A consumer that wants the number of uses reads `total`.
The two are equal only on a first page that holds everything (`offset` 0 and
`has_more` false), so read `total`, whatever the page.

`dump` is a single JSON document and is not paged. `--pkg P` limits it to
one package. `-o <file>` writes it to that file (written in place, so it
may be a device) and prints `{"ok":true,"output":PATH,"bytes":N,
"revision":REV}` instead of the document.

## Paths

A `file` in a record, and each entry of a receipt's `files`, is by default
relative to the working directory ovid was started in, so that it can be
opened as it stands. It is absolute when the file lies more than one
directory above that.

With `OVID_PATHS=module` in the environment, every such path is relative to
the module root instead, with forward slashes, and a file of a shipped
package reads `std:<package>/<file>`. The same module at the same revision
then reports the same paths wherever it is and wherever ovid runs, which is
what comparing or replaying records between two copies of a module needs (a
sandbox's and its host's). `OVID_PATHS=cwd` is the default; any other value
is a usage error.

## Ids and hashes

`ovid help ids` gives the id forms. Decl ids (`fn:`, `ty:`, `cn:`, `fld:`,
`pa:`, `pkg:`, `im:`) are names and survive edits elsewhere. `st:` and `ex:`
ids are positions, counted per function, and are renumbered by any insert or
delete above them. `show` of a
statement lists the `ex:` ids inside it with their hashes (`--exprs` does it
for a whole decl), so one expression can be replaced on its own.

A hash is 12 hex digits. A decl's hash covers its text, and the text of a
func, type, or const begins at its **doc comment**: the unbroken run of
`//` lines directly above it (a blank line ends the run, so a comment
separated from the decl by one belongs to nothing). It is what `show`
prints (with `doc_line` and `doc` in `--json`; `line` stays the decl's own
first line), what `replace` swaps (text that opens with a `//` comment
puts it in the old one's place; text with none keeps the old one, so an
agent fixing a body does not lose its doc), and what `delete` removes;
`insert --before` lands above it. A statement's or
expression's covers its kind, its text, which occurrence of that text in
its decl it is (so two identical statements hash differently), and the
decl's id and **whole text**. A positional hash is therefore bound to the
decl as it was read: any change to that decl, even one far from the node,
changes every statement and expression hash in it, so an edit guarded by
one read before the change is stale (exit 2) instead of landing on whatever
now has the id, such as the twin of a deleted statement. A change to
another decl changes none of them, so agents editing different functions
do not disturb each other. An edit to a `st:`/`ex:` id may `expect` its own
hash or its decl's; the two are equally strict. `revision`
(16 hex digits) covers everything a build reads: `ovid.mod`, every file of
the module, and the files of the shipped packages the module imports, so a
new toolchain with a changed standard library moves it too. Both are the
leading digits of a sha256; the tools keep the whole digest internally.

### Duplicated ids

A module that declares a name twice in one package (`func F` in both
`a.ov` and `b.ov`) has two nodes with one id, and so do their params,
statements, and expressions; `check` reports the duplicate. Every copy is
indexed: `outline` lists each at its own `file` and `line` with its own
hash and `"id_copies":N`, and `show` and `grep` find each where it is.
A name reaches every copy as the id does, `Func.param` and `Type.field`
included. The checker checks only the first copy, so `show` gives a
`type` (JSON) or `type=` (text) only for the first copy's nodes, never
another copy's. An
edit addresses one copy by giving its hash as `expect` (for a `st:`/`ex:`
id, the node's hash or its decl copy's; a param or field copy, only its
own hash, as for any decl id). Without an `expect`, the op fails
with `ambiguous_id`, which lists the copies; `--rev` and `--force` do not
choose one. An `expect` no copy has is `stale` (exit 2). Copies with the
same hash have the same text, so the op takes the first of them, the one
`outline` lists first. `refs`, `rename`, and `move` cannot choose a copy
and fail with `ambiguous_id`: delete one copy, or replace it under another
name, first.

## Guards

Every edit op (`edit`, `replace`, `insert`, `append`, `delete`) must carry
a guard, decl ids included:

- `expect`: the hash of the node the op names (its id, insert's `before`/
  `after`, append's `into`), from `outline`, `show`, or a receipt; for a
  `st:`/`ex:` node its decl's hash works too;
- or the request's `revision` (`--rev` on the command line), which
  covers the whole module;
- or `--force`, which skips every guard.

Without one the op fails with `expect_required` and nothing is written; a
guard that no longer matches fails with `stale` (exit 2). The exception is
`append` into a package path: it names no node and overwrites no code, and
replaying it is refused by the checker as a `duplicate_name`, so it needs no
guard; an `expect` on it is `bad_edit`. `rename` and `move` take no guard
for the same reason: they carry no code, are planned from the module as it
is under the lock, and a replay is refused (the old name is gone, or the decl
is already in that package).

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
`expect` them without reading again. `decls` lists, in source order, the
func, type, or const the op wrote into, or every one its text holds (an
`append` or `insert` of several decls, or a decl replaced by several),
each with its `text` under `--show`; a `delete` lists the decl it was in (none for a decl deleted whole, not the neighbour now at its place). Rename adds `from`, `id`, `to`, `refs`,
`edits`, and `doc` (whether the first word of the decl's doc comment was
its name and was renamed with it); move adds `from`, `to` (the new id), `file`, `refs`, `edits`. A
move of several decls prints one such receipt per decl, in order, then
`{"ok":true,"moved":[NAME...],"to":PKG,"written":BOOL}`. Each move is
planned in memory over the ones before it, and only when every one has
passed are the files written, together, as one edit's are; `--dry-run`
touches no file.
`refs` counts the uses the checker resolved to the declaration, the same
ones `ovid refs` lists; rename rewrites their name tokens, the declaration's own, and the
first word of its doc comment when that is the name (`doc` in the receipt),
and `edits` counts all of them; a field, local, or declaration of the same
spelling in another namespace is never touched.

## Concurrency

Every command that writes takes an exclusive lock on `ovid.mod` (flock, on
Unix) for the whole read-plan-check-write, so writers are serialized. A
writer that finds the lock held prints one
`{"fact":"waiting","for":"lock","file":PATH,"timeout_ms":N,"message":TEXT}`
line and keeps trying for up to 10 s (`OVID_LOCK_TIMEOUT`, a Go duration
such as `30s`, `500ms`, or `0` for no wait, changes it; a value that does
not parse is a `usage` error), then fails with `lock_timeout`. Files
are written to temp files, fsynced, and renamed into place. A write still
starts from what the command read: an agent that read a node before another
agent changed it is caught by `expect` (exit 2), not by the lock.
