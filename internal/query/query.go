package query

import (
	"bytes"
	"encoding/json"

	"ovid/internal/ir"
)

type Match struct {
	ID   string          `json:"id"`
	Kind string          `json:"kind"`
	Pkg  string          `json:"pkg,omitempty"`
	Name string          `json:"name,omitempty"`
	Func string          `json:"func,omitempty"`
	Node json.RawMessage `json:"node,omitempty"`
}

type Result struct {
	Revision string  `json:"revision"`
	Matches  []Match `json:"matches"`
}

// Run selects nodes. With no filters it returns an index of packages, types,
// consts, and funcs (ids only). Any filter returns the full node.
func Run(p *ir.Program, revision, id, name, pkg, kind string) Result {
	full := id != "" || name != "" || pkg != "" || kind != ""
	var ms []Match
	for _, pk := range p.Packages {
		ms = append(ms, consider(full, id, name, pkg, kind, pk.ID, "package", pk.Path, "", "", pk)...)
		for _, im := range pk.Imports {
			ms = append(ms, consider(full, id, name, pkg, kind, im.ID, "import", pk.Path, im.Path, "", im)...)
		}
		for _, t := range pk.Types {
			ms = append(ms, consider(full, id, name, pkg, kind, t.ID, "type", pk.Path, t.Name, "", t)...)
			for _, f := range t.Fields {
				ms = append(ms, consider(full, id, name, pkg, kind, f.ID, "field", pk.Path, f.Name, t.Name, f)...)
			}
		}
		for _, c := range pk.Consts {
			ms = append(ms, consider(full, id, name, pkg, kind, c.ID, "const", pk.Path, c.Name, "", c)...)
		}
		for _, fn := range pk.Funcs {
			ms = append(ms, consider(full, id, name, pkg, kind, fn.ID, "func", pk.Path, fn.Name, "", fn)...)
			for _, pa := range fn.Params {
				ms = append(ms, consider(full, id, name, pkg, kind, pa.ID, "param", pk.Path, pa.Name, fn.Name, pa)...)
			}
			var walk func(n *ir.Node)
			walk = func(n *ir.Node) {
				if n == nil {
					return
				}
				k := kindOf(n)
				ms = append(ms, consider(full, id, name, pkg, kind, n.ID, k, pk.Path, n.Name, fn.Name, n)...)
				n.Left.Walk(func(c *ir.Node) {})
				for _, c := range childList(n) {
					walk(c)
				}
			}
			for _, st := range fn.Body {
				walk(st)
			}
		}
	}
	if ms == nil {
		ms = []Match{}
	}
	return Result{Revision: revision, Matches: ms}
}

func childList(n *ir.Node) []*ir.Node {
	var out []*ir.Node
	push := func(c *ir.Node) {
		if c != nil {
			out = append(out, c)
		}
	}
	push(n.Left)
	push(n.Right)
	push(n.Arg)
	push(n.Base)
	push(n.Addr)
	push(n.Val)
	push(n.Cond)
	out = append(out, n.Args...)
	out = append(out, n.Then...)
	out = append(out, n.Else...)
	out = append(out, n.Body...)
	return out
}

func kindOf(n *ir.Node) string {
	switch n.Op {
	case "var", "assign", "setfield", "store8", "store64", "return", "if", "while", "expr":
		return "stmt"
	default:
		return "expr"
	}
}

func consider(full bool, id, name, pkg, kind, gotID, gotKind, gotPkg, gotName, gotFunc string, node any) []Match {
	if id != "" && id != gotID {
		return nil
	}
	if name != "" && name != gotName {
		return nil
	}
	if pkg != "" && pkg != gotPkg {
		return nil
	}
	if kind != "" && kind != gotKind {
		return nil
	}
	// No filters: index only, skip statements and expressions.
	if !full && (gotKind == "stmt" || gotKind == "expr" || gotKind == "param" || gotKind == "field" || gotKind == "import") {
		return nil
	}
	m := Match{ID: gotID, Kind: gotKind, Pkg: gotPkg, Name: gotName, Func: gotFunc}
	if full {
		raw, err := json.Marshal(node)
		if err == nil {
			m.Node = raw
		}
	}
	return []Match{m}
}

func Marshal(r Result) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
	return buf.Bytes()
}
