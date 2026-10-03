package query

import (
	"bytes"
	"encoding/json"

	"ovid/internal/ir"
)

const DefaultLimit = 50

type Options struct {
	ID, Name, Pkg, Kind string
	CallsTo             string
	Full                bool
	Limit, Offset       int
}

func DefaultOptions() Options { return Options{Limit: DefaultLimit} }

type Match struct {
	ID     string          `json:"id"`
	Kind   string          `json:"kind"`
	Pkg    string          `json:"pkg,omitempty"`
	Name   string          `json:"name,omitempty"`
	Func   string          `json:"func,omitempty"`
	FuncID string          `json:"funcId,omitempty"`
	Op     string          `json:"op,omitempty"`
	Node   json.RawMessage `json:"node,omitempty"`
}

type Result struct {
	Revision   string  `json:"revision"`
	Matches    []Match `json:"matches"`
	Total      int     `json:"total"`
	Offset     int     `json:"offset"`
	Limit      int     `json:"limit"`
	Returned   int     `json:"returned"`
	HasMore    bool    `json:"hasMore"`
	NextOffset *int    `json:"nextOffset,omitempty"`
}

// Run performs compact discovery with the default page size. Use RunOptions for
// explicit payload expansion, pagination, or references to a function.
func Run(p *ir.Program, revision, id, name, pkg, kind string) Result {
	return RunOptions(p, revision, Options{ID: id, Name: name, Pkg: pkg, Kind: kind, Limit: DefaultLimit})
}

// RunOptions selects declarations by default. Name, kind, id and calls-to
// filters also search nested nodes. Only --id and --full include node payloads.
// Traversal order is the order in ovid.json; use the revision when paging.
func RunOptions(p *ir.Program, revision string, opts Options) Result {
	res := Result{Revision: revision, Matches: []Match{}, Offset: opts.Offset, Limit: opts.Limit}
	full := opts.Full || opts.ID != ""
	nested := opts.ID != "" || opts.Name != "" || opts.Kind != "" || opts.CallsTo != ""
	callPkg, callName := "", ""
	if opts.CallsTo != "" {
		for _, pk := range p.Packages {
			for _, fn := range pk.Funcs {
				if fn.ID == opts.CallsTo {
					callPkg, callName = pk.Path, fn.Name
				}
			}
		}
	}
	consider := func(m Match, node any) {
		if opts.ID != "" && opts.ID != m.ID || opts.Name != "" && opts.Name != m.Name ||
			opts.Pkg != "" && opts.Pkg != m.Pkg || opts.Kind != "" && opts.Kind != m.Kind {
			return
		}
		if !nested && m.Kind != "package" && m.Kind != "type" && m.Kind != "const" && m.Kind != "func" {
			return
		}
		if opts.CallsTo != "" {
			n, ok := node.(*ir.Node)
			if !ok || n.Op != "call" || callName == "" {
				return
			}
			targetPkg := n.Pkg
			if targetPkg == "" {
				targetPkg = m.Pkg
			}
			if targetPkg != callPkg || n.Func != callName {
				return
			}
		}
		res.Total++
		if res.Total <= opts.Offset || opts.Limit > 0 && len(res.Matches) >= opts.Limit {
			return
		}
		if full {
			m.Node, _ = json.Marshal(node)
		}
		res.Matches = append(res.Matches, m)
	}
	for _, pk := range p.Packages {
		consider(Match{ID: pk.ID, Kind: "package", Pkg: pk.Path, Name: pk.Path}, pk)
		for _, im := range pk.Imports {
			consider(Match{ID: im.ID, Kind: "import", Pkg: pk.Path, Name: im.Path}, im)
		}
		for _, t := range pk.Types {
			consider(Match{ID: t.ID, Kind: "type", Pkg: pk.Path, Name: t.Name}, t)
			for _, f := range t.Fields {
				consider(Match{ID: f.ID, Kind: "field", Pkg: pk.Path, Name: f.Name}, f)
			}
		}
		for _, c := range pk.Consts {
			consider(Match{ID: c.ID, Kind: "const", Pkg: pk.Path, Name: c.Name}, c)
		}
		for _, fn := range pk.Funcs {
			consider(Match{ID: fn.ID, Kind: "func", Pkg: pk.Path, Name: fn.Name}, fn)
			for _, pa := range fn.Params {
				consider(Match{ID: pa.ID, Kind: "param", Pkg: pk.Path, Name: pa.Name, Func: fn.Name, FuncID: fn.ID}, pa)
			}
			if nested {
				var walk func(n *ir.Node)
				walk = func(n *ir.Node) {
					if n == nil {
						return
					}
					consider(Match{ID: n.ID, Kind: kindOf(n), Pkg: pk.Path, Name: n.Name, Func: fn.Name, FuncID: fn.ID, Op: n.Op}, n)
					for _, c := range childList(n) {
						walk(c)
					}
				}
				for _, st := range fn.Body {
					walk(st)
				}
			}
		}
	}
	res.Returned = len(res.Matches)
	res.HasMore = opts.Offset+res.Returned < res.Total
	if res.HasMore {
		next := opts.Offset + res.Returned
		res.NextOffset = &next
	}
	return res
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

func Marshal(r Result) []byte {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	_ = enc.Encode(r)
	return buf.Bytes()
}
