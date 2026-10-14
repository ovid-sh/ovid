This directory is the module `ovid`, a compiler for Ovid written in Ovid
(about 7,700 lines in eleven packages). In the package `ovid/parse`, in
the file `ovid/parse/ast.ov` next to the other helpers over `Decl` lists
(`CountDecls`), add

    func LastDecl(d *Decl) *Decl

which returns the last `Decl` of the list that starts at `d` (they are
chained through `next`), or `d` itself when `d` is nil. Another agent is
changing the same package and file at the same time; keep its work. You
are done when your function is in, `ovid check` reports no errors, and
`ovid test` passes.
