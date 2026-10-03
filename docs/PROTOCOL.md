# Ovid command and program contract

This document describes the current development CLI. The Go bootstrap and the
self-hosted CLI share the ordinary query, patch, init, check-summary, and build
contracts. Conformance tests exercise both; verbose `check --facts` output is
implementation-specific. There is not yet a negotiated wire/schema version.

## Receipts, revisions, and errors

Commands write JSON to stdout. Most commands return one object; `check` and an
unsuccessful `build` can return newline-delimited diagnostic objects. Exit codes:

| Code | Meaning |
| --- | --- |
| 0 | Successful command; for check, no detected errors |
| 1 | Invalid input, check failure, I/O failure, or unsupported usage |
| 2 | Patch base revision no longer matches the current file |

A command error has `ok: false`, an `error` category, and usually `detail`.
Examples include `usage`, `read`, `parse`, `revision`, `revision_mismatch`,
`bad_program`, `bad_patch`, `exists`, `lock`, and `write`. Diagnostic detail text
is explanatory; branch on the category/code, not its prose. Low-level JSON
parse-error wording can differ between implementations.

The revision is an exact-byte SHA-256 snapshot. The canonical JSON contains
`"revision": "` followed by exactly 64 lowercase hex digits and a closing quote.
Replace those digits with zeros before hashing. Whitespace and property ordering
contribute to the hash. `query` and `patch` reject a mismatching stored hash.
`init`, `patch`, and the bootstrap frontend stamp files for the caller.

Go and Ovid may serialize the same edited program differently, so a semantically
identical patch can produce different revisions in different workspaces. A
receipt describes the bytes committed in that workspace, not a universal
semantic hash.

## Initialize

`ovid init DIR` creates directories as needed and a minimal program whose entry
is `app.main(io *ovid/io.Cap) i64`, returning zero. It includes the required
`ovid/io.Cap` declaration. No source text, projection, package directories, or
runtime library installation is required for this starter.

```json
{"ok":true,"revision":"<64 hex>","entry":"app"}
```

Any existing `ovid.json` path, including a dangling symlink, causes `exists` and
exit 1. Multiple initializers use the same permanent `.ovid.lock` as patches;
only one can create a module.

## Query

`ovid query DIR` lists package, type, constant, and function summaries. Filters
are exact matches and combine with AND:

| Flag | Selection or representation |
| --- | --- |
| `--pkg PATH` | Objects belonging to that package; default discovery stays compact |
| `--name NAME` | Also searches nested objects with that declared/reference name |
| `--kind KIND` | `package`, `import`, `type`, `field`, `const`, `func`, `param`, `stmt`, `expr` |
| `--id ID` | Exact identity; includes the complete node |
| `--calls-to ID` | Direct call expressions referring to the function identified by ID |
| `--full` | Include raw node payloads for selected matches |
| `--limit N` | Maximum matches returned; default 50; 0 means all |
| `--offset N` | Skip N selected matches; default 0 |

Limit and offset accept decimal integers from 0 through 2147483647. Discovery
filters do not search source substrings. `--calls-to` resolves the target's
current package/name, including local calls with an omitted `pkg`. A missing
target returns no matches. This is not a complete data-flow/reference graph.

```json
{
  "revision": "<64 hex>",
  "matches": [
    {"id":"fn:opaque","kind":"func","pkg":"demo","name":"Compute"}
  ],
  "total": 12,
  "offset": 0,
  "limit": 1,
  "returned": 1,
  "hasMore": true,
  "nextOffset": 1
}
```

`nextOffset` is absent on the final page. Empty results use `matches: []`.
Nested matches include `func` and `funcId`; expressions/statements also include
`op`. Fields use their owning type's name in `func` for compatibility, and have
no `funcId`. Empty optional metadata fields may be omitted.

Pagination follows document order. If a later page has a different revision,
restart discovery before constructing a patch. Paging limits output, not the
memory required to parse the whole module. `--full` can still expand overlapping
subtrees; prefer exact IDs for inspection.

## Program objects

The root is `{revision, module, entry, packages}`. `module` is a nonempty name,
`entry` is a package path, and `packages` is an array of packages. Every object
below the root has a unique nonempty string `id`.

Treat an ID as opaque. Existing descriptive prefixes are not a lookup formula.
Use query output to find identities, retain them for existing objects, and mint
fresh collision-resistant IDs for additions. The root itself is not currently
an addressable patch target; changing `entry`, `module`, or the root package
list requires the bootstrap authoring/import path.

| Object | Additional properties |
| --- | --- |
| Package | `path`; optional arrays `imports`, `types`, `consts`, `funcs` |
| Import | `path` |
| Type | `name`, `fields` array |
| Field | `name`, `type` |
| Constant | `name`, `type: "i64"`, signed integer `value` |
| Function | `name`, `result`; optional `params` and `body` arrays |
| Parameter | `name`, `type` |
| Statement/expression | `op` plus the properties below |

Use fully qualified pointer types in canonical JSON, such as `*demo.Record`.
The `.ov` frontend expands local type names. Canonical unqualified type names
are not yet handled identically by both compiler implementations.

Missing optional containers are preferred over explicit null. Null optional
containers are accepted as empty, but Go may omit them when writing while Ovid
preserves their original spelling. Null elements inside node lists are invalid.
JSON numbers used as values must be signed 64-bit integers. Duplicate keys and
trailing JSON input are rejected.

## Statement and expression shapes

Every entry below also includes `id`. Child expressions/statements are objects
with their own IDs; child lists contain such objects. This table describes
well-typed programs; patch can store drafts that `check` then diagnoses.

