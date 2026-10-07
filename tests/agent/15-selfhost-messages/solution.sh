# Each vague message becomes a call to NeedOp or Mismatch, which spell it as
# the Go checker does; the helpers are appended to ovid/check. Every line the
# rewrite expects must be found, or prog/ changed under this task.
set -e
f=ovid/check/check.ov
awk '
/NeedI64\(c, "arith needs i64"\)/ { arith++; sub(/NeedI64\(.*/, "NeedOp(c, " (arith == 1 ? "true" : "false") ", op, strptr(\"i64\"))"); n++ }
/Err\(c, .*"comparison types differ"/ { sub(/Err\(.*/, "Mismatch(c, false, op, c.typ, ovid/io.CStr(c.io, t))"); n++ }
/NeedI64\(c, "order needs i64"\)/ { order++; sub(/NeedI64\(.*/, "NeedOp(c, " (order == 1 ? "true" : "false") ", op, strptr(\"i64\"))"); n++ }
/NeedBool\(c, "logic needs bool"\)/ { logic++; sub(/NeedBool\(.*/, "NeedOp(c, " (logic == 1 ? "true" : "false") ", op, strptr(\"bool\"))"); n++ }
{ print }
END { if (n != 7) { print "rewrote " n " lines, want 7" > "/dev/stderr"; exit 1 } }
' "$f" > "$f.new"
mv "$f.new" "$f"
ovid append ovid/check <<'EOF2'
// OpText is the binary operator op as written.
func OpText(op i64) bytes {
  if op == ovid/parse.OP_LOR {
    return "||"
  } else if op == ovid/parse.OP_LAND {
    return "&&"
  } else if op == ovid/parse.OP_EQ {
    return "=="
  } else if op == ovid/parse.OP_NE {
    return "!="
  } else if op == ovid/parse.OP_LT {
    return "<"
  } else if op == ovid/parse.OP_LE {
    return "<="
  } else if op == ovid/parse.OP_GT {
    return ">"
  } else if op == ovid/parse.OP_GE {
    return ">="
  } else if op == ovid/parse.OP_ADD {
    return "+"
  } else if op == ovid/parse.OP_SUB {
    return "-"
  } else if op == ovid/parse.OP_OR {
    return "|"
  } else if op == ovid/parse.OP_XOR {
    return "^"
  } else if op == ovid/parse.OP_MUL {
    return "*"
  } else if op == ovid/parse.OP_DIV {
    return "/"
  } else if op == ovid/parse.OP_MOD {
    return "%"
  } else if op == ovid/parse.OP_AND {
    return "&"
  } else if op == ovid/parse.OP_SHL {
    return "<<"
  }
  return ">>"
}

// Mismatch reports a type_mismatch for one operand of the operator op, in
// the Go checker's words: "left of +: got bool, want i64".
func Mismatch(c *Ch, left bool, op i64, got bytes, want i64) i64 {
  var m *ovid/mem.Buf = ovid/mem.New(c.io, 64)
  if left {
    ovid/mem.WBytes(c.io, m, "left of ")
  } else {
    ovid/mem.WBytes(c.io, m, "right of ")
  }
  ovid/mem.WBytes(c.io, m, OpText(op))
  ovid/mem.WBytes(c.io, m, ": got ")
  ovid/mem.WBytes(c.io, m, got)
  ovid/mem.WBytes(c.io, m, ", want ")
  ovid/mem.WBytes(c.io, m, bytes(want, ovid/io.CLen(want)))
  return Err(c, "type_mismatch", ovid/mem.Bytes(m))
}

// NeedOp reports the operand of op just typed (left or right) unless the
// current type is want (a NUL-terminated literal) or invalid.
func NeedOp(c *Ch, left bool, op i64, want i64) i64 {
  if !Bad(c) && !IsTy(c, bytes(want, ovid/io.CLen(want))) {
    return Mismatch(c, left, op, c.typ, want)
  }
  return 0
}
EOF2
