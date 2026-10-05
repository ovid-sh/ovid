package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"ovid/internal/ir"
	"ovid/internal/module"
	"ovid/internal/syntax"
)

// EditOp is one edit, addressed by id. Text is Ovid source.
//
//	replace  id, text       replace the node's source
//	delete   id             remove the node (whole lines for statements and decls)
//	insert   before|after, text   insert statements or decls next to an id
//	append   into, text     append statements to a func/while/if body, or
//	                        decls to a package (into = package path; file optional)
//
// expect must equal the hash of the id the op addresses or, for a
// statement or expression, of its enclosing declaration (`ovid show` prints
// it in its header); otherwise the edit is stale. It is optional for
// declaration ids, which are names, and required for st:/ex: ids, which
// are positions: an insert renumbers everything after it. Force skips it.
type EditOp struct {
	Op     string `json:"op"`
	ID     string `json:"id,omitempty"`
	Before string `json:"before,omitempty"`
	After  string `json:"after,omitempty"`
	Into   string `json:"into,omitempty"`
	File   string `json:"file,omitempty"`
	Text   string `json:"text,omitempty"`
	Expect string `json:"expect,omitempty"`
	// Given names the fields the caller spelled out, even as "": the JSON
	// keys of a request, or the flags of a single-op command. A field given
	// empty is still one the op may not take.
	Given []string `json:"-"`
}

type EditReq struct {
	Revision string   `json:"revision,omitempty"`
	Ops      []EditOp `json:"ops"`
}

type splice struct {
	op    int
	abs   string
	off   int
	end   int
	text  string
	isNew bool // creates the file
	// newOff is set when applied: where text starts in the new file.
	newOff int
}

// Check guards for applySplices.
const (
	guardNone    = iota
	guardClean   // refuse if any check error remains
	guardNoWorse // refuse if the change adds a check error (see newDiags)
)

type editErr struct {
	code, msg, hint string
	extra           map[string]any
	exit            int
}

// The keys an edit request and an op may carry. Anything else is refused,
// not ignored: a misspelled "expect" must not become an unguarded write.
var (
	reqKeys = []string{"ops", "revision"}
	opKeys  = []string{"op", "id", "before", "after", "into", "file", "text", "expect"}
)

// parseEditReq decodes an edit request strictly: an unknown key, a repeated
// key, or trailing data fails. op is the index of the op at fault, or -1.
func parseEditReq(raw []byte) (req *EditReq, op int, err error) {
	var probe any
	if err := json.Unmarshal(raw, &probe); err != nil {
		return nil, -1, err
	}
	req = &EditReq{}
	var ops []json.RawMessage
	single := false
	switch probe.(type) {
	case []any:
		if err := json.Unmarshal(raw, &ops); err != nil {
			return nil, -1, err
		}
	case map[string]any:
		keys, vals, err := members(raw)
		if err != nil {
			return nil, -1, fmt.Errorf("request: %v", err)
		}
		if _, isOp := vals["op"]; isOp && vals["ops"] == nil {
			// One op on its own, which may carry the request's revision.
			single = true
			ops = []json.RawMessage{raw}
		} else {
			if k := unknownKey(keys, reqKeys); k != "" {
				return nil, -1, fmt.Errorf("request: unknown key %q; a request has %s", k, strings.Join(reqKeys, ", "))
			}
			if v, ok := vals["ops"]; ok {
				if err := json.Unmarshal(v, &ops); err != nil {
					return nil, -1, fmt.Errorf("request: ops: %v", err)
				}
			}
		}
		if v, ok := vals["revision"]; ok {
			if err := json.Unmarshal(v, &req.Revision); err != nil {
				return nil, -1, fmt.Errorf("request: revision: %v", err)
			}
		}
	default:
		return nil, -1, fmt.Errorf("want an object or a list of ops")
	}
	if len(ops) == 0 {
		return nil, -1, fmt.Errorf("no ops")
	}
	allowed := opKeys
	if single {
		allowed = append(slices.Clip(opKeys), "revision")
	}
	for i, o := range ops {
		keys, _, err := members(o)
		if err != nil {
			return nil, i, fmt.Errorf("op %d: %v", i, err)
		}
		if k := unknownKey(keys, allowed); k != "" {
			return nil, i, fmt.Errorf("op %d: unknown key %q; an op has %s", i, k, strings.Join(opKeys, ", "))
		}
		var one EditOp
		if err := json.Unmarshal(o, &one); err != nil {
			return nil, i, fmt.Errorf("op %d: %v", i, err)
		}
		one.Given = keys
		req.Ops = append(req.Ops, one)
	}
	return req, -1, nil
}

