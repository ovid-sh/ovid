# Agent usage feedback — 2026-10-03

This round used three delegated agents with separate tasks: discover and inspect
code through the CLI, complete a repair and refactor workflow, and reproduce
concurrent edits. They ran the Go bootstrap CLI and freshly compiled Ovid CLI,
reported failures, and then exercised the changed commands. These are small
engineering exercises, not a controlled model benchmark or a claim that the
language is already fully agent native.

## Discovery without consuming the context window

The discovery agent's task was to find the routines that write files, inspect
`WriteFile`, and identify its callers.

On the original checked-in `prog`, `query --pkg ovid/io` returned 427 matches and
296,511 bytes through the Go CLI. Package, function, statement, and expression
payloads repeated the same subtrees. Finding the 17 functions required filtering
that output. An exact `--id fn:ovid/io.WriteFile` then returned one useful payload
of 5,828 bytes.

With the revised CLI, the same package query against the same original program
returns 32 declaration summaries in 3,910 bytes through Go, a 98.7% reduction.
The self-hosted CLI returns the same parsed result in 2,534 bytes; whitespace
accounts for the byte difference. Exact ID lookup still returns a complete node.

A separate usage agent measured `query --pkg ovid/cli` before the changes:
2,432 matches, 2,316,502 bytes through Go and 2,617,904 bytes through Ovid. The new
command against that original program returns 27 summaries: 3,333 bytes through
Go and 2,167 bytes through Ovid.

The implemented contract separates discovery from expansion:

- The default and `--pkg` selection list packages, types, constants, and functions.
- `--id` returns a node payload; `--full` explicitly expands selected results.
- `--name`, `--kind`, and `--calls-to` also search nested nodes.
- Pages default to 50 matches. `--limit 0` explicitly requests all matches.
- Results include `total`, `offset`, `limit`, `returned`, `hasMore`, and, when
  another page exists, `nextOffset`.
- Nested matches expose their enclosing `funcId` and `func`, plus their `op`.

The practical improvement is that an agent can discover a small set of IDs,
retrieve one definition, and ask for its call sites without reading the whole
package. `--calls-to` resolves the supplied function ID to its package and name;
it finds both local and qualified direct calls.

## Repairing a program and renaming a function

The workflow agent began with a function declared to return `i64` whose body
returned a boolean. It completed `check → query → patch → check → build → run`,
replacing the boolean with `42`; both runtimes produced a program that exited 42.

Creating that fixture originally required discovering the five exact `ovid/io.Cap`
ABI fields and supplying `PROJECTION` and package marker directories. Those files
were hidden prerequisites unrelated to the intended repair. `init <dir>` now
creates a minimal `app.main` and the required ABI in `ovid.json`. It creates the
module lock as well, but requires no projection or package markers. Both
initializers produce identical canonical bytes and revision, and both starter
programs check, build, and exit zero.

The agent next renamed a function. Replacing only the definition left its caller
broken. In the revised workflow it queried `--calls-to`, retrieved the call-site
node, and submitted the definition and call-site replacements in one patch. The
function kept its ID, and the executable still exited 42. This demonstrates a
workable explicit refactor; it is not an automatic semantic rename operation.

Package replacement to add a function originally failed in Go with `not_found`
and succeeded in Ovid. The conformance workflow now verifies both package and
constant replacements in both implementations.

Successful self-hosted builds originally emitted ten JSON lines and 659 bytes
for the small fixture, including checker facts. The corresponding Go build
emitted one line. Both now emit one success object. Default `check` now emits
only diagnostics and its summary: for one freshly compiled 276-function module,
both CLIs emitted a single 143-byte success summary containing the revision.
`check --facts` remains available for deeper inspection.

## Source reimport and identity

The workflow agent also added and ran seven source-identity regression tests.
Within matched parents, declarations retain identity by their path or name;
unique unchanged function bodies can identify a rename. Unique subtree anchors
retain IDs through insertion and reordering. Tests also preserve a statement and
its expression through a scalar edit or a boolean-to-integer repair in the same
role. Deletion followed by reinsertion does not reuse the deleted parser ID.

Ambiguous changed lists receive new IDs, generated as a kind prefix plus 128
random bits and checked against prior IDs. Unchanged identical lists retain
positional identities. This is deliberately best-effort source reconciliation:
a simultaneous rename and body edit, including a recursive call renamed with its
function, is not inferred as the same function. Canonical patches address exact
IDs and do not depend on that inference.

