# Ovid

Ovid v0 is a compiler that can compile its own command-line tool. The tool is one static Linux x86-64 binary named `ovid`, with four commands: `query`, `patch`, `check`, and `build`.

The program on disk is plain JSON (`ovid.json`). That file is the source an agent reads and writes. `PROJECTION` is a plain-text view for a person. Agents do not need it. There is no private binary program format and no agent protocol beyond the JSON file and these four commands.

## v0 is done when

1. `check` is faster than a cold `go build` with an empty build cache, and prints one JSON fact per line.
2. An agent can fix a broken program without reading the projection: `query`, `patch`, `check`. `query` returns stable ids. `patch` addresses those ids and is rejected when the file's revision changed.
3. `build` emits one static binary, and that binary compiles this CLI once, without invoking `go`.

Measured on this tree (wall clock, `date +%s%3N`):

- Cold `GOCACHE=$(mktemp -d) go build -o /tmp/ovid-cold ./cmd/ovid`: 2681 ms. The standard library in `GOROOT` was already built.
- `ovid check prog` from the binary that `ovid` itself emitted: 318 ms, 292 JSON facts, exit 0.
- That same binary, with `PATH` pointing at an empty directory, compiled `prog` to a 172920-byte static executable. `check` of that result matches.

## Build

The first compiler is Go. Go builds the machine. The machine's first program is this CLI, written in Ovid under `src/cli`.

```sh
go build -o bin/ovid-boot ./cmd/ovid
bin/ovid-boot build prog -o bin/ovid
```

`bin/ovid` is a static `ET_EXEC` ELF. It does not link libc and it does not call `go`. Compile the CLI again with it:

```sh
bin/ovid build prog -o bin/ovid2
bin/ovid2 check prog
```

Regenerate `prog/` after editing `src/cli` (one `.ov` file per package directory):

```sh
go run ./bootstrap/front -root src/cli -out prog -entry ovid/cli
bin/ovid check prog
```

`src/` is authoring input for the bootstrap. `ovid` reads the module directory (`prog/`), not the `.ov` files.

Tests for the Go bootstrap:

```sh
go test ./...
```

## Commands

```text
ovid check <dir>
ovid query <dir> [--id ID] [--name NAME] [--pkg PATH] [--kind KIND]
ovid patch <dir> <patch.json>
ovid build <dir> -o <file>
```

`check` writes one JSON object per line. The last line is a summary. A type error is an `"fact":"error"` line and a non-zero exit. `build` runs the same check first and, on success, writes one JSON object: `{"ok":true,"output":"...","bytes":N}`.

`query` with no filters lists packages, types, consts, and funcs. Any filter also returns statements, expressions, and the raw JSON `node` for each match. The object starts with the computed revision:

```json
{"revision": "<64 hex>", "matches": [{"id": "ex:demo.main:2", "kind": "expr", "pkg": "demo", "name": "", "node": {}}]}
```

Ids come from source order: `pkg:`, `im:`, `ty:`, `fld:`, `cn:`, `fn:`, `pa:`, `st:`, `ex:`. They stay put as long as that order stays put.

`patch` applies `replace` ops. The patch names the revision `query` returned. If the file has moved on, the command exits 2 and does not write:

```json
{"ok":false,"error":"stale_patch","revision":"<current 64 hex>"}
```

```json
{"baseRevision":"<64 hex>","ops":[{"op":"replace","id":"ex:demo.main:2","node":{"id":"ex:demo.main:2","op":"int","value":2}}]}
```

A successful patch rewrites `ovid.json` and stamps a new revision. It does not rewrite `PROJECTION`. The projection is not an input to the compiler.

## What a module looks like

```text
prog/ovid.json          canonical program
prog/PROJECTION         human text, required to exist
prog/<import path>/PACKAGE
```

The revision is the SHA-256 of the file bytes with the 64 hex digits after the first `"revision": "` replaced by zeros. `check` and `patch` recompute that hash. A stored revision that does not match is an error.

`build` emits one static binary. `main` is `(io *ovid/io.Cap) i64`. The runtime maps a heap and passes the capability in. There is no global allocator. `syscall` exists only in package `ovid/io`.

## Imports

The import path is the package name. One directory is one package. There are no header files.

v0 resolves those paths on the local filesystem, relative to the module root. There is no package registry and no network fetch. See `COMMITMENTS.md` for the later HTTP lookup.

## Language, briefly

`i64`, `bool`, and one level of pointer (`*Struct`). Struct fields are 8 bytes. No struct values as locals, parameters, or results. No globals, function pointers, methods, macros, generics, or implicit allocation. At most six parameters. The result is one type. Operators follow Go precedence. `>>` is arithmetic. Strings are interned, NUL-terminated bytes (`strptr`, `strlen`). Memory is `load8`, `load32`, `load64`, `store8`, `store64`, and `ovid/io.Alloc`.

## Not in v0

No HTTP client, no URL imports, no package server, no language server. Those are recorded in `COMMITMENTS.md` and are not implemented here.
