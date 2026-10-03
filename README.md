# Ovid

Ovid is a small self-hosting language and compiler built around an agent editing
loop: discover a program, inspect exact nodes, submit a revision-checked patch,
and verify the result. The canonical program is plain JSON (`ovid.json`).
The CLI produces structured JSON and builds one static Linux x86-64 executable.

Agent usage experiments and remaining limitations are recorded in
[docs/AGENT_FEEDBACK.md](docs/AGENT_FEEDBACK.md). The command and program contract
is described in [docs/PROTOCOL.md](docs/PROTOCOL.md).

## Build the tool

```sh
go build -o bin/ovid-boot ./cmd/ovid
bin/ovid-boot build prog -o bin/ovid
bin/ovid build prog -o bin/ovid2
bin/ovid2 check prog
```

Go builds the first compiler. The generated `ovid` compiles the CLI again without
invoking Go, linking libc, or requiring another tool on `PATH`.

## Start an agent workspace

```sh
bin/ovid init demo
bin/ovid query demo
bin/ovid query demo --name main --kind func
bin/ovid check demo
bin/ovid build demo -o bin/demo
```

`init` creates a valid entry function returning zero and the required runtime
ABI. It refuses to overwrite an existing `ovid.json`, including a symlink.
Only `ovid.json` is a program input; `PROJECTION` and package marker directories
are optional. `.ovid.lock` coordinates writers and must not be deleted while
tools are operating on the module.

## Discover, inspect, edit, verify

```text
ovid init <dir>
ovid query <dir> [--id ID] [--name NAME] [--pkg PATH] [--kind KIND]
                 [--calls-to FUNCTION_ID] [--full] [--limit N] [--offset N]
ovid patch <dir> <patch.json|->
ovid check <dir> [--facts]
ovid build <dir> -o <file>
```

Discovery returns compact declaration summaries, including when selecting a
package. It defaults to 50 matches per page. `--id` retrieves a full node;
`--full` explicitly includes payloads for other selections. `--name`, `--kind`,
and `--calls-to` also search nested nodes. `--limit 0` requests all matches.

Every query includes the module revision, total match count, page information,
and `nextOffset` if another page exists. Consume pages only while their revisions
agree. Nested matches identify their enclosing function with `funcId` and `func`.

To rename a function, query its ID, query `--calls-to ID --full`, and replace the
definition and all affected calls in one patch. IDs are opaque identities: retain
them when changing a name or body, and use fresh unique IDs for newly added nodes.
Never reconstruct an ID from a current name or source position.

`patch` accepts a file or standard input (`-`). Use the revision and target ID
returned by `query`; a replacement must retain the target object's ID:

```json
{
  "baseRevision": "<revision returned by query>",
  "ops": [
    {
      "op": "replace",
      "id": "<expression ID>",
      "node": {"id": "<expression ID>", "op": "int", "value": 42}
    }
  ]
}
```

All addressable object kinds can be replaced, including packages, types, fields,
constants, functions, parameters, statements, and expressions. To add or remove
an item, replace its containing node's list while retaining unaffected IDs.
There is currently no dedicated insert, delete, or automatic rename operation.

The complete patch holds a module lock and publishes the new file by atomic
rename. Invalid or stale patches do not modify the program. A stale patch exits
2 and returns `baseRevision`, `currentRevision`, and `revision` (the current one).
Query again and re-evaluate the edit before retrying. The lock is shared by the
Go and self-hosted implementations.

Patch validation checks structure and identity, and permits type-invalid drafts
so an agent can repair them. Run `check` after editing. Type errors identify the
offending expression and include `expected` and `actual` types. The default
output is one JSON diagnostic per line followed by a summary with the checked
revision. Success produces only the summary. `--facts` additionally emits
implementation-specific semantic facts.

`build` checks the program first. Success emits exactly one object:
`{"ok":true,"output":"...","bytes":N}`. An unsuccessful build emits diagnostics
or a structured command error and exits 1.

## Program storage and source authoring

`prog/ovid.json` is the checked-in canonical CLI program. `src/cli` contains the
bootstrap authoring form, one `.ov` file per package directory. The runtime CLI
reads JSON, not `.ov` files.

After editing the compiler's `.ov` source:

```sh
go run ./bootstrap/front -root src/cli -out prog -entry ovid/cli
bin/ovid-boot build prog -o bin/ovid
bin/ovid check prog
```

The frontend records the last generated revision in `BOOTSTRAP.json`, under the
same module lock as patches. It refuses to overwrite a canonical program changed
by an agent since the last import. To intentionally discard those canonical
changes, rerun the import with `-replace`. Importing over a pre-existing module
without bootstrap provenance also requires that explicit flag.

Reimport preserves matching declaration IDs and unambiguous AST identities across
common insertions, reordering, and scalar edits. Ambiguous matches get fresh
opaque IDs. Arbitrary text edits cannot guarantee identity preservation; canonical
patches retain exact identities. An unchanged reimport retains the same IDs and
canonical bytes.

The bootstrap emits a human `PROJECTION` stamped with its revision, plus legacy
package markers. Patches update only `ovid.json`; a projection can therefore be
stale. Neither projections, markers, nor bootstrap provenance affect `check` or
`build`. If an interrupted import leaves provenance behind the canonical file,
the next import refuses it; inspect the files before explicitly using `-replace`.

The revision is SHA-256 of the file bytes with the 64 hexadecimal digits after
the first `"revision": "` replaced by zeros. It protects an exact byte snapshot,
including formatting. Query and patch reject a mismatching stored revision;
use the tools to stamp changes rather than manually rewriting the canonical file.

## Language and runtime

The language has `i64`, `bool`, and one level of pointer (`*Struct`). Struct
fields occupy 8 bytes. There are no struct values as locals, parameters, or
results; no globals, function pointers, methods, macros, generics, or implicit
allocation. Functions take at most six parameters and return one value.

Operators follow Go precedence; `>>` is arithmetic. Strings use interned,
NUL-terminated bytes (`strptr`, `strlen`). Memory operations are `load8`, `load32`,
`load64`, `store8`, `store64`, and `ovid/io.Alloc`. The runtime currently uses a
fixed 128 MiB bump heap. Raw pointers and allocation failure require care.

`main` has signature `(io *ovid/io.Cap) i64`. The `Cap` ABI is exactly five `i64`
fields in order: `argc`, `argv`, `heap`, `used`, `size`. Only package `ovid/io` can
use the `syscall` intrinsic. This package boundary is not yet a fine-grained
resource permission system.

Import paths identify packages included in the JSON document. There is no
registry or network fetching. [COMMITMENTS.md](COMMITMENTS.md) records the later
HTTP work; it is not implemented.

## Validation

```sh
go test ./...
```

The suite builds both public CLIs from current sources, including a self-compiled
second generation. It exercises canonical-only initialization, diagnosis and
repair, caller-guided rename, pagination, malformed input, revision conflicts,
and competing Go/Ovid writers. Source-reimport tests check identity preservation
and protection of canonical edits. JSON tests cover escaping, Unicode surrogate
pairs, duplicate keys, trailing input, and signed integer boundaries.

The test harness discovers this checkout instead of requiring `/workspace`.
Executable tests target Linux x86-64.
