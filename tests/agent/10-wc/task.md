The module `wc` in this directory is an empty program. Make it count the
lines, words, and bytes of the file named by its first argument and print
them as `LINES WORDS BYTES` followed by a newline, separated by single
spaces, for example `2 5 27`.

- Lines are the number of newline bytes.
- Words are maximal runs of bytes that are not a space, tab, newline, or
  carriage return.
- Bytes are the file's size.

If there is no argument, or the file cannot be read, print a message to
standard error and exit with code 1, printing nothing to standard output.
You are done when `ovid run -- <file>` works for any file.
