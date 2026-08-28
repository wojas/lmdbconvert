package convert

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// mustFail runs a conversion that must return an error containing want,
// and asserts the destination was never created.
func mustFail(t *testing.T, opts Options, want string) {
	t.Helper()
	_, err := Run(opts)
	if err == nil {
		t.Fatalf("expected error containing %q, got nil", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("error %q does not contain %q", err, want)
	}
	if opts.Dest != "" {
		if _, serr := os.Stat(opts.Dest); !os.IsNotExist(serr) {
			t.Errorf("destination %s was created despite the error", opts.Dest)
		}
	}
}

func TestConvert_MissingSource(t *testing.T) {
	dir := t.TempDir()
	mustFail(t, Options{
		Source:        filepath.Join(dir, "nonexistent"),
		Dest:          filepath.Join(dir, "dst"),
		TargetVersion: lmdb.V10,
	}, "no such file")
}

func TestConvert_EmptySourceDir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	if err := os.MkdirAll(src, 0o755); err != nil {
		t.Fatal(err)
	}
	mustFail(t, Options{
		Source:        src,
		Dest:          filepath.Join(dir, "dst"),
		TargetVersion: lmdb.V10,
	}, "does not exist or is empty")
}

func TestConvert_NonLMDBSource(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mdb")
	if err := os.WriteFile(src, bytes.Repeat([]byte("junk data! "), 1000), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := Run(Options{
		Source:        src,
		Dest:          filepath.Join(dir, "dst.mdb"),
		TargetVersion: lmdb.V10,
	})
	if err == nil {
		t.Fatal("expected error for non-LMDB source")
	}
}

func TestConvert_DirectionErrors(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	mustFail(t, Options{Source: src, Dest: dst},
		"no target version specified")
	mustFail(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10, SameVersion: true},
		"mutually exclusive")
	mustFail(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V09},
		"already in the 0.9 format")
}

func TestConvert_ExistingDest(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)
	buildFixture(t, dst, lmdb.V10, false, 3)

	_, err := Run(Options{Source: src, Dest: dst, TargetVersion: lmdb.V10})
	if err == nil || !strings.Contains(err.Error(), "already exists") {
		t.Fatalf("expected 'already exists' error, got %v", err)
	}
}

func TestConvert_ForceRefusesNonLMDBDest(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)
	if err := os.MkdirAll(dst, 0o755); err != nil {
		t.Fatal(err)
	}
	dataFile := filepath.Join(dst, "data.mdb")
	if err := os.WriteFile(dataFile, bytes.Repeat([]byte("precious "), 1000), 0o644); err != nil {
		t.Fatal(err)
	}

	_, err := Run(Options{Source: src, Dest: dst, TargetVersion: lmdb.V10, Force: true})
	if err == nil || !strings.Contains(err.Error(), "refusing to remove") {
		t.Fatalf("expected 'refusing to remove' error, got %v", err)
	}
	if _, err := os.Stat(dataFile); err != nil {
		t.Errorf("non-LMDB destination file was removed: %v", err)
	}
}

func TestConvert_DowngradeOversizedKey(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	env := openTestEnv(t, src, lmdb.V10, false, false)
	err := env.Update(func(txn *lmdb.Txn) error {
		dbi, err := txn.OpenDBI("big", lmdb.Create)
		if err != nil {
			return err
		}
		return txn.Put(dbi, bytes.Repeat([]byte("k"), 600), []byte("val"), 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Close()

	mustFail(t, Options{
		Source:        src,
		Dest:          filepath.Join(dir, "dst"),
		TargetVersion: lmdb.V09,
	}, "exceed the 0.9 size limits")
}

func TestConvert_DowngradeOversizedDupValue(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	env := openTestEnv(t, src, lmdb.V10, false, false)
	err := env.Update(func(txn *lmdb.Txn) error {
		dbi, err := txn.OpenDBI("bigdup", lmdb.Create|lmdb.DupSort)
		if err != nil {
			return err
		}
		return txn.Put(dbi, []byte("key"), bytes.Repeat([]byte("v"), 600), 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Close()

	mustFail(t, Options{
		Source:        src,
		Dest:          filepath.Join(dir, "dst"),
		TargetVersion: lmdb.V09,
	}, "exceed the 0.9 size limits")
}

func TestConvert_JunkInRootDBI(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src")
	env := openTestEnv(t, src, lmdb.V09, false, false)
	err := env.Update(func(txn *lmdb.Txn) error {
		if _, err := txn.OpenDBI("real", lmdb.Create); err != nil {
			return err
		}
		root, err := txn.OpenRoot(0)
		if err != nil {
			return err
		}
		return txn.Put(root, []byte("loose-key"), []byte("0123456789"), 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Close()

	mustFail(t, Options{
		Source:        src,
		Dest:          filepath.Join(dir, "dst"),
		TargetVersion: lmdb.V10,
	}, "not a named database reference")
}

func TestConvert_TooManyDBIs(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	mustFail(t, Options{
		Source:        src,
		Dest:          dst,
		TargetVersion: lmdb.V10,
		MaxDBs:        2,
	}, "--max-dbs")
}

func TestConvert_MapSizeTooSmall(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	mustFail(t, Options{
		Source:        src,
		Dest:          dst,
		TargetVersion: lmdb.V10,
		MapSize:       4096, // one page; far below what the source uses
	}, "smaller than")
}

func TestDirection(t *testing.T) {
	cases := []struct {
		src     lmdb.LMDBVersion
		target  lmdb.LMDBVersion
		same    bool
		want    lmdb.LMDBVersion
		wantErr bool
	}{
		{src: lmdb.V09, target: lmdb.V10, want: lmdb.V10},
		{src: lmdb.V10, target: lmdb.V09, want: lmdb.V09},
		{src: lmdb.V09, same: true, want: lmdb.V09},
		{src: lmdb.V10, same: true, want: lmdb.V10},
		{src: lmdb.V09, target: lmdb.V09, wantErr: true},
		{src: lmdb.V10, target: lmdb.V10, wantErr: true},
		{src: lmdb.V09, wantErr: true},
		{src: lmdb.V09, target: lmdb.V10, same: true, wantErr: true},
	}
	for _, c := range cases {
		name := fmt.Sprintf("src=%s target=%s same=%v", c.src, c.target, c.same)
		got, err := direction(c.src, Options{TargetVersion: c.target, SameVersion: c.same})
		if c.wantErr {
			if err == nil {
				t.Errorf("%s: expected error", name)
			}
			continue
		}
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		if got != c.want {
			t.Errorf("%s: got %s, want %s", name, got, c.want)
		}
	}
}