// members reads a JSON object's keys in order and its values by key,
// refusing a key that appears twice. Keys match exactly: encoding/json
// alone would take "Expect" for "expect" and keep the last of two "text"s.
func members(raw []byte) ([]string, map[string]json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	t, err := dec.Token()
	if err != nil {
		return nil, nil, err
	}
	if d, ok := t.(json.Delim); !ok || d != '{' {
		return nil, nil, fmt.Errorf("want an object, got %s", bytes.TrimSpace(raw))
	}
	var keys []string
	vals := map[string]json.RawMessage{}
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		k, _ := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if _, dup := vals[k]; dup {
			return nil, nil, fmt.Errorf("key %q appears twice", k)
		}
		keys = append(keys, k)
		vals[k] = v
	}
	if _, err := dec.Token(); err != nil {
		return nil, nil, err
	}
	return keys, vals, nil
}

func unknownKey(keys, allowed []string) string {
	for _, k := range keys {
		if !slices.Contains(allowed, k) {
			return k
		}
	}
	return ""
}

// EditOpts are edit's flags. By default an edit that adds check errors is
// refused; RequireClean also refuses one that leaves any, and AllowBroken
// writes it anyway (a step in a multi-edit change). Show adds each changed
// decl's new source to the result. Revision (--rev) is the request's
// revision guard given on the command line. Force skips every guard.
type EditOpts struct {
	DryRun, RequireClean, AllowBroken, Show, Force bool
	Revision                                       string
}

// Edit applies a batch of ops atomically: all of them or none.
func Edit(dir, src string, o EditOpts, w io.Writer) int {
	var raw []byte
	var err error
	if src == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(src)
	}
	if err != nil {
		return fail(w, "read", err.Error(), "pass a file path, or - to read the edit from stdin")
	}
	req, op, err := parseEditReq(raw)
	if err != nil {
		r := map[string]any{"ok": false, "error": "bad_edit", "message": err.Error() + "; nothing was written",
			"hint": `want {"ops":[{"op":"replace","id":"...","expect":"...","text":"..."}]}; see ovid help edit`}
		if op >= 0 {
			r["op"] = op
		}
		emit(w, r)
		return ExitFail
	}
	return runEdit(dir, req, o, w)
}

// EditOne applies a single op whose text comes from a file or stdin, so a
// shell heredoc can carry the code without JSON escaping.
func EditOne(dir string, op EditOp, textFrom string, o EditOpts, w io.Writer) int {
	if textFrom != "" {
		var raw []byte
		var err error
		if textFrom == "-" {
			raw, err = io.ReadAll(os.Stdin)
		} else {
			raw, err = os.ReadFile(textFrom)
		}
		if err != nil {
			return fail(w, "read", err.Error(), "")
		}
		op.Text = strings.TrimRight(string(raw), "\n")
	}
	return runEdit(dir, &EditReq{Ops: []EditOp{op}}, o, w)
}

