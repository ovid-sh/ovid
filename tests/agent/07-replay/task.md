`Count` in the module `tally` in this directory counted each event twice:
it had two `x = x + 1` statements, and one had to go. You read it with
`ovid show`, and the first of the two was `st:tally.Count:2` with hash
`fa799c299f8e`. You then ran

```
ovid delete st:tally.Count:2 --expect fa799c299f8e
```

but the connection dropped before you saw any output, so you do not know
whether the delete ran. Make sure that `Count` ends up with exactly one
`x = x + 1`. You are done when it does and `ovid check` reports no errors.
