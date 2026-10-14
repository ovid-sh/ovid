In the module `shop` in this directory, add a function
`Bulk(n i64, each i64) i64`, the price of `n` items that each cost `each`
before tax, where an order of 10 or more items takes 5 off each item's
price before tax. Add the tax to each item with `Calc`, as `Order` does.
Then make `main` also print `Bulk(12, 105)`, on a line of its own after
what it prints now. Another agent is changing the same module at the same
time; keep its work. You are done when your change is in and `ovid check`
reports no errors.