| `op` | Properties and types |
| --- | --- |
| `int` | `value`: signed 64-bit integer; produces `i64` |
| `bool` | `value`: boolean; produces `bool` |
| `strptr`, `strlen` | `value`: string; produces `i64` |
| `name` | `name`: local variable, parameter, or same-package constant |
| `add`, `sub`, `mul`, `div`, `mod`, `and`, `or`, `xor`, `shl`, `shr` | `left`, `right`: `i64` expressions; produces `i64` |
| `eq`, `ne` | `left`, `right`: expressions with matching types; produces `bool` |
| `lt`, `le`, `gt`, `ge` | `left`, `right`: `i64`; produces `bool` |
| `land`, `lor` | `left`, `right`: `bool`; short-circuits |
| `not` | `arg`: `bool` |
| `neg`, `bnot` | `arg`: `i64` |
| `cast` | `arg`, `type`: explicit target type |
| `field` | `base`: pointer expression, `name`: field name |
| `call` | `func`: current function name, optional `pkg`, `args` array |
| `syscall` | Seven `i64` expressions in `args`; allowed only in `ovid/io` |
| `load8`, `load32`, `load64` | `arg`: `i64` address; produces `i64` |
| `var` | `name`, `type`, optional initializer `val` |
| `assign` | `name`, `val` |
| `setfield` | `base`, `name`, `val` |
| `store8`, `store64` | `addr`, `val`: `i64` expressions |
| `expr` | `val`: expression evaluated for its effects |
| `return` | `val`: expression of the enclosing function's result type |
| `if` | `cond`: `bool`; statement arrays `then`, optional `else` |
| `while` | `cond`: `bool`; statement array `body` |

The `ovid/io.Cap` type has exactly these fields, in this order, all `i64`:
`argc`, `argv`, `heap`, `used`, `size`. The entry package has a function named
`main` with one `*ovid/io.Cap` parameter and an `i64` result. `init` supplies this
ABI. Its minimal I/O package has no other functions; add needed library routines
through the existing package or use the bootstrap source importer.

## Patch transactions

`ovid patch DIR FILE` reads one request; FILE can be `-` for stdin:

```json
{
  "baseRevision":"<query revision>",
  "ops":[
    {"op":"replace","id":"ex:opaque","node":{"id":"ex:opaque","op":"int","value":42}}
  ]
}
```

Operations run in order in a private candidate program. Later operations see
previous replacements in the same request. A replacement must retain its target
ID and fit that target's object kind. All addressable kinds are supported. An
empty operation list is invalid. Only `replace` is currently supported.

Add a function by retrieving its package, adding the function to `funcs` with
fresh IDs throughout its new subtree, and replacing the package. Remove an item
by replacing its containing object with the shortened list. Existing siblings'
IDs must remain intact. For rename, query all direct calls and replace the
function name and call nodes in one request; preserve their IDs. Do not assume
changing an ID-shaped name automatically updates references.

The tool reads patch input before taking the lock, then holds `.ovid.lock` over
reading the canonical snapshot, checking revision/structure, applying every
operation, validating resulting identities, and publishing the complete file.
A same-directory temporary is written and synced, renamed over `ovid.json`, and
the directory is synced. Go and self-hosted writers use the same Linux `flock`.

Success: `{"ok":true,"revision":"<committed revision>"}`.

Stale revision (exit 2):

```json
{"ok":false,"error":"stale_patch","revision":"<current>","baseRevision":"<requested>","currentRevision":"<current>"}
```

Structural failure (exit 1): `ok:false`, `error:"bad_patch"`, with a diagnostic
`detail`, for example `not_found`, `id_mismatch`, `missing_id`, `duplicate_id`,
or `invalid_node`. The on-disk program is unchanged for rejected requests,
including a failure after earlier operations succeeded in the candidate.

Type errors do not prevent committing a structurally valid draft. `check` or
`build` determines whether it is currently compilable. The lock covers the whole
module; disjoint edits based on the same revision still conflict. On stale
status, query a fresh snapshot and reconsider the requested edit before retrying.

The lock coordinates participating tools; external writes bypass it. As with
filesystem transactions generally, a storage failure after rename can make the
reported outcome or durability uncertain. After an I/O failure, query again
before retrying rather than assuming nothing was committed.

## Check and build

Default `check` produces errors followed by a summary. A type mismatch points
to the expression an agent can inspect and replace:

```json
{"fact":"error","id":"ex:opaque","pkg":"demo","func":"main","code":"type_mismatch","detail":"expected i64, got bool","expected":"i64","actual":"bool"}
{"fact":"summary","ok":false,"errors":1,"packages":2,"funcs":2,"revision":"<checked revision>"}
```

A valid program produces only its `ok:true` summary. Parse/read failures can
instead produce a command-error envelope. `check DIR --facts` includes additional
module/declaration facts; those inventories are currently richer in Go than in
the self-hosted tool and should not be treated as interchangeable schemas.

`build` validates first. Check failures produce error facts and exit 1; success
produces one `{"ok":true,"output":"PATH","bytes":N}` object and a static ELF.
The executable's own exit code is independent of the build command's exit code.

## Bootstrap source import

`bootstrap/front` records the generated revision in `BOOTSTRAP.json` and uses
the same writer lock. A later import requires that the current canonical file
still matches that provenance; canonical agent patches leave provenance behind
intentionally. `-replace` explicitly permits discarding canonical edits.

Reconciliation preserves matching declarations and unambiguous AST identities,
including common insertions, reorderings, and literal edits. Ambiguous edits
receive new opaque IDs. Direct canonical patches are the reliable way to retain
identity through arbitrary refactors. The frontend-generated `PROJECTION`
contains its revision but is not refreshed by patch.
