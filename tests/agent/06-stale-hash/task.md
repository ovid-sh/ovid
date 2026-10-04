Earlier you read this function of the module `price` in this directory:

```
$ ovid show fn:price.Price
// func fn:price.Price price/main.ov:6-8 hash=c24bc7bb9248
func Price(n i64) i64 {  // @fn:price.Price
  return n * 250  // @st:price.Price:1
}
```

Change it so that an order of zero or fewer items costs 0. Make the change
with `ovid replace fn:price.Price --expect c24bc7bb9248`, since that is the
version you read. Other people work on this module too. You are done when
the change is in and `ovid check` reports no errors.
