The program in the module `stock` in this directory sums the stock counts
in a file. A user reports that for this file, whose last line has no
newline,

```
bolt 10
nut 30
bolt 2
```

(that is, `bolt 10\nnut 30\nbolt 2`), `ovid run -- stock.txt` prints
`stock: bad line 3` instead of `bolt 12`, `nut 30`, and `total 42` on three
lines. Fix the program so that it does what its doc comments say for any
input. There may be more than one bug; the doc comments are right, so keep
them. You are done when it does.
