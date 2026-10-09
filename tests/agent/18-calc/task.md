The module `calc` in this directory is a calculator: `calc FILE`
evaluates each line of FILE as an integer expression and prints the
values, one a line. It works on good input, but a user reports that a
file holding the line `7 / 0` kills it, and other bad lines print
nonsense or get it wrong without a word.

An expression is decimal numbers, `+`, `-`, `*`, `/` (which truncates
toward zero), parentheses, and unary `-`, with the usual precedence and
left to right; spaces may sit between them. Lines end with a newline (the
last may end without one) and are numbered from 1; a line that is empty
or only spaces is skipped but still counted.

Make it report a bad line. The values of the lines before it are printed
as now; then, for the first bad line, it prints exactly
`calc: line N: REASON` and a newline to standard error, prints nothing
more to standard output, and exits with code 1. REASON is

- `division by zero` for a division by zero;
- `overflow` when a number, or the result of any operation, is outside
  i64 (so `-9223372036854775808` is an overflow, as its number is, but
  `-9223372036854775807 - 1` is not);
- `syntax error` when the line is not an expression.

If FILE cannot be read, or there is no argument, it prints a message to
standard error and exits with code 1, printing nothing. You are done when
the program does all this for any file.
