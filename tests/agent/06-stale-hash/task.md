Earlier you read this function of the module `price` in this directory:

```
$ ovid show fn:price.Price
// func fn:price.Price price/main.ov:5-8 hash=0094bd675479
// Price is what n items cost, in cents.
func Price(n i64) i64 {
  return n * 250
}
```

Change it so that an order of zero or fewer items costs 0. Make the change
with `ovid replace fn:price.Price --expect 0094bd675479`, since that is the
version you read. Other people work on this module too. You are done when
the change is in and `ovid check` reports no errors.
