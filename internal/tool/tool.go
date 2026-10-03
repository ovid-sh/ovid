package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"ovid/internal/check"
	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/patch"
	"ovid/internal/proj"
	"ovid/internal/query"
)

func Check(dir string, w io.Writer) int {
	file, prog, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	lines, nerr := check.Run(prog, dir, file)
	for _, ln := range lines {
		fmt.Fprintf(w, "%s\n", ln)
	}
	if nerr > 0 {
		return 1
	}
	return 0
}

func Query(dir, id, name, pkg, kind string, w io.Writer) int {
	file, prog, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	_, computed, err := ir.Hash(file)
	if err != nil {
		writeErr(w, "revision", err.Error())
		return 1
	}
	res := query.Run(prog, computed, id, name, pkg, kind)
	w.Write(query.Marshal(res))
	return 0
}

func Patch(dir, patchPath string, w io.Writer) int {
	file, prog, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	_, computed, err := ir.Hash(file)
	if err != nil {
		writeErr(w, "revision", err.Error())
		return 1
	}
	raw, err := os.ReadFile(patchPath)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	updated, resp := patch.Apply(prog, computed, raw)
	if !resp.Ok {
		w.Write(patch.Marshal(resp))
		if resp.Error == "stale_patch" {
			return 2
		}
		return 1
	}
	if err := WriteModule(dir, updated, ""); err != nil {
		writeErr(w, "write", err.Error())
		return 1
	}
	b, _, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	_, rev, err := ir.Hash(b)
	if err != nil {
		writeErr(w, "revision", err.Error())
		return 1
	}
	resp.Revision = rev
	w.Write(patch.Marshal(resp))
	return 0
}

func Build(dir, out string, w io.Writer) int {
	file, prog, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	lines, nerr := check.Run(prog, dir, file)
	if nerr > 0 {
		for _, ln := range lines {
			fmt.Fprintf(w, "%s\n", ln)
		}
		return 1
	}
	bin, err := compile.Compile(prog)
	if err != nil {
		writeErr(w, "compile", err.Error())
		return 1
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil && filepath.Dir(out) != "." {
		writeErr(w, "write", err.Error())
		return 1
	}
	if err := os.WriteFile(out, bin, 0o755); err != nil {
		writeErr(w, "write", err.Error())
		return 1
	}
	writeOK(w, map[string]any{"ok": true, "output": out, "bytes": len(bin)})
	return 0
}

// WriteModule writes ovid.json (stamped), PROJECTION, and one directory per package.
// projection is used verbatim when non-empty; otherwise it is rendered from p.
func WriteModule(dir string, p *ir.Program, projection string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.Revision = ir.RevZeros
	raw, err := ir.Marshal(p)
	if err != nil {
		return err
	}
	stamped, err := ir.Stamp(raw)
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "ovid.json"), stamped, 0o644); err != nil {
		return err
	}
	stored, computed, err := ir.Hash(stamped)
	if err != nil {
		return err
	}
	if stored != computed {
		return fmt.Errorf("revision stamp mismatch")
	}
	p.Revision = computed
	if projection == "" {
		projection = proj.Text(p)
	}
	if err := os.WriteFile(filepath.Join(dir, "PROJECTION"), []byte(projection), 0o644); err != nil {
		return err
	}
	for _, pkg := range p.Packages {
		d := filepath.Join(dir, filepath.FromSlash(pkg.Path))
		if err := os.MkdirAll(d, 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(d, "PACKAGE"), []byte(pkg.Path+"\n"), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func writeErr(w io.Writer, code, detail string) {
	writeOK(w, map[string]any{"ok": false, "error": code, "detail": detail})
}

func writeOK(w io.Writer, v any) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	w.Write(buf.Bytes())
}
