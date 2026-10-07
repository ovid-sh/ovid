# Ovid on Cloudflare Workers (proof of concept)

`hello/hello.ov` is an ordinary `ovid/http` handler: the same source builds
to a native ELF (`ovid build`) that serves requests on stdin, or to wasm
(`ovid build --target wasm`) that a Worker serves.

```sh
npm install
npm run build    # bin/ovid, then src/hello.wasm (~7.6 KB)
npm run smoke    # run the wasm under Node with the Worker's host shim
npm run dev      # wrangler dev on :8787
npm run deploy
```

How it fits together:

- `internal/wasm` compiles the same IR as `internal/compile`. Every value is
  an `i64`; pointers are wrapped to `i32` at each load and store. `syscall`
  becomes the module's one import, `env.syscall(n, a1..a6) -> i64`. The
  module exports `memory` and `_start(argc, argv)`.
- `internal/wasm/host.mjs` answers the syscalls ovid/io makes: `read` on fd
  0 from a byte array, `write` on fd 1/2 into buffers, `mmap` by growing
  memory, `munmap` into a free list, `madvise(DONTNEED)` by zeroing, `exit`
  by throwing. Anything else returns `ENOSYS`, so there is no file system.
  `TestCorpusWasm` runs the tests/run corpus through the same file.
- `src/worker.js` turns each request into the raw HTTP/1.1 bytes that the
  toolchain-written stdio host reads, runs a fresh instance, and parses the
  response it writes.

Limits: the first heap region is 32 MiB (the native one is 128 MiB; an
isolate has 128 MiB in all). Once it is used up, `ovid/io.Alloc` asks for a
128 MiB `HEAPCHUNK`, which a Worker refuses (exit 71, out of memory). Bodies are buffered whole.
`TestSelfHost` does not cover this backend, and `prog/` has no wasm
counterpart.
