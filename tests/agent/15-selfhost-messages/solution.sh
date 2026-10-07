# Each vague message becomes a call to NeedOp or Mismatch, which spell it as
# the Go checker does; the helpers are appended to ovid/check. Every line the
# rewrite expects must be found, or prog/ changed under this task.
set -e
f=ovid/check/check.ov
awk '
/NeedI64\(c, strptr\("arith needs i64"\)/ { arith++; sub(/NeedI64\(.*/, "NeedOp(c, " (arith == 1 ? "true" : "false") ", op, strptr(\"i64\"))"); n++ }
/Err\(c, .*"comparison types differ"/ { sub(/Err\(.*/, "Mismatch(c, false, op, c.typ, c.tyn, ovid/io.CStr(c.io, bytes(lp, ln)))"); n++ }
/NeedI64\(c, strptr\("order needs i64"\)/ { order++; sub(/NeedI64\(.*/, "NeedOp(c, " (order == 1 ? "true" : "false") ", op, strptr(\"i64\"))"); n++ }
/NeedBool\(c, strptr\("logic needs bool"\)/ { logic++; sub(/NeedBool\(.*/, "NeedOp(c, " (logic == 1 ? "true" : "false") ", op, strptr(\"bool\"))"); n++ }
{ print }
END { if (n != 7) { print "rewrote " n " lines, want 7" > "/dev/stderr"; exit 1 } }
' "$f" > "$f.new"
mv "$f.new" "$f"
ovid append ovid/check <<'EOF2'
// OpText is the binary operator op as written, a NUL-terminated literal.
func OpText(op i64) i64 {
  if op == ovid/parse.OP_LOR {
    return strptr("||")
  } else if op == ovid/parse.OP_LAND {
    return strptr("&&")
  } else if op == ovid/parse.OP_EQ {
    return strptr("==")
  } else if op == ovid/parse.OP_NE {
    return strptr("!=")
  } else if op == ovid/parse.OP_LT {
    return strptr("<")
  } else if op == ovid/parse.OP_LE {
    return strptr("<=")
  } else if op == ovid/parse.OP_GT {
    return strptr(">")
  } else if op == ovid/parse.OP_GE {
    return strptr(">=")
  } else if op == ovid/parse.OP_ADD {
    return strptr("+")
  } else if op == ovid/parse.OP_SUB {
    return strptr("-")
  } else if op == ovid/parse.OP_OR {
    return strptr("|")
  } else if op == ovid/parse.OP_XOR {
    return strptr("^")
  } else if op == ovid/parse.OP_MUL {
    return strptr("*")
  } else if op == ovid/parse.OP_DIV {
    return strptr("/")
  } else if op == ovid/parse.OP_MOD {
    return strptr("%")
  } else if op == ovid/parse.OP_AND {
    return strptr("&")
  } else if op == ovid/parse.OP_SHL {
    return strptr("<<")
  }
  return strptr(">>")
}

// Mismatch reports a type_mismatch for one operand of the operator op, in
// the Go checker's words: "left of +: got bool, want i64".
func Mismatch(c *Ch, left bool, op i64, gp i64, gn i64, want i64) i64 {
  var m *ovid/mem.Buf = ovid/mem.New(c.io, 64)
  if left {
    ovid/mem.WBytes(c.io, m, "left of ")
  } else {
    ovid/mem.WBytes(c.io, m, "right of ")
  }
  ovid/mem.WBytes(c.io, m, bytes(OpText(op), ovid/io.CLen(OpText(op))))
  ovid/mem.WBytes(c.io, m, ": got ")
  ovid/mem.WBytes(c.io, m, bytes(gp, gn))
  ovid/mem.WBytes(c.io, m, ", want ")
  ovid/mem.WBytes(c.io, m, bytes(want, ovid/io.CLen(want)))
  return Err(c, strptr("type_mismatch"), strlen("type_mismatch"), m.data, m.len)
}

// NeedOp reports the operand of op just typed (left or right) unless the
// current type is want (a NUL-terminated literal) or invalid.
func NeedOp(c *Ch, left bool, op i64, want i64) i64 {
  if !Bad(c) && !IsTy(c, want, ovid/io.CLen(want)) {
    return Mismatch(c, left, op, c.typ, c.tyn, want)
  }
  return 0
}
EOF2
