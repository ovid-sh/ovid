The module `sum` in this directory has two packages, `sum`, an empty
program, and `num`.

Add to `num` a func `ParseI64` that takes a bytes `s` and returns a value
and an error code: the integer `s` spells and 0; or 0 and `E_SYNTAX` if
`s` is not an optional `-` followed by one or more decimal digits, with
nothing else, not even spaces; or 0 and `E_RANGE` if it is, but the value
is outside i64 (`-9223372036854775808` and `9223372036854775807` are in
it; leading zeros are fine).

Then make `sum` read standard input, a number a line, and print the sum
of the numbers and a newline. Lines end with a newline (the last may end
without one) and are numbered from 1; an empty line is skipped but still
counted. Each line is parsed with `num.ParseI64`. At the first line that
is not a number in i64, or at which the running sum leaves i64, it
prints nothing to standard output, prints one line to standard error,
`sum: line N: ` and then `not a number`, `out of range`, or `sum
overflows`, and exits with code 1. You are done when `ovid run` does
this for any input.
