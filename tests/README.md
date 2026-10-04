# The test corpus

Plain Ovid programs with their expectations written as comments. To add a
test, add a file; `go test ./internal/tool -run TestCorpus` runs them all
(`-run TestCorpusRun/run/hello` for one).

A case is either

- a single `.ov` file with `package demo`, which the runner places at
  `demo/main.ov` of a module named `demo`, or
- a directory with its own `ovid.mod`, for programs that need several files
  or packages. Its comments may sit in any of its `.ov` files.

## `run/`: programs that must build and run

The program must check and build. On Linux x86-64 it is then run and
compared; elsewhere it is only built.

```
// exit: 3            the exit code (default 0)
// stdout: "two\n"    a Go-quoted string; several lines are concatenated
// args: one two      command-line arguments, split on spaces
```

Each sits alone on its line. stdout defaults to empty, and stderr is not
compared (it is shown when the case fails).

## `fail/`: programs that must be rejected

Put a comment at the end of each line that should get a diagnostic:

```
  return Add(1, true) // error: type_mismatch 17
```

The word is the diagnostic's `code` (see `docs/PROTOCOL.md`), and the
optional number is its column. The diagnostics `ovid check` reports must be
exactly the ones named: one that is missing fails the case, and so does one
that no comment asked for. A diagnostic that carries no line matches a
comment with its code anywhere in the case.
