The module `cp` in this directory is a program that copies a file,
`cp SRC DST`, which takes exactly two arguments. A user reports that, built
with `ovid build`, `cp missing.txt out.txt`, where `missing.txt` does not
exist, exits with code 0, prints nothing, and leaves an empty `out.txt`
behind.

Fix the program so that it never fails silently. Whenever it cannot do
the copy, for any reason, it must print a message to standard error that
names the file it could not read or write (or, given the wrong number of
arguments, says how to use it), exit with code 1, print nothing to
standard output, and leave no DST behind if there was none. When it can,
it copies SRC to DST, prints nothing, and exits with code 0. The missing
file may not be the only failure it ignores today. You are done when it
behaves so for any arguments.
