# The rename lands first, through ovid, so it is atomic for the other
# writer. The other writer appends its function with ovid too: append
# into the package needs no guard and cannot overwrite the renamed file,
# so both changes survive whichever order they land in.
set -e
ovid rename fn:ovid/parse.FindDecl LookupDecl
ovid append ovid/parse --file ovid/parse/ast.ov <<'EOF2'
// LastDecl returns the last decl of the list that starts at d, or d when
// the list is empty.
func LastDecl(d *Decl) *Decl {
  if d == 0 as *Decl {
    return d
  }
  while d.next != 0 as *Decl {
    d = d.next
  }
  return d
}
EOF2
