set -e
cat > calc/main.ov <<'OV'
package calc

import ovid/io

const MaxI64 i64 = 9223372036854775807
const MinI64 i64 = -9223372036854775807 - 1

// The error codes the parsing funcs return.
const E_ZERO i64 = 1
const E_OVERFLOW i64 = 2
const E_SYNTAX i64 = 3

// Reason is the message for the error code e.
func Reason(e i64) bytes {
  if e == E_ZERO {
    return "division by zero"
  } else if e == E_OVERFLOW {
    return "overflow"
  }
  return "syntax error"
}

// P parses one line: its text and the position of the next byte.
type P struct {
  s bytes
  pos i64
}

// Peek moves past spaces and returns the next byte, or -1 at the end of
// the line.
func Peek(p *P) i64 {
  while p.pos < len(p.s) && p.s[p.pos] == 32 {
    p.pos = p.pos + 1
  }
  if p.pos == len(p.s) {
    return -1
  }
  return p.s[p.pos]
}

// Apply is a op b for op one of + - * /: the value and 0, or 0 and
// E_ZERO or E_OVERFLOW.
func Apply(op i64, a i64, b i64) (i64, i64) {
  if op == 43 {
    if (b > 0 && a > MaxI64 - b) || (b < 0 && a < MinI64 - b) {
      return 0, E_OVERFLOW
    }
    return a + b, 0
  }
  if op == 45 {
    if (b < 0 && a > MaxI64 + b) || (b > 0 && a < MinI64 + b) {
      return 0, E_OVERFLOW
    }
    return a - b, 0
  }
  if op == 42 {
    if a == -1 {
      if b == MinI64 {
        return 0, E_OVERFLOW
      }
      return 0 - b, 0
    }
    var r i64 = a * b
    if a != 0 && r / a != b {
      return 0, E_OVERFLOW
    }
    return r, 0
  }
  if b == 0 {
    return 0, E_ZERO
  }
  if a == MinI64 && b == -1 {
    return 0, E_OVERFLOW
  }
  return a / b, 0
}

// Expr is a sum: Term { ("+" | "-") Term }, left to right.
func Expr(p *P) (i64, i64) {
  var v i64, e i64 = Term(p)
  var op i64 = Peek(p)
  while e == 0 && (op == 43 || op == 45) {
    p.pos = p.pos + 1
    var w i64 = 0
    w, e = Term(p)
    if e == 0 {
      v, e = Apply(op, v, w)
    }
    op = Peek(p)
  }
  return v, e
}

// Term is a product: Factor { ("*" | "/") Factor }, left to right; "/"
// truncates toward zero.
func Term(p *P) (i64, i64) {
  var v i64, e i64 = Factor(p)
  var op i64 = Peek(p)
  while e == 0 && (op == 42 || op == 47) {
    p.pos = p.pos + 1
    var w i64 = 0
    w, e = Factor(p)
    if e == 0 {
      v, e = Apply(op, v, w)
    }
    op = Peek(p)
  }
  return v, e
}

// Factor is a decimal number, "(" Expr ")", or "-" Factor.
func Factor(p *P) (i64, i64) {
  var c i64 = Peek(p)
  if c == 40 {
    p.pos = p.pos + 1
    var v i64, e i64 = Expr(p)
    if e != 0 {
      return 0, e
    }
    if Peek(p) != 41 {
      return 0, E_SYNTAX
    }
    p.pos = p.pos + 1
    return v, 0
  }
  if c == 45 {
    p.pos = p.pos + 1
    var v i64, e i64 = Factor(p)
    if e != 0 {
      return 0, e
    }
    return Apply(45, 0, v)
  }
  if c < 48 || c > 57 {
    return 0, E_SYNTAX
  }
  var v i64 = 0
  var e i64 = 0
  while p.pos < len(p.s) && p.s[p.pos] >= 48 && p.s[p.pos] <= 57 {
    var d i64 = p.s[p.pos] - 48
    if e == 0 && v > (MaxI64 - d) / 10 {
      e = E_OVERFLOW
    }
    v = v * 10 + d
    p.pos = p.pos + 1
  }
  return v, e
}

// Line is the value of the expression s, a whole line, and 0, or 0 and
// an error code.
func Line(io *ovid/io.Cap, s bytes) (i64, i64) {
  var p *P = ovid/io.Alloc(io, sizeof(P)) as *P
  p.s = s
  var v i64, e i64 = Expr(p)
  if e == 0 && Peek(p) != -1 {
    e = E_SYNTAX
  }
  return v, e
}

// Blank reports whether s is empty or only spaces.
func Blank(s bytes) bool {
  var i i64 = 0
  while i < len(s) && s[i] == 32 {
    i = i + 1
  }
  return i == len(s)
}

// main evaluates each line of the file FILE (calc FILE) and prints the
// values, one a line. Blank lines are skipped.
func main(io *ovid/io.Cap) i64 {
  if ovid/io.Argc(io) != 2 {
    ovid/io.Eprint("usage: calc FILE\n")
    return 1
  }
  var data bytes, e i64 = ovid/io.ReadFile(io, ovid/io.Arg(io, 1))
  if e != 0 {
    ovid/io.Eprint("calc: ")
    ovid/io.Eprint(ovid/io.Arg(io, 1))
    ovid/io.Eprint(": ")
    ovid/io.Eprint(ovid/io.ErrText(e))
    ovid/io.Eprint("\n")
    return 1
  }
  var n i64 = 1
  var start i64 = 0
  var i i64 = 0
  while i <= len(data) {
    if i == len(data) || data[i] == 10 {
      var line bytes = data[start:i]
      if !Blank(line) {
        var v i64, le i64 = Line(io, line)
        if le != 0 {
          ovid/io.Eprint("calc: line ")
          ovid/io.WriteInt(io, 2, n)
          ovid/io.Eprint(": ")
          ovid/io.Eprint(Reason(le))
          ovid/io.Eprint("\n")
          return 1
        }
        ovid/io.PrintInt(io, v)
        ovid/io.Print("\n")
      }
      start = i + 1
      n = n + 1
    }
    i = i + 1
  }
  return 0
}
OV
