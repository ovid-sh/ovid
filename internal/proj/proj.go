package proj

import (
	"fmt"
	"strconv"
	"strings"

	"ovid/internal/ir"
)

// Text renders a human projection. Agents do not need this file.
func Text(p *ir.Program) string {
	var b strings.Builder
	fmt.Fprintf(&b, "// Ovid projection generated from ovid.json\n")
	fmt.Fprintf(&b, "// entry %s\n", p.Entry)
	fmt.Fprintf(&b, "// revision %s\n\n", p.Revision)
	for i, pkg := range p.Packages {
		if i > 0 {
			b.WriteString("\n")
		}
		fmt.Fprintf(&b, "package %s\n\n", pkg.Path)
		for _, im := range pkg.Imports {
			fmt.Fprintf(&b, "import %s\n", im.Path)
		}
		if len(pkg.Imports) > 0 {
			b.WriteByte('\n')
		}
		for _, t := range pkg.Types {
			fmt.Fprintf(&b, "type %s struct {\n", t.Name)
			for _, f := range t.Fields {
				fmt.Fprintf(&b, "  %s %s\n", f.Name, showType(pkg.Path, f.Type))
			}
			b.WriteString("}\n\n")
		}
		for _, c := range pkg.Consts {
			fmt.Fprintf(&b, "const %s i64 = %d\n", c.Name, c.Value)
		}
		if len(pkg.Consts) > 0 {
			b.WriteByte('\n')
		}
		for _, fn := range pkg.Funcs {
			b.WriteString("func ")
			b.WriteString(fn.Name)
			b.WriteByte('(')
			for i, pa := range fn.Params {
				if i > 0 {
					b.WriteString(", ")
				}
				fmt.Fprintf(&b, "%s %s", pa.Name, showType(pkg.Path, pa.Type))
			}
			b.WriteString(") ")
			b.WriteString(showType(pkg.Path, fn.Result))
			b.WriteString(" {\n")
			writeStmts(&b, pkg.Path, fn.Body, 1)
			b.WriteString("}\n\n")
		}
	}
	return b.String()
}

func showType(pkg, t string) string {
	prefix := pkg + "."
	if strings.HasPrefix(t, "*"+prefix) {
		return "*" + strings.TrimPrefix(t, "*"+prefix)
	}
	if strings.HasPrefix(t, prefix) {
		return strings.TrimPrefix(t, prefix)
	}
	return t
}

func writeStmts(b *strings.Builder, pkg string, stmts []*ir.Node, ind int) {
	pad := strings.Repeat("  ", ind)
	for _, s := range stmts {
		if s == nil {
			continue
		}
		b.WriteString(pad)
		switch s.Op {
		case "var":
			fmt.Fprintf(b, "var %s %s", s.Name, showType(pkg, s.Type))
			if s.Val != nil {
				b.WriteString(" = ")
				b.WriteString(expr(pkg, s.Val))
			}
			b.WriteByte('\n')
		case "assign":
			fmt.Fprintf(b, "%s = %s\n", s.Name, expr(pkg, s.Val))
		case "setfield":
			fmt.Fprintf(b, "%s.%s = %s\n", expr(pkg, s.Base), s.Name, expr(pkg, s.Val))
		case "store8", "store64":
			fmt.Fprintf(b, "%s(%s, %s)\n", s.Op, expr(pkg, s.Addr), expr(pkg, s.Val))
		case "expr":
			fmt.Fprintf(b, "%s\n", expr(pkg, s.Val))
		case "return":
			if s.Val == nil {
				b.WriteString("return\n")
			} else {
				fmt.Fprintf(b, "return %s\n", expr(pkg, s.Val))
			}
		case "if":
			fmt.Fprintf(b, "if %s {\n", expr(pkg, s.Cond))
			writeStmts(b, pkg, s.Then, ind+1)
			if len(s.Else) == 1 && s.Else[0] != nil && s.Else[0].Op == "if" {
				b.WriteString(pad)
				b.WriteString("} else ")
				// reprint if without indent pad duplicated: write the if header inline
				writeIf(b, pkg, s.Else[0], ind)
			} else if len(s.Else) > 0 {
				b.WriteString(pad)
				b.WriteString("} else {\n")
				writeStmts(b, pkg, s.Else, ind+1)
				b.WriteString(pad)
				b.WriteString("}\n")
			} else {
				b.WriteString(pad)
				b.WriteString("}\n")
			}
		case "while":
			fmt.Fprintf(b, "while %s {\n", expr(pkg, s.Cond))
			writeStmts(b, pkg, s.Body, ind+1)
			b.WriteString(pad)
			b.WriteString("}\n")
		default:
			fmt.Fprintf(b, "// %s\n", s.Op)
		}
	}
}

