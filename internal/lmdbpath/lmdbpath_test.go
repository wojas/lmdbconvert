package lmdbpath

import (
	"os"
	"path/filepath"
	"testing"
)

func TestResolveExisting(t *testing.T) {
	dir := t.TempDir()

	subdirDB := filepath.Join(dir, "subdb")
	if err := os.MkdirAll(subdirDB, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(subdirDB, DataFileName), []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	emptyDir := filepath.Join(dir, "empty")
	if err := os.MkdirAll(emptyDir, 0o755); err != nil {
		t.Fatal(err)
	}
	plainFile := filepath.Join(dir, "db.mdb")
	if err := os.WriteFile(plainFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name    string
		path    string
		want    Path
		wantErr bool
	}{
		{"dir with data.mdb", subdirDB, Path{EnvPath: subdirDB, Exists: true}, false},
		{"dir without data.mdb", emptyDir, Path{EnvPath: emptyDir}, false},
		{"data.mdb file", filepath.Join(subdirDB, DataFileName), Path{EnvPath: subdirDB, Exists: true}, false},
		{"plain file", plainFile, Path{EnvPath: plainFile, NoSubdir: true, Exists: true}, false},
		{"missing", filepath.Join(dir, "nope"), Path{}, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := ResolveExisting(c.path)
			if c.wantErr {
				if err == nil {
					t.Fatal("expected error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got != c.want {
				t.Errorf("got %+v, want %+v", got, c.want)
			}
		})
	}
}

func TestResolveNew(t *testing.T) {
	dir := t.TempDir()
	existingDir := filepath.Join(dir, "existing")
	if err := os.MkdirAll(existingDir, 0o755); err != nil {
		t.Fatal(err)
	}
	existingFile := filepath.Join(dir, "existing.mdb")
	if err := os.WriteFile(existingFile, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}

	t.Run("new subdir path", func(t *testing.T) {
		p, err := ResolveNew(filepath.Join(dir, "newdb"), false)
		if err != nil {
			t.Fatal(err)
		}
		want := Path{EnvPath: filepath.Join(dir, "newdb")}
		if p != want {
			t.Errorf("got %+v, want %+v", p, want)
		}
	})
	t.Run("data.mdb resolves to parent", func(t *testing.T) {
		p, err := ResolveNew(filepath.Join(dir, "newdb2", DataFileName), true)
		if err != nil {
			t.Fatal(err)
		}
		want := Path{EnvPath: filepath.Join(dir, "newdb2")}
		if p != want {
			t.Errorf("got %+v, want %+v", p, want)
		}
	})
	t.Run("nosubdir file", func(t *testing.T) {
		p, err := ResolveNew(filepath.Join(dir, "new.mdb"), true)
		if err != nil {
			t.Fatal(err)
		}
		want := Path{EnvPath: filepath.Join(dir, "new.mdb"), NoSubdir: true}
		if p != want {
			t.Errorf("got %+v, want %+v", p, want)
		}
	})
	t.Run("existing file for subdir mode", func(t *testing.T) {
		if _, err := ResolveNew(existingFile, false); err == nil {
			t.Error("expected error for file destination in subdir mode")
		}
	})
	t.Run("existing dir for nosubdir mode", func(t *testing.T) {
		if _, err := ResolveNew(existingDir, true); err == nil {
			t.Error("expected error for directory destination in NoSubdir mode")
		}
	})
}

func TestDataLockFile(t *testing.T) {
	p := Path{EnvPath: "/x/db"}
	if got := p.DataFile(); got != "/x/db/data.mdb" {
		t.Errorf("DataFile = %q", got)
	}
	if got := p.LockFile(); got != "/x/db/lock.mdb" {
		t.Errorf("LockFile = %q", got)
	}
	p = Path{EnvPath: "/x/db.mdb", NoSubdir: true}
	if got := p.DataFile(); got != "/x/db.mdb" {
		t.Errorf("DataFile = %q", got)
	}
	if got := p.LockFile(); got != "/x/db.mdb-lock" {
		t.Errorf("LockFile = %q", got)
	}
}
