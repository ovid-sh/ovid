This directory is the module `ovid`, a compiler for Ovid written in Ovid;
its entry package is `ovid/cli`. Built with `ovid build` (into
`bin/ovid` here), it runs as `bin/ovid check DIR` and prints one JSON line
per error it finds in the module at DIR.

Its messages for a type error in an operand of a binary operator are
vague. For `x + true` it says `arith needs i64`, where `ovid check`, the
`ovid` on your PATH, a separate implementation of the same language, says
`right of +: got bool, want i64`. Make this compiler's `message` for such
errors the same as `ovid check`'s, for every binary operator:
`+ - * / % & | ^ << >> < <= > >= == != && ||`. Change nothing else that
it prints, and keep its own tests (`ovid test`) passing. You are done when
that holds and `ovid check` reports no errors in this module.
