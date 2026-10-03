package tool

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"syscall"

	"ovid/internal/check"
	"ovid/internal/compile"
	"ovid/internal/ir"
	"ovid/internal/patch"
	"ovid/internal/proj"
	"ovid/internal/query"
)

func Check(dir string, w io.Writer) int {
	return CheckOptions(dir, false, w)
}

func CheckOptions(dir string, facts bool, w io.Writer) int {
	file, prog, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	lines, nerr := check.Run(prog, dir, file)
	for _, ln := range lines {
		var fact struct {
			Fact string `json:"fact"`
		}
		_ = json.Unmarshal(ln, &fact)
		if facts || fact.Fact == "error" || fact.Fact == "summary" {
			fmt.Fprintf(w, "%s\n", ln)
		}
	}
	if nerr > 0 {
		return 1
	}
	return 0
}

func Query(dir, id, name, pkg, kind string, w io.Writer) int {
	return QueryOptions(dir, query.Options{ID: id, Name: name, Pkg: pkg, Kind: kind, Limit: query.DefaultLimit}, w)
}

func QueryOptions(dir string, opts query.Options, w io.Writer) int {
	file, prog, err := ir.ReadFile(dir)
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	stored, computed, err := ir.Hash(file)
	if err != nil {
		writeErr(w, "revision", err.Error())
		return 1
	}
	if stored != computed {
		writeErr(w, "revision_mismatch", "stored revision does not match file bytes")
		return 1
	}
	res := query.RunOptions(prog, computed, opts)
	w.Write(query.Marshal(res))
	return 0
}

func Patch(dir, patchPath string, w io.Writer) int {
	var raw []byte
	var err error
	if patchPath == "-" {
		raw, err = io.ReadAll(os.Stdin)
	} else {
		raw, err = os.ReadFile(patchPath)
	}
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	lock, err := LockModule(dir)
	if err != nil {
		writeErr(w, "lock", err.Error())
		return 1
	}
	defer lock.Close()
	file, err := os.ReadFile(filepath.Join(dir, "ovid.json"))
	if err != nil {
		writeErr(w, "read", err.Error())
		return 1
	}
	prog, err := ir.Unmarshal(file)
	if err != nil {
		writeErr(w, "parse", err.Error())
		return 1
	}
	stored, computed, err := ir.Hash(file)
	if err != nil {
		writeErr(w, "revision", err.Error())
		return 1
	}
	if stored != computed {
		writeErr(w, "revision_mismatch", "stored revision does not match file bytes")
		return 1
	}
	if err := ir.ValidateStructure(file); err != nil {
		writeErr(w, "bad_program", err.Error())
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
	rev, err := writeProgram(dir, updated)
	if err != nil {
		writeErr(w, "write", err.Error())
		return 1
	}
	resp.Revision = rev
	w.Write(patch.Marshal(resp))
	return 0
}

// The lock file is never renamed or removed. Both implementations use Linux
// flock on this inode, covering the entire read/compare/replace transaction.
func LockModule(dir string) (*os.File, error) {
	f, err := os.OpenFile(filepath.Join(dir, ".ovid.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX)
		if err != syscall.EINTR {
			break
		}
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

func writeProgram(dir string, p *ir.Program) (string, error) {
	p.Revision = ir.RevZeros
	raw, err := ir.Marshal(p)
	if err != nil {
		return "", err
	}
	stamped, err := ir.Stamp(raw)
	if err != nil {
		return "", err
	}
	_, revision, err := ir.Hash(stamped)
	if err != nil {
		return "", err
	}
	if err := atomicModuleFile(dir, stamped); err != nil {
		return "", err
	}
	p.Revision = revision
	return revision, nil
}

func atomicModuleFile(dir string, data []byte) error {
	f, err := os.CreateTemp(dir, ".ovid.json-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	defer f.Close()
	mode := os.FileMode(0o644)
	if st, err := os.Stat(filepath.Join(dir, "ovid.json")); err == nil {
		mode = st.Mode().Perm()
	}
	if err := f.Chmod(mode); err != nil {
		return err
	}
	if _, err := f.Write(data); err != nil {
		return err
	}
	if err := f.Sync(); err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	d, err := os.Open(dir)
	if err != nil {
		return err
	}
	defer d.Close()
	if err := os.Rename(f.Name(), filepath.Join(dir, "ovid.json")); err != nil {
		return err
	}
	return d.Sync()
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
			var fact struct {
				Fact string `json:"fact"`
			}
			_ = json.Unmarshal(ln, &fact)
			if fact.Fact == "error" {
				fmt.Fprintf(w, "%s\n", ln)
			}
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
	lock, err := LockModule(dir)
	if err != nil {
		return err
	}
	defer lock.Close()
	return WriteModuleLocked(dir, p, projection)
}

// WriteModuleLocked is for import transactions that already hold LockModule.
func WriteModuleLocked(dir string, p *ir.Program, projection string) error {
	if _, err := writeProgram(dir, p); err != nil {
		return err
	}
	if projection == "" {
		projection = proj.Text(p)
	} else {
		projection = "// revision " + p.Revision + "\n" + projection
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

// lockModule is the package-local spelling used by init.
func lockModule(dir string) (*os.File, error) { return LockModule(dir) }
