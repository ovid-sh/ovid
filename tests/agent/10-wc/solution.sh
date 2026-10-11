set -e
cat > wc/main.ov <<'EOF'
package wc

import ovid/io

// IsSpace reports whether c separates words.
func IsSpace(c i64) bool {
  return c == 32 || c == 9 || c == 10 || c == 13
}

func main(io *ovid/io.Cap) i64 {
  if ovid/io.Argc(io) < 2 {
    ovid/io.Eprint(io, "usage: wc FILE\n")
    return 1
  }
  var data bytes, e error = ovid/io.ReadFile(io, ovid/io.Arg(io, 1))
  if e != 0 {
    ovid/io.Eprint(io, "wc: cannot read the file\n")
    return 1
  }
  var p i64 = ptr(data)
  var n i64 = len(data)
  var lines i64 = 0
  var words i64 = 0
  var inword bool = false
  var i i64 = 0
  while i < n {
    var c i64 = load8(p + i)
    if c == 10 {
      lines = lines + 1
    }
    if IsSpace(c) {
      inword = false
    } else if !inword {
      inword = true
      words = words + 1
    }
    i = i + 1
  }
  ovid/io.PrintInt(io, lines)
  ovid/io.Print(io, " ")
  ovid/io.PrintInt(io, words)
  ovid/io.Print(io, " ")
  ovid/io.PrintInt(io, n)
  ovid/io.Print(io, "\n")
  return 0
}
EOF