func writeIf(b *strings.Builder, pkg string, s *ir.Node, ind int) {
	pad := strings.Repeat("  ", ind)
	fmt.Fprintf(b, "if %s {\n", expr(pkg, s.Cond))
	writeStmts(b, pkg, s.Then, ind+1)
	if len(s.Else) > 0 {
		b.WriteString(pad)
		b.WriteString("} else {\n")
		writeStmts(b, pkg, s.Else, ind+1)
	}
	b.WriteString(pad)
	b.WriteString("}\n")
}

func expr(pkg string, n *ir.Node) string {
	if n == nil {
		return "?"
	}
	switch n.Op {
	case "int":
		return strconv.FormatInt(n.Int, 10)
	case "bool":
		if n.Bool {
			return "true"
		}
		return "false"
	case "name":
		return n.Name
	case "strptr":
		return "strptr(" + strconv.Quote(n.Str) + ")"
	case "strlen":
		return "strlen(" + strconv.Quote(n.Str) + ")"
	case "add":
		return bin(pkg, n, "+")
	case "sub":
		return bin(pkg, n, "-")
	case "mul":
		return bin(pkg, n, "*")
	case "div":
		return bin(pkg, n, "/")
	case "mod":
		return bin(pkg, n, "%")
	case "and":
		return bin(pkg, n, "&")
	case "or":
		return bin(pkg, n, "|")
	case "xor":
		return bin(pkg, n, "^")
	case "shl":
		return bin(pkg, n, "<<")
	case "shr":
		return bin(pkg, n, ">>")
	case "eq":
		return bin(pkg, n, "==")
	case "ne":
		return bin(pkg, n, "!=")
	case "lt":
		return bin(pkg, n, "<")
	case "le":
		return bin(pkg, n, "<=")
	case "gt":
		return bin(pkg, n, ">")
	case "ge":
		return bin(pkg, n, ">=")
	case "land":
		return bin(pkg, n, "&&")
	case "lor":
		return bin(pkg, n, "||")
	case "not":
		return "!(" + expr(pkg, n.Arg) + ")"
	case "neg":
		return "-(" + expr(pkg, n.Arg) + ")"
	case "bnot":
		return "^(" + expr(pkg, n.Arg) + ")"
	case "cast":
		return "(" + expr(pkg, n.Arg) + " as " + showType(pkg, n.Type) + ")"
	case "field":
		return expr(pkg, n.Base) + "." + n.Name
	case "load8", "load32", "load64":
		return n.Op + "(" + expr(pkg, n.Arg) + ")"
	case "syscall":
		return "syscall(" + args(pkg, n.Args) + ")"
	case "call":
		name := n.Func
		if n.Pkg != "" {
			name = n.Pkg + "." + n.Func
		}
		return name + "(" + args(pkg, n.Args) + ")"
	default:
		return n.Op
	}
}

func bin(pkg string, n *ir.Node, op string) string {
	return "(" + expr(pkg, n.Left) + " " + op + " " + expr(pkg, n.Right) + ")"
}

func args(pkg string, ns []*ir.Node) string {
	var ss []string
	for _, n := range ns {
		ss = append(ss, expr(pkg, n))
	}
	return strings.Join(ss, ", ")
}
