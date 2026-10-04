# The test corpus

Plain Ovid programs with their expectations written as comments. To add a
test, add a file; `go test ./internal/tool -run TestCorpus` runs them all
(`-run TestCorpusRun/run/hello` for one).

A case is either

- a single `.ov` file with `package demo`, which the runner places at
  `demo/main.ov` of a module named `demo`, or
- a directory with its own `ovid.mod`, for programs that need several files
  or packages. Its comments may sit in any of its `.ov` files, including
  those of a std dir it names with `std .std`: a dot directory, so that the
  module itself leaves it out.

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
optional number is its column. After those, any of `expected="..."`,
`got="..."`, and `hint="..."` (Go-quoted) must equal the diagnostic's field
of that name:

```
  return Add(1, true) // error: type_mismatch 17 expected="i64" got="bool"
```

A line that gets two diagnostics carries two comments, one after the other.

The diagnostics `ovid check` reports must be exactly the ones named: one
that is missing fails the case, and so does one that no comment asked for.
A diagnostic that carries no line matches a comment with its code anywhere
in the case.

`go test ./internal/tool -run TestCorpusFail -v` prints what each case
reports in the form a comment takes, ready to paste.

Each diagnostic code has at least one case here, with four exceptions.
`duplicate_package`, `bad_op`, and `missing_expr` guard the program tree
itself, and no source text is known to produce them. `duplicate_id` is
reported today only as a side effect of a repeated func, field, or
parameter, which is a bug (#26); it has no case so that the corpus does not
record it.
