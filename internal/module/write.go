package module

import (
	"os"
	"path/filepath"
	"sort"
)

// WriteFiles replaces or creates several files as close to all-or-nothing
// as a filesystem allows, which is not all-or-nothing. Every new content
// goes to its own temp file in the target's directory and is fsynced; a
// failure there changes no file. Only when all of them are written are
// they renamed into place, one by one, in path order, and then each
// directory is fsynced. If a rename fails, written lists the files already
// replaced, and the rest are as they were: an existing file with its old
// text, a new one still absent. If a directory sync fails, written lists
// every file, all with their new text, whose rename may not yet be
// durable. Temp files not renamed are removed (a process killed between
// renames leaves them, and the same partial state, behind). Mode 0 keeps
// an existing file's mode (0644 for a new one).
func WriteFiles(files map[string][]byte, mode os.FileMode) (written []string, err error) {
	var paths []string
	for p := range files {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	tmps := map[string]string{}
	defer func() {
		for _, t := range tmps {
			os.Remove(t)
		}
	}()
	for _, p := range paths {
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return nil, err
		}
		t, err := writeTemp(p, files[p], mode, true)
		if err != nil {
			return nil, err
		}
		tmps[p] = t
	}
	dirs := map[string]bool{}
	for _, p := range paths {
		if err := renameFile(tmps[p], p); err != nil {
			return written, err
		}
		delete(tmps, p)
		written = append(written, p)
		dirs[filepath.Dir(p)] = true
	}
	for d := range dirs {
		if err := syncDirOf(d); err != nil {
			return written, err
		}
	}
	return written, nil
}

// renameFile and syncDirOf are WriteFiles's steps that can fail after a
// file has changed; tests replace them to fail on purpose.
var (
	renameFile = os.Rename
	syncDirOf  = syncDir
)

// ReplaceFile replaces or creates p in one step, through a temp file in its
// directory and a rename, so a reader sees the old file or the new one and
// a program running from p can be replaced. Nothing is synced: it is for a
// file that can be made again, like a build's output, where WriteFiles is
// for source. Mode 0 means what it does to WriteFiles.
func ReplaceFile(p string, src []byte, mode os.FileMode) error {
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	t, err := writeTemp(p, src, mode, false)
	if err != nil {
		return err
	}
	if err := os.Rename(t, p); err != nil {
		os.Remove(t)
		return err
	}
	return nil
}

// writeTemp writes src to a fresh temp file next to p, and syncs it if sync.
func writeTemp(p string, src []byte, mode os.FileMode, sync bool) (string, error) {
	if mode == 0 {
		mode = 0o644
		if st, err := os.Stat(p); err == nil {
			mode = st.Mode().Perm()
		}
	}
	f, err := os.CreateTemp(filepath.Dir(p), "."+filepath.Base(p)+".*.ovid-tmp")
	if err != nil {
		return "", err
	}
	_, err = f.Write(src)
	if err == nil {
		err = f.Chmod(mode)
	}
	if err == nil && sync {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.Name())
		return "", err
	}
	return f.Name(), nil
}

func syncDir(d string) error {
	f, err := os.Open(d)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}
