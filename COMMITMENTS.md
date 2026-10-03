# Commitments after v0

v0 reads local files only. This file records what comes next. It is not implemented.

Network is part of the language, not a later maybe. After `ovid` can compile itself, the first standard library is HTTP: the best HTTP client, harder to misuse than Go's `net/http`.

That same capability does three jobs:

- programs call it to speak HTTP
- the compiler fetches packages when an import path is a URL
- it serves user programs

The import syntax stays. The import path is still the package name. Only the lookup changes: a path that is a URL is fetched with the HTTP capability instead of read from the module directory. v0 resolves every import on the local filesystem.
