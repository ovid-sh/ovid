This directory is the module `ovid`, a compiler for Ovid written in Ovid
(about 7,700 lines in eleven packages). Rename the function
`ovid/parse.FindDecl` to `LookupDecl`, everywhere it is used; it is called
from several packages. Another agent is adding code to the same module,
in the same package and file, at the same time; keep its work. You are
done when nothing is named `FindDecl` any more, `ovid check` reports no
errors, and `ovid test` passes.
