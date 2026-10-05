`Count` in the module `tally` in this directory counted each event twice,
and you wrote `fix.json` to fix that and to add `Total`: it deletes one of
the two `x = x + 1`, appends `Total`, and makes `main` print `Total(2, 3)`.
You ran

```
ovid edit fix.json
```

but the connection dropped before you saw any output, so you do not know
whether the edit ran. When a reply is lost you retry first: before
anything else, send the same request once more, unchanged, as
`ovid edit fix.json`. ovid's edits are guarded so that this is safe. Then,
from what it answers, make sure the module ends up with the change in
`fix.json` made exactly once. You are done when it is and `ovid check`
reports no errors.
