# Each vague message becomes a call to Mismatch, which spells it as the Go
# checker does; the helpers are appended to ovid/check. Every line the
# rewrite expects must be found, or prog/ changed under this task.
set -e
f=ovid/check/check.ov
awk '
/Err\(c, .*"arith needs i64"/ { arith++; sub(/Err\(.*/, arith == 1 ? "Mismatch(c, true, op, lp, ln, strptr(\"i64\"))" : "Mismatch(c, false, op, c.typ, c.tyn, strptr(\"i64\"))"); n++ }
/Err\(c, .*"comparison types differ"/ { sub(/Err\(.*/, "Mismatch(c, false, op, c.typ, c.tyn, ovid/io.CStr(c.io, lp, ln))"); n++ }
/Err\(c, .*"order needs i64"/ { sub(/Err\(.*/, "Mismatch(c, true, op, lp, ln, strptr(\"i64\"))"); n++ }
/NeedI64\(c, strptr\("order needs i64"\)/ { sub(/NeedI64\(.*/, "NeedOp(c, op, strptr(\"i64\"))"); n++ }
/Err\(c, .*"logic needs bool"/ { sub(/Err\(.*/, "Mismatch(c, true, op, lp, ln, strptr(\"bool\"))"); n++ }
/NeedBool\(c, strptr\("logic needs bool"\)/ { sub(/NeedBool\(.*/, "NeedOp(c, op, strptr(\"bool\"))"); n++ }
{ print }
/var lbool bool = IsTy\(c, strptr\("bool"\), strlen\("bool"\)\)/ { ind = $0; sub(/[^ ].*/, "", ind); print ind "var lp i64 = c.typ"; print ind "var ln i64 = c.tyn"; n++ }
END { if (n != 8) { print "rewrote " n " lines, want 8" > "/dev/stderr"; exit 1 } }
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
    ovid/mem.WBytes(c.io, m, strptr("left of "), strlen("left of "))
  } else {
    ovid/mem.WBytes(c.io, m, strptr("right of "), strlen("right of "))
  }
  ovid/mem.WBytes(c.io, m, OpText(op), ovid/io.CLen(OpText(op)))
  ovid/mem.WBytes(c.io, m, strptr(": got "), strlen(": got "))
  ovid/mem.WBytes(c.io, m, gp, gn)
  ovid/mem.WBytes(c.io, m, strptr(", want "), strlen(", want "))
  ovid/mem.WBytes(c.io, m, want, ovid/io.CLen(want))
  return Err(c, strptr("type_mismatch"), strlen("type_mismatch"), m.data, m.len)
}

// NeedOp reports the right operand of op unless the current type is want
// (a NUL-terminated literal) or invalid.
func NeedOp(c *Ch, op i64, want i64) i64 {
  if !Bad(c) && !IsTy(c, want, ovid/io.CLen(want)) {
    return Mismatch(c, false, op, c.typ, c.tyn, want)
  }
  return 0
}
EOF2
