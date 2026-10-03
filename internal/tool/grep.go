package tool

import (
	"fmt"
	"io"
	"regexp"
)

// Grep finds a regexp in the module's own source (not shipped packages
// unless std) and names, for each match, the decl and the innermost
// statement around it, so the result can feed show, refs, or edit directly.
func Grep(dir, pattern, pkg string, std bool, w io.Writer) int {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fail(w, "bad_pattern", err.Error(), "the pattern is a Go regexp (RE2); quote it for the shell")
	}
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	filePkg := map[int]string{}
	for _, id := range m.Order {
		l := m.Index[id]
		if _, ok := filePkg[l.Span.File]; !ok {
			filePkg[l.Span.File] = l.Pkg
		}
	}
	count := 0
	for fi, f := range m.Files {
		if m.IsStd(fi) && !std {
			continue
		}
		if pkg != "" && filePkg[fi] != pkg {
			continue
		}
		for _, loc := range re.FindAllIndex(f.Src, -1) {
			p := f.Pos(loc[0])
			r := map[string]any{"file": m.DisplayPath(f), "line": p.Line, "col": p.Col,
				"match": string(f.Src[loc[0]:loc[1]]), "source": lineText(f.Src, loc[0])}
			// The smallest decl and statement spans that hold the match.
			declLen, stLen := 1<<62, 1<<62
			for _, id := range m.Order {
				l := m.Index[id]
				if l.Span.File != fi || loc[0] < l.Span.Off || loc[0] >= l.Span.End {
					continue
				}
				size := l.Span.End - l.Span.Off
				switch l.Kind {
				case "func", "type", "const", "import":
					if size < declLen {
						declLen = size
						r["decl"] = id
					}
				case "stmt":
					if size < stLen {
						stLen = size
						r["stmt"] = id
					}
				}
			}
			emit(w, r)
			count++
		}
	}
	res := map[string]any{"ok": true, "count": count}
	if count == 0 {
		res["hint"] = fmt.Sprintf("no match for %q; ovid refs <name> finds uses by meaning", pattern)
	}
	emit(w, res)
	return ExitOK
}

func lineText(src []byte, off int) string {
	a := lineBegin(src, off)
	b := a
	for b < len(src) && src[b] != '\n' {
		b++
	}
	return string(src[a:b])
}
