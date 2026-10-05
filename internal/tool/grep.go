package tool

import (
	"fmt"
	"io"
	"regexp"
)

// GrepLimit is how many matches grep prints when no limit is given.
const GrepLimit = PageLimit

// Grep finds a regexp in the module's own source (not shipped packages
// unless std) and names, for each match, the decl and the innermost
// statement around it, so the result can feed show, refs, or edit directly.
// It prints matches offset through offset+limit-1 (limit <= 0 means all);
// the last line counts every match and says where the next page starts.
func Grep(dir, pattern, pkg string, std bool, offset, limit int, w io.Writer) int {
	re, err := regexp.Compile(pattern)
	if err != nil {
		return fail(w, "bad_pattern", err.Error(), "the pattern is a Go regexp (RE2); quote it for the shell")
	}
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	// The decls and statements of each file, so a match is only compared
	// with the nodes of its own file.
	filePkg := map[int]string{}
	byFile := map[int][]string{}
	for _, id := range m.Order() {
		l := m.Index()[id]
		if _, ok := filePkg[l.Span.File]; !ok {
			filePkg[l.Span.File] = l.Pkg
		}
		switch l.Kind {
		case "func", "type", "const", "import", "stmt":
			byFile[l.Span.File] = append(byFile[l.Span.File], id)
		}
	}
	pg := pager{Page: Page{offset, limit}}
	for fi, f := range m.Files {
		if m.IsStd(fi) && !std {
			continue
		}
		if pkg != "" && filePkg[fi] != pkg {
			continue
		}
		for _, loc := range re.FindAllIndex(f.Src, -1) {
			if !pg.take() {
				continue
			}
			p := f.Pos(loc[0])
			r := map[string]any{"file": m.DisplayPath(f), "line": p.Line, "col": p.Col,
				"match": string(f.Src[loc[0]:loc[1]]), "source": lineText(f.Src, loc[0])}
			// The smallest decl and statement spans that hold the match.
			declLen, stLen := 1<<62, 1<<62
			for _, id := range byFile[fi] {
				l := m.Index()[id]
				if loc[0] < l.Full.Off || loc[0] >= l.Full.End {
					continue
				}
				size := l.Full.End - l.Full.Off
				if l.Kind == "stmt" {
					if size < stLen {
						stLen = size
						r["stmt"] = id
					}
				} else if size < declLen {
					declLen = size
					r["decl"] = id
				}
			}
			emit(w, r)
		}
	}
	res := map[string]any{"ok": true, "revision": m.Revision()}
	if pg.more() > 0 {
		res["hint"] = fmt.Sprintf("%d more; --offset %d for the next page, or narrow with --pkg or a tighter pattern", pg.more(), pg.Offset+pg.shown)
	}
	pg.finish(res)
	if pg.total == 0 {
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
