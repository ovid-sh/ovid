# rb: a Ruby-subset interpreter in Ovid

A proof of concept: a tree-walking interpreter for a subset of Ruby, about
2,500 lines of Ovid, 44 KB as a binary with no libc. It exists to find out
what a dynamic-language runtime needs from Ovid, and to have a program with
recursion, many small allocations, and string-keyed dispatch to measure the
compiler on.

```sh
ovid build -C examples/ruby -o rb
./rb examples/ruby/tests/demo.rb
```

`./verify.py` runs every `tests/*.rb` with `rb` and with `ruby` and
compares standard output byte for byte, and the exit code. Standard error
is not compared: errors are reported as `file:line: message (ClassName)`,
without Ruby's backtrace.

## Packages

One package, `rb`:

- `lex.ov`: tokens, including `#{...}` kept inside the string token
- `parse.ov`: a recursive-descent parser to an AST; interpolation is parsed
  by lexing the inside of each `#{...}` again
- `val.ov`: values (`nil`, booleans, integers, strings, arrays, ranges),
  `to_s`, `inspect`, equality and ordering
- `eval.ov`: scopes, methods, blocks, `yield`, and `break`/`next`/`return`
- `methods.ov`: the built-in methods
- `main.ov`: reads the file, and the interpreter's state

## The subset

- `def` with fixed arity, recursion, implicit return; `if`/`elsif`/`else`,
  `unless`, `while`, `until`, modifiers (`x if c`), `a ? b : c`,
  `and`/`or`/`not`
- blocks (`do |x| ... end`, `{ |x| ... }`) that read and assign the
  enclosing locals; `yield`, `block_given?`, `loop`
- string interpolation, `"%d %s" % [...]`, ranges, `a[i] = v`, `<<`;
  integer `/` and `%` round toward negative infinity, as in Ruby
- `Integer`: `times` `upto` `downto` `even?` `odd?` `zero?` `abs` `digits`
  `chr` ...; `String`: `length` `upcase` `split` `strip` `include?` `chars`
  `to_i` ...; `Array` and `Range`: `each` `each_with_index` `map` `select`
  `reject` `reduce` `sum` `count` `any?` `all?` `find` `sort` `uniq` `min`
  `max` `join` `push` `pop` ...

## What it does not do

- Classes, modules, or objects of your own; `3.class` is the string
  `"Integer"`, which prints the same under `puts` and not under `p`.
- Hashes, symbols, floats, big integers (integers wrap at 64 bits),
  `rescue`, `eval`, `method_missing`, keyword or default arguments, gems.
- Freeing memory: nothing allocated is ever given back (#81).

## Measurements

`fib(30)` (`tests/fib.rb`) and `puts "hi"`, on starship (Ryzen 7 8745HS),
`/usr/bin/time -v`, 2026-10-06:

| | `fib(30)` wall | peak RSS | `puts "hi"` wall | peak RSS |
|---|---|---|---|---|
| rb | 0.36 s | 358 MB | under 0.01 s | 2 MB |
| Ruby 3.4.10 | 0.09 s | 36 MB | 0.04 s | 36 MB |
| Ruby 3.4.10 `--yjit` | 0.07 s | 36 MB | | |

The interpreter is about four times slower than CRuby on calls, and starts
in a fraction of the time. The memory is the cost of never freeing: each of
the 2.7 million calls leaves its scope and argument array behind.

`ovid build` lists the system calls the binary can make:
`[0,1,3,5,9,60,257]` (read, write, close, fstat, mmap, exit, openat). No
script it runs can do anything else.

## What it found

- #136: a fatal error deep in the interpreter has to be carried back to
  `main` by a flag, tested 27 times, since there is no `Exit`.
- #137: ten loops end through a flag, since there is no `break`.
- #81: the memory above.
- #83: built-in methods are found by an `if` chain of 88 name comparisons;
  and a JIT would need indirect calls into generated code, which func
  values as proposed there would not give.