func runEdit(dir string, req *EditReq, o EditOpts, w io.Writer) int {
	unlock, code := lockModule(dir, w)
	if unlock == nil {
		return code
	}
	defer unlock()
	m, err := load(dir)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	if o.Revision != "" {
		if req.Revision != "" && req.Revision != o.Revision {
			return fail(w, "bad_edit", "--rev "+o.Revision+" and the request's revision "+req.Revision+" disagree; nothing was written", "give the revision once")
		}
		req.Revision = o.Revision
	}
	if req.Revision != "" && req.Revision != m.Revision() && !o.Force {
		emit(w, map[string]any{"ok": false, "error": "stale", "message": "module revision changed", "revision": m.Revision(),
			"hint": "drop revision and use per-op expect hashes so unrelated edits do not conflict"})
		return ExitStale
	}
	var sps []*splice
	for i, op := range req.Ops {
		s, e := planOp(m, i, op, o.Force, req.Revision != "")
		if e != nil {
			r := map[string]any{"ok": false, "error": e.code, "message": e.msg, "op": i}
			if e.hint != "" {
				r["hint"] = e.hint
			}
			for k, v := range e.extra {
				r[k] = v
			}
			emit(w, r)
			if e.exit != 0 {
				return e.exit
			}
			return ExitFail
		}
		sps = append(sps, s...)
	}
	guard := guardNoWorse
	switch {
	case o.RequireClean:
		guard = guardClean
	case o.AllowBroken:
		guard = guardNone
	}
	return applySplices(w, dir, m, sps, req.Ops, o.DryRun, guard, o.Show, nil)
}

// applySplices applies text splices, reparses, checks, and writes.
func applySplices(w io.Writer, dir string, m *module.Module, sps []*splice, ops []EditOp, dryRun bool, guard int, show bool, extra map[string]any) int {
	loaded := map[string][]byte{}
	for _, f := range m.Files {
		if f.Abs != "" {
			loaded[f.Abs] = f.Src
		}
	}
	byFile := map[string][]*splice{}
	for _, s := range sps {
		byFile[s.abs] = append(byFile[s.abs], s)
	}
	overlay := map[string][]byte{}
	var changed []string
	for abs, list := range byFile {
		sort.SliceStable(list, func(i, j int) bool { return list[i].off < list[j].off })
		for i := 1; i < len(list); i++ {
			if list[i].off < list[i-1].end {
				return fail(w, "overlap", fmt.Sprintf("ops %d and %d touch overlapping source", list[i-1].op, list[i].op),
					"split them into separate edits, or replace the enclosing node once")
			}
		}
		// Splice the bytes the plan was made from, not a fresh read.
		old, known := loaded[abs]
		if !list[0].isNew && !known {
			return fail(w, "read", abs+" is not a file of this module", "")
		}
		var out []byte
		prev := 0
		for _, s := range list {
			out = append(out, old[prev:s.off]...)
			s.newOff = len(out)
			out = append(out, s.text...)
			prev = s.end
		}
		out = append(out, old[prev:]...)
		overlay[abs] = out
		changed = append(changed, abs)
	}
	sort.Strings(changed)
	// Reparse changed files first so a syntax slip names the edited text.
	for _, abs := range changed {
		if _, perr := syntax.ParseFile(0, overlay[abs]); perr != nil {
			f := &module.File{Path: abs, Src: overlay[abs]}
			pos := f.Pos(perr.Off)
			op := -1
			// Blame the last op that wrote at or before the error.
			for _, s := range byFile[abs] {
				if s.newOff <= perr.Off {
					op = s.op
				}
			}
			emit(w, map[string]any{"ok": false, "error": "syntax", "message": perr.Msg, "op": op, "file": rel(m, abs),
				"line": pos.Line, "col": pos.Col, "source": f.Line(pos.Line), "hint": "nothing was written"})
			return ExitFail
		}
	}
	before := runCheck(m)
	nm, err := module.LoadOverlay(dir, overlay)
	if err != nil {
		return fail(w, "load", err.Error(), "")
	}
	after := runCheck(nm)
	added := newDiags(before.diags, after.diags)
	worse := len(added) > 0
	if (guard == guardClean && len(after.diags) > 0) || (guard == guardNoWorse && worse) {
		if guard == guardNoWorse {
			// Only the errors this change introduced.
			(&checked{m: nm, diags: added}).writeDiags(w)
		} else {
			after.writeDiags(w)
		}
		emit(w, map[string]any{"ok": false, "error": "check", "message": "the change leaves check errors; nothing was written",
			"errors": len(after.diags), "errors_before": len(before.diags),
			"hint": "fix the text, or pass --allow-broken to write it anyway"})
		return ExitFail
	}
	if !dryRun {
		// Ovid writers are serialized by the module lock; this catches an
		// editor or script that changed a file while the edit was planned.
		for _, abs := range changed {
			if now, err := os.ReadFile(abs); err == nil && string(now) != string(loaded[abs]) {
				emit(w, map[string]any{"ok": false, "error": "stale", "message": rel(m, abs) + " changed on disk while the edit ran; nothing was written",
					"hint": "run the edit again; ids and hashes are read fresh each time"})
				return ExitStale
			}
		}
		if done, err := module.WriteFiles(overlay, 0); err != nil {
			var files []string
			for _, abs := range done {
				files = append(files, rel(m, abs))
			}
			emit(w, map[string]any{"ok": false, "error": "write", "message": err.Error(), "written_files": files,
				"hint": "files in written_files have the new text and the rest the old; check git status"})
			return ExitFail
		}
	}
	after.writeDiags(w)
	var files []string
	for _, abs := range changed {
		files = append(files, rel(m, abs))
	}
	res := map[string]any{"ok": true, "written": !dryRun, "files": files, "check_ok": len(after.diags) == 0,
		"errors": len(after.diags), "errors_before": len(before.diags), "revision": nm.Revision()}
	if ops != nil {
		res["ops"] = newIDs(nm, sps, len(ops), show)
	}
	for k, v := range extra {
		res[k] = v
	}
	// A dry run asks "would this be fine?": not if it adds errors.
	if dryRun && worse {
		res["ok"] = false
		res["error"] = "check"
		res["message"] = "the change would add check errors"
		emit(w, res)
		return ExitFail
	}
	emit(w, res)
	return ExitOK
}

