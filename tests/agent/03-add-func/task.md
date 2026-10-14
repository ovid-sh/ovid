In the module `shop` in this directory, add a function
`Discount(total i64) i64`: a total of 100 or more gets 10% off, and the
discounted total is rounded down; a smaller total is unchanged. Make `main`
print the discounted total instead of the raw one, and add a test
`TestDiscount` that covers both cases. You are done when `ovid run` prints
`108` and `ovid test` passes.
