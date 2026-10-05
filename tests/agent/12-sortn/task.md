The module `sortn` in this directory is an empty program. Make it read
integers from standard input, one per line, and print them in ascending
order, one per line, each followed by a newline.

- A line is an optional `-` followed by one or more decimal digits, and
  must be a value of `i64`. Nothing else is allowed on it, not even spaces.
- Lines end with a newline; the last one may end without one. Empty lines
  are skipped.
- If any line is not such a number, print a message to standard error and
  exit with code 1, printing nothing to standard output.

The package must also have `func Sort(io *ovid/io.Cap, p i64, n i64) i64`,
which sorts the `n` values of 8 bytes at `p` in place, ascending, returns
0, and is what `main` uses. Inputs can have millions of lines, so it must
be fast: sorting two million values must take well under a second. You are
done when `ovid run` does all this.
