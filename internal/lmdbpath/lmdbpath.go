// Package lmdbpath resolves user-provided LMDB database paths into the
// path and flags needed to open them, supporting both subdir-style
// environments (a directory containing data.mdb) and NoSubdir
// environments (a plain file).
package lmdbpath

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// DataFileName is the data file inside a subdir-style environment.
const DataFileName = "data.mdb"

// Path describes how to open an LMDB environment.
type Path struct {
	EnvPath  string // path to pass to Env.Open
	NoSubdir bool   // whether lmdb.NoSubdir is needed
	Exists   bool   // whether the data file already exists
}

// DataFile returns the path of the environment's data file.
func (p Path) DataFile() string {
	if p.NoSubdir {
		return p.EnvPath
	}
	return filepath.Join(p.EnvPath, DataFileName)
}

// LockFile returns the path of the environment's lock file.
func (p Path) LockFile() string {
	if p.NoSubdir {
		return p.EnvPath + "-lock"
	}
	return filepath.Join(p.EnvPath, "lock.mdb")
}

// ResolveExisting inspects a path that must point at an existing LMDB
// database: a directory (containing data.mdb), the data.mdb file itself,
// or a plain file (NoSubdir mode).
func ResolveExisting(path string) (Path, error) {
	st, err := os.Stat(path)
	if err != nil {
		return Path{}, err
	}
	if st.IsDir() {
		p := Path{EnvPath: filepath.Clean(path)}
		_, err := os.Stat(p.DataFile())
		p.Exists = err == nil
		return p, nil
	}
	if filepath.Base(path) == DataFileName {
		return Path{EnvPath: filepath.Dir(path), Exists: true}, nil
	}
	return Path{EnvPath: filepath.Clean(path), NoSubdir: true, Exists: true}, nil
}

// ResolveNew resolves a destination path that will be created in the given
// mode. A path ending in a separator or in data.mdb always resolves to
// subdir mode. Exists reports whether a data file is already present at
// the location.
func ResolveNew(path string, noSubdir bool) (Path, error) {
	if strings.HasSuffix(path, string(os.PathSeparator)) {
		noSubdir = false
	}
	path = filepath.Clean(path)
	if filepath.Base(path) == DataFileName {
		path = filepath.Dir(path)
		noSubdir = false
	}
	p := Path{EnvPath: path, NoSubdir: noSubdir}
	if st, err := os.Stat(path); err == nil {
		if noSubdir && st.IsDir() {
			return Path{}, fmt.Errorf("destination %s is a directory, but the source is a NoSubdir database file", path)
		}
		if !noSubdir && !st.IsDir() {
			return Path{}, fmt.Errorf("destination %s is a file, but the source is a directory-style database", path)
		}
	}
	_, err := os.Stat(p.DataFile())
	p.Exists = err == nil
	return p, nil
}