The frontend also records its generated revision in `BOOTSTRAP.json`. An import
refuses to overwrite a canonical program changed since that recorded revision,
or an existing program without import provenance. During the adversarial rebuild,
an independently generated module without that metadata was correctly refused.
The frontend regression tests cover patching a canonical value to 42, rejecting
a later source import without changing its bytes, and permitting replacement
only when `-replace` explicitly requests it.

## Concurrent edits and recovery

The transaction agent reproduced a lost update before making changes: two
simultaneous Go patches changed different functions, both exited zero and reported
the same success revision, but the final program retained only one change.

Both implementations now acquire the same module lock before reading and
validating the revision, then replace the canonical file atomically. The CLI
conformance suite holds that lock externally while starting eight writers with
the same base revision. Go-only, Ovid-only, and mixed groups are exercised over
four rounds. Each group has exactly one successful writer; the rest report stale
revisions. Stale responses identify the supplied and current revisions so the
agent can query again and rebuild its patch.

A further mixed-runtime experiment used a roughly 2.72 MB compiler-program fixture.
Exactly one writer succeeded and the other returned stale status 2; both agreed
on the resulting revision. Deliberately changing canonical bytes without updating
the stored hash made both patch commands reject `revision_mismatch` without
writing. Query now rejects that mismatch too, so it does not offer an apparently
usable revision that the next patch would reject.

The Go initializer also has a twelve-writer concurrency test: exactly one
initializer succeeds. Existing canonical files and dangling symlinks are left
untouched.

## Adversarial inputs checked after implementation

Fresh Go and Ovid binaries were compared on twelve query scenarios each, using
names containing quotes, backslashes, ASCII control characters 0–31, Chinese
characters, and an emoji. Decoded results matched for discovery, exact ID lookup,
name lookup, nested expressions, empty results, unknown call targets, limits 0
and 50, and offsets up to 2,147,483,647. Embedded NUL names were retrieved by
ordinary IDs; operating-system arguments cannot contain a NUL byte.

Both CLIs reject negative, malformed, overflowing, and empty numeric query
arguments with a structured usage error. Both reject replacement nodes with a
numeric function name or a null body element, leaving the file unchanged.

This pass found and fixed two additional issues: initializing over a regular
file reported different error categories between runtimes, and the query number
formatter reserved 32 bytes although `FormatI64` also uses scratch space starting
at byte 32. The initializer now reports `mkdir` consistently for that case, and
the formatter reserves 64 bytes. The query context was also checked: its 26
pointer/integer fields match its 208-byte allocation on the supported target.

## Remaining limitations

- Source reimport preserves common edit patterns, but cannot establish identity
  across every arbitrary edit or ambiguous duplicate subtree. Reimport inference
  is weaker than directly retaining an ID through canonical patches.
- Rename is still a caller query plus explicit replacements. The tool does not
  yet provide one semantic rename transaction or a complete reference graph.
- Pagination bounds output, not parsing memory. Each query still parses the
  document; `total` requires counting all selected nodes. Explicit `--full` can
  return large, overlapping payloads. Pages should be consumed only while their
  revisions agree.
- The self-hosted runtime has a fixed 128 MiB bump heap. These tests do not prove
  graceful behavior for every oversized or deeply nested document.
- Optional null containers are accepted as empty. Go may normalize them away
  while Ovid preserves their source spelling. Whole-file bytes, resulting
  revisions, and raw optional fields after arbitrary patches need not match
  between implementations, even when their program meanings match.
- Verbose checker fact streams are not identical: on the 276-function fixture,
  Go `--facts` emitted 1,343 lines and Ovid emitted 317. Default success summaries
  matched; full semantic fact parity remains work to do.
- The lock covers the whole module and coordinates participating writers.
  Arbitrary external file writes bypass it. Storage failure after rename can
  leave the durability or reported outcome uncertain.
- Tests and contracts are not yet first-class program-model objects. The CLI
  has a written schema in [PROTOCOL.md](PROTOCOL.md), but still needs machine
  discovery of that schema and richer guidance for constructing new nodes.

## Repeating the checked workflows

```sh
go test ./internal/query ./cmd/ovid ./internal/tool
go test ./bootstrap/ov -run TestCLIConformance
```

The focused tests cover discovery, references, pagination, argument validation,
initializer concurrency, and revision mismatch. The conformance suite builds
both CLIs and exercises the repair, caller-guided rename, initialization,
structural validation, and competing-writer workflows. Temporary experiment
paths are intentionally not required to reproduce these checks.
