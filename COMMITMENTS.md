# Commitments after v0

v0 reads local files only. This file records what comes next. It is not implemented.

Network is part of the language, not a later maybe. After `ovid` can compile itself, the first standard library is HTTP: the best HTTP client, harder to misuse than Go's `net/http`.

That same capability does three jobs:

- programs call it to speak HTTP
- the compiler fetches packages when an import path is a URL
- it serves user programs

The import syntax stays. The import path is still the package name. Only the lookup changes: a path that is a URL is fetched with the HTTP capability instead of read from the module directory. v0 resolves every import on the local filesystem.

## The self-hosted compiler is the language (decided 2026-10-07)

`prog/`, the compiler written in Ovid, is where the language lives. The Go
toolchain keeps what it alone has, the agent commands (`edit`, `show`,
`outline`, `grep`, `move`) and the bootstrap, and its compiler follows the
Ovid one rather than leading it. While `TestSelfHost` holds the two
byte-identical, a language change lands in `prog/` first, with `internal/`
following in the same pull request; the parity test stays until the Go
compiler is retired.

What is ported to Ovid is what agents are seen to rely on (#12,
`docs/AGENT_FEEDBACK.md`): `check`, `build`, and `refs` are done; `test`
(fork, exec, wait, and pipes in `ovid/io`) and `rename` (the multi-file write
under the module lock) come next. The rest waits for evidence that agents
reach for it.

Why: the self-hosted compiler is a small static binary (about 155 KB as of
2026-10-07; the README keeps the current figure) that checked `prog/` in
7 MB with nine system calls on one thread when measured in #12 (main at
6d4fcd8), and it builds itself under the seccomp list of its own receipt;
the Go runtime cannot be made to fit that. The cost is that every language
feature is written twice until the Go compiler goes.