// newDiags returns the diagnostics in after that before does not have,
// counting duplicates. Two diagnostics are the same error if they agree on
// code, message, expected, got, and the declaration they are in; lines and
// statement numbers are left out because an edit shifts them.
func newDiags(before, after []module.Diag) []module.Diag {
	key := func(d module.Diag) string {
		where := d.File
		if d.ID != "" {
			where = declOfID(d.ID)
		}
		return strings.Join([]string{d.Code, d.Message, d.Expected, d.Got, where}, "\x00")
	}
	have := map[string]int{}
	for _, d := range before {
		have[key(d)]++
	}
	var out []module.Diag
	for _, d := range after {
		k := key(d)
		if have[k] > 0 {
			have[k]--
			continue
		}
		out = append(out, d)
	}
	return out
}

// declOfID is the declaration part of an id: st:app.main:3 -> app.main.
func declOfID(id string) string {
	rest := id
	if i := strings.Index(id, ":"); i >= 0 {
		rest = id[i+1:]
	}
	if i := strings.LastIndex(rest, ":"); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

// checkExpect guards an op on l with the hash the caller read. Every op
// needs a guard: its own expect, the request's revision (rev, already
// checked by the caller), or force. A decl id is a name, but two agents
// replacing the same func would otherwise both get ok, the second
// silently undoing the first.
func checkExpect(m *module.Module, l *module.Loc, expect string, force, rev bool) *editErr {
	positional := l.Kind == "stmt" || l.Kind == "expr"
	if force || (expect == "" && rev) {
		return nil
	}
	if expect == "" {
		if positional {
			return &editErr{code: "expect_required", msg: l.ID + " is a position, so an edit to it needs --expect: the hash of " + l.Decl + " (in the header `ovid show` printed) or of the node",
				hint: "ids after an insert are renumbered; the hash proves the id still means what you read. --rev REV guards with the whole module instead; --force skips the check"}
		}
		return &editErr{code: "expect_required", msg: "an edit to " + l.ID + " needs a guard: --expect with its hash, from `ovid outline`, `ovid show`, or the last edit's receipt",
			hint: "without one, a second agent's edit would silently replace the first. --rev REV guards with the whole module instead; --force skips the check"}
	}
	if expect == m.Hash(l.ID) || (positional && expect == m.Hash(l.Decl)) {
		return nil
	}
	extra := map[string]any{"id": l.ID, "hash": m.Hash(l.ID), "text": m.Text(l.Full)}
	if positional {
		extra["decl"], extra["decl_hash"] = l.Decl, m.Hash(l.Decl)
		// A statement's hash is bound to its decl as it is now, so this is
		// an id and a hash read together but passed apart, not a move.
		for _, id := range m.Order() {
			if o := m.Index()[id]; o.Decl == l.Decl && o.ID != l.ID && m.Hash(id) == expect {
				return &editErr{code: "stale", exit: ExitStale, extra: extra,
					msg:  "that hash belongs to " + id + ", not " + l.ID,
					hint: "edit " + id + " if that is the node you read, or re-read with `ovid show " + l.Decl + "`"}
			}
		}
		return &editErr{code: "stale", exit: ExitStale, extra: extra,
			msg:  l.Decl + " changed since you read " + l.ID + "; a statement's hash covers its whole decl, and st:/ex: ids are renumbered by edits above them",
			hint: "re-read with `ovid show " + l.Decl + "` and use the ids and hashes it prints now; text and hash here are " + l.ID + "'s current ones"}
	}
	return &editErr{code: "stale", msg: l.ID + " changed since you read it", exit: ExitStale, extra: extra,
		hint: "re-read with `ovid show`; text has the current source"}
}

func rel(m *module.Module, abs string) string {
	return m.DisplayPath(&module.File{Abs: abs})
}

// newIDs reports, per op, the outermost ids whose source the op wrote and
// the decls it touched with their new hashes (and source, if show).
func newIDs(m *module.Module, sps []*splice, nops int, show bool) []map[string]any {
	out := make([]map[string]any, nops)
	for i := range out {
		out[i] = map[string]any{"ids": []string{}}
	}
	fileIdx := map[string]int{}
	for i, f := range m.Files {
		fileIdx[f.Abs] = i
	}
	seen := make([]map[string]bool, nops)
	for _, s := range sps {
		fi, ok := fileIdx[s.abs]
		if !ok {
			continue
		}
		// The top-level decl around the splice, so the caller can chain
		// another edit on it without re-reading.
		at := s.newOff + len(s.text)/2
		for _, id := range m.Order() {
			l := m.Index()[id]
			if l.Kind != "func" && l.Kind != "type" && l.Kind != "const" {
				continue
			}
			if l.Span.File != fi || at < l.Full.Off || at > l.Full.End || seen[s.op][id] {
				continue
			}
			if seen[s.op] == nil {
				seen[s.op] = map[string]bool{}
			}
			seen[s.op][id] = true
			d := map[string]any{"id": id, "hash": m.Hash(id)}
			if show {
				d["text"] = m.Text(l.Full)
			}
			prev, _ := out[s.op]["decls"].([]map[string]any)
			out[s.op]["decls"] = append(prev, d)
		}
		text := strings.TrimSpace(s.text)
		if text == "" {
			continue
		}
		lo := s.newOff + strings.Index(s.text, text)
		hi := lo + len(text)
		var ids []string
		for _, id := range m.Order() {
			l := m.Index()[id]
			if l.Span.File != fi || l.Span.Off < lo || l.Span.End > hi {
				continue
			}
			if p := m.Index()[l.Parent]; p != nil && p.Span.File == fi && p.Span.Off >= lo && p.Span.End <= hi {
				continue
			}
			ids = append(ids, id)
		}
		prev := out[s.op]["ids"].([]string)
		out[s.op]["ids"] = append(prev, ids...)
	}
	return out
}

// opFields are the fields each op reads, besides op and expect. A field
// that belongs to another op is refused, not ignored: a replace that names
// a before was meant as an insert, and replacing would drop the code the
// caller meant to keep.
var opFields = map[string][]string{
	"replace": {"id", "text"},
	"delete":  {"id"},
	"insert":  {"before", "after", "text"},
	"append":  {"into", "file", "text"},
}

// strayField is the first field op sets that its kind does not read.
func strayField(op EditOp) *editErr {
	set := []struct {
		k  string
		on bool
	}{{"id", op.ID != ""}, {"before", op.Before != ""}, {"after", op.After != ""},
		{"into", op.Into != ""}, {"file", op.File != ""}, {"text", op.Text != ""}}
	for _, f := range set {
		if !(f.on || slices.Contains(op.Given, f.k)) || slices.Contains(opFields[op.Op], f.k) {
			continue
		}
		hint := map[string]string{
			"id":     map[string]string{"insert": "insert names its node with before or after", "append": "append names its node with into"}[op.Op],
			"before": `to add code next to a node, insert: {"op":"insert","before":ID,"text":T}, or ovid insert --before ID`,
			"after":  `to add code next to a node, insert: {"op":"insert","after":ID,"text":T}, or ovid insert --after ID`,
			"into":   `to add code at the end of a body or package, append: {"op":"append","into":ID,"text":T}, or ovid append ID`,
			"file":   "only append into a package takes a file",
			"text":   "delete removes the node; to change it, replace",
		}[f.k]
		return &editErr{code: "bad_edit", msg: fmt.Sprintf("%s takes no %q; it reads %s", op.Op, f.k, strings.Join(opFields[op.Op], ", ")), hint: hint}
	}
	return nil
}

func planOp(m *module.Module, i int, op EditOp, force, rev bool) ([]*splice, *editErr) {
	if _, ok := opFields[op.Op]; ok {
		if e := strayField(op); e != nil {
			return nil, e
		}
	}
	target := op.ID
	switch op.Op {
	case "insert":
		target = op.Before
		if target == "" {
			target = op.After
		}
		if (op.Before == "") == (op.After == "") {
			return nil, &editErr{code: "bad_edit", msg: "insert needs exactly one of before or after", hint: `{"op":"insert","after":"st:app.main:2","text":"x = x + 1"}`}
		}
	case "append":
		target = op.Into
	case "replace", "delete":
	default:
		return nil, &editErr{code: "bad_edit", msg: "unknown op " + op.Op,
			hint: `ops: {"op":"replace","id":ID,"text":T}, {"op":"delete","id":ID}, {"op":"insert","after":ID,"text":T} (or "before"), {"op":"append","into":ID,"text":T}; or skip JSON: ovid replace|insert|append|delete`}
	}
	if target == "" {
		return nil, &editErr{code: "bad_edit", msg: op.Op + " needs an id"}
	}
	if op.Op == "append" {
		if p := findPkg(m, target); p != nil {
			return appendDecl(m, i, p, op, force)
		}
	}
	l := m.Index()[target]
	if l == nil {
		ls, err := m.Lookup(target)
		if err != nil || len(ls) != 1 {
			msg := "no node " + target
			if err != nil {
				msg = err.Error()
			}
			return nil, &editErr{code: "not_found", msg: msg, hint: "ids change when the source changes; get fresh ones from `ovid show --ids`"}
		}
		l = ls[0]
	}
	if m.IsStd(l.Span.File) {
		return nil, &editErr{code: "std", msg: target + " is in a shipped package", hint: "copy the package into the module to change it"}
	}
	if e := checkExpect(m, l, op.Expect, force, rev); e != nil {
		return nil, e
	}
	f := m.Files[l.Span.File]
	src := f.Src
	ind := indentAt(src, l.Span.Off)
	sp := &splice{op: i, abs: f.Abs}
	block := l.Kind == "stmt" || l.Kind == "func" || l.Kind == "type" || l.Kind == "const" || l.Kind == "import"
	decl := l.Kind == "func" || l.Kind == "type" || l.Kind == "const"
	// A decl's doc comment is part of it (Full): replace swaps it for the
	// new text's, or keeps it when the text brings none, and delete
	// removes it.
	switch op.Op {
	case "replace":
		sp.off, sp.end = l.Full.Off, l.Full.End
		if decl && !startsWithComment(op.Text) {
			sp.off = l.Span.Off
		}
		sp.text = reindent(op.Text, ind, false)
	case "delete":
		sp.off, sp.end = l.Full.Off, l.Full.End
		if block && ownsLines(src, l.Full) {
			sp.off = lineBegin(src, l.Full.Off)
			sp.end = lineAfter(src, l.Full.End)
			if decl && sp.end < len(src) && src[sp.end] == '\n' {
				sp.end++
			}
		}
	case "insert":
		if !block {
			return nil, &editErr{code: "bad_edit", msg: "insert anchors on a statement or declaration; " + target + " is a " + l.Kind,
				hint: "to change part of an expression, replace it"}
		}
		gap := "\n"
		if decl {
			gap = "\n\n"
		}
		if op.Before != "" {
			// Above the anchor's doc comment, which stays with it.
			sp.off = lineBegin(src, l.Full.Off)
			sp.text = reindent(op.Text, ind, true) + gap
		} else {
			sp.off = l.Span.End
			sp.text = gap + reindent(op.Text, ind, true)
		}
		sp.end = sp.off
	case "append":
		closeAt := -1
		switch n := l.Node.(type) {
		case *ir.Func:
			closeAt = l.Span.End - 1
		case *ir.Node:
			if n.Op == "while" || (n.Op == "if" && len(n.Else) == 0) {
				closeAt = l.Span.End - 1
			} else if n.Op == "if" && len(n.Then) > 0 {
				last := n.Then[len(n.Then)-1]
				sp.off, sp.end = last.Span.End, last.Span.End
				sp.text = "\n" + reindent(op.Text, indentAt(src, last.Span.Off), true)
				return []*splice{sp}, nil
			}
		}
		if closeAt < 0 || src[closeAt] != '}' {
			return nil, &editErr{code: "bad_edit", msg: "append into needs a func, a while, an if, or a package path; got " + l.Kind}
		}
		inner := ind + "  "
		if ln := lineBegin(src, closeAt); strings.TrimSpace(string(src[ln:closeAt])) == "" {
			sp.off = ln
			sp.text = reindent(op.Text, inner, true) + "\n"
		} else {
			sp.off = closeAt
			sp.text = "\n" + reindent(op.Text, inner, true) + "\n" + ind
		}
		sp.end = sp.off
	}
	return []*splice{sp}, nil
}

func findPkg(m *module.Module, q string) *ir.Package {
	q = strings.TrimPrefix(q, "pkg:")
	for pi := range m.Prog.Packages {
		if m.Prog.Packages[pi].Path == q {
			return &m.Prog.Packages[pi]
		}
	}
	return nil
}

func appendDecl(m *module.Module, i int, p *ir.Package, op EditOp, force bool) ([]*splice, *editErr) {
	if m.Std[p.Path] {
		return nil, &editErr{code: "std", msg: p.Path + " is a shipped package", hint: "copy it into the module to change it"}
	}
	// The one op that needs no guard: it names no existing node and
	// overwrites no code, and replaying it, or racing another writer that
	// added the same name, is refused by the checker (duplicate_name). A
	// package path has no hash, so an expect here would be ignored; refuse
	// it rather than let the caller think it guarded anything.
	if op.Expect != "" && !force {
		return nil, &editErr{code: "bad_edit", msg: "append into package " + p.Path + " names no node, so it takes no expect",
			hint: "drop expect; to guard against any change to the module, give the request a revision (--rev REV)"}
	}
	text := strings.TrimRight(reindent(op.Text, "", true), "\n")
	if op.File != "" {
		abs := filepath.Join(m.Root, filepath.FromSlash(op.File))
		if filepath.ToSlash(filepath.Dir(op.File)) != p.Path {
			return nil, &editErr{code: "bad_edit", msg: op.File + " is not in package directory " + p.Path}
		}
		if b, err := os.ReadFile(abs); err == nil {
			return []*splice{{op: i, abs: abs, off: len(b), end: len(b), text: sepFor(b) + text + "\n"}}, nil
		}
		return []*splice{{op: i, abs: abs, isNew: true, text: "package " + p.Path + "\n\n" + text + "\n"}}, nil
	}
	f := m.Files[p.Span.File]
	n := len(f.Src)
	return []*splice{{op: i, abs: f.Abs, off: n, end: n, text: sepFor(f.Src) + text + "\n"}}, nil
}

func sepFor(b []byte) string {
	switch {
	case len(b) == 0:
		return ""
	case strings.HasSuffix(string(b), "\n\n"):
		return ""
	case strings.HasSuffix(string(b), "\n"):
		return "\n"
	}
	return "\n\n"
}

func indentAt(src []byte, off int) string {
	b := lineBegin(src, off)
	e := b
	for e < len(src) && (src[e] == ' ' || src[e] == '\t') {
		e++
	}
	return string(src[b:e])
}

func lineBegin(src []byte, off int) int {
	for off > 0 && src[off-1] != '\n' {
		off--
	}
	return off
}

// lineAfter returns the offset just past the newline that ends the line at off.
func lineAfter(src []byte, off int) int {
	for off < len(src) && src[off] != '\n' {
		off++
	}
	if off < len(src) {
		off++
	}
	return off
}

// ownsLines reports whether nothing but blanks shares the span's lines.
// startsWithComment reports whether a decl's new text opens with a //
// comment, which then replaces the old doc comment.
func startsWithComment(text string) bool {
	return strings.HasPrefix(strings.TrimLeft(text, " \t\r\n"), "//")
}

func ownsLines(src []byte, s ir.Span) bool {
	b := lineBegin(src, s.Off)
	if strings.TrimSpace(string(src[b:s.Off])) != "" {
		return false
	}
	e := s.End
	for e < len(src) && src[e] != '\n' {
		e++
	}
	rest := strings.TrimSpace(string(src[s.End:e]))
	return rest == "" || strings.HasPrefix(rest, "//")
}

// reindent strips the text's own indentation and prefixes ind. When first is
// false the first line is left bare (it continues an existing line).
func reindent(text, ind string, first bool) string {
	text = strings.Trim(text, "\n")
	lines := strings.Split(text, "\n")
	lines[0] = strings.TrimLeft(lines[0], " \t")
	common := -1
	for _, ln := range lines[1:] {
		if strings.TrimSpace(ln) == "" {
			continue
		}
		n := len(ln) - len(strings.TrimLeft(ln, " \t"))
		if common < 0 || n < common {
			common = n
		}
	}
	if common < 0 {
		common = 0
	}
	// A block's closing line usually sits at the first line's level.
	for i := range lines {
		if i > 0 {
			if strings.TrimSpace(lines[i]) == "" {
				lines[i] = ""
				continue
			}
			lines[i] = lines[i][min(common, len(lines[i])-len(strings.TrimLeft(lines[i], " \t"))):]
		}
		if i > 0 || first {
			lines[i] = ind + lines[i]
		}
	}
	return strings.Join(lines, "\n")
}
