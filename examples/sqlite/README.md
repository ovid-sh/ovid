# sqlread: reading SQLite files from Ovid

A proof of concept: a read-only SQLite reader written in Ovid, with no libc
and no libsqlite3. It parses the file format directly and prints rows the
way the `sqlite3` shell does, so the two can be diffed.

```sh
ovid build -C examples/sqlite -o sqlread
./sqlread file.db                # the tables
./sqlread file.db users          # every row, in rowid order
./sqlread file.db users 42       # one row, found through the b-tree
```

`./verify.sh` builds it and compares its output with `sqlite3` on databases
`sqlite3` creates (page sizes 512, 4096, 65536). `ovid test -C examples/sqlite`
runs the unit tests.

## Packages

- `sqlite/db`: the file header, table b-tree cursor (`First`/`Next`/`Seek`),
  overflow chains, record decoding, and `sqlite_schema`.
- `sqlite/real`: prints a REAL from its IEEE 754 bits, since the language
  has no floating point. It ports sqlite3's own conversion so the digits
  agree.
- `sqlite/cli`: the `sqlread` command.

## What it reads

Rowid tables of any depth, all integer widths, text, blobs, NULL, REAL,
payloads that spill to overflow pages, `INTEGER PRIMARY KEY` columns (the
rowid alias), and whole-number reals stored as integers in REAL columns.

## What it does not do

- No SQL. There is no query language, only table scans and rowid lookups.
- No indexes, `WITHOUT ROWID` tables, or virtual tables.
- No WAL: rows still in the `-wal` file are not seen (it warns).
- No locking: reading while another process writes is unsafe.
- UTF-8 databases only.
- Columns added by `ALTER TABLE ADD COLUMN` are missing from older rows
  (their defaults are not filled in), and a table-level `PRIMARY KEY(col)`
  is not recognized as a rowid alias.
- Not hardened: a corrupt file can make it read out of bounds and crash.
- The whole file is read into memory, and nothing is freed.
- No writing of any kind.

## Benchmark

`bench/bench.py` times `sqlread` against the same program in C
(`bench/sqlread.c`, the same algorithms function for function, at several
gcc optimization levels) and against libsqlite3 through its C API
(`bench/api.c`) and the `sqlite3` shell. It checks that every program prints
the same bytes before timing anything. The C port exists to separate what
the compiler costs from what the program's design costs, so a change to the
Ovid source belongs in `sqlread.c` too.

```sh
bench/bench.py              # 185 MB database, built once into bench/out/
bench/bench.py --levels     # also gcc -O1 and gcc -O2 -fno-inline
bench/bench.py --small      # a tenth of the rows
```
