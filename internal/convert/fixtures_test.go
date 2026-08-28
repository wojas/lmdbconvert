package convert

import (
	"encoding/binary"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

const testMapSize = 64 << 20 // 64 MiB

type fixtureDBI struct {
	name  string
	flags uint
	kvs   [][2][]byte
}

func nativeUint64(v uint64) []byte {
	b := make([]byte, 8)
	binary.NativeEndian.PutUint64(b, v)
	return b
}

// fixtureData returns a deterministic data set covering all DBI flag
// combinations that must round-trip through a conversion.
func fixtureData() []fixtureDBI {
	var dbis []fixtureDBI

	var plain [][2][]byte
	for i := 0; i < 200; i++ {
		plain = append(plain, [2][]byte{
			[]byte(fmt.Sprintf("key-%04d", i)),
			[]byte(fmt.Sprintf("val-%d", i)),
		})
	}
	dbis = append(dbis, fixtureDBI{"plain", 0, plain})

	var dups [][2][]byte
	for i := 0; i < 20; i++ {
		for j := 0; j <= i%5; j++ {
			dups = append(dups, [2][]byte{
				[]byte(fmt.Sprintf("dup-%02d", i)),
				[]byte(fmt.Sprintf("val-%02d-%02d", i, j)),
			})
		}
	}
	dbis = append(dbis, fixtureDBI{"dups", lmdb.DupSort, dups})

	var dupfixed [][2][]byte
	for i := 0; i < 10; i++ {
		for j := 0; j < 4; j++ {
			dupfixed = append(dupfixed, [2][]byte{
				[]byte(fmt.Sprintf("df-%02d", i)),
				nativeUint64(uint64(i*100 + j)),
			})
		}
	}
	dbis = append(dbis, fixtureDBI{"dupfixed", lmdb.DupSort | lmdb.DupFixed, dupfixed})

	var intkey [][2][]byte
	for i := 0; i < 50; i++ {
		intkey = append(intkey, [2][]byte{
			nativeUint64(uint64(i * 7)),
			[]byte(fmt.Sprintf("int-%d", i)),
		})
	}
	dbis = append(dbis, fixtureDBI{"intkey", lmdb.IntegerKey, intkey})

	var revkey [][2][]byte
	for i := 0; i < 30; i++ {
		revkey = append(revkey, [2][]byte{
			[]byte(fmt.Sprintf("rk-%03d", i)),
			[]byte(fmt.Sprintf("rv-%d", i)),
		})
	}
	dbis = append(dbis, fixtureDBI{"revkey", lmdb.ReverseKey, revkey})

	var intdup [][2][]byte
	for i := 0; i < 10; i++ {
		for j := 0; j < 3; j++ {
			intdup = append(intdup, [2][]byte{
				[]byte(fmt.Sprintf("id-%02d", i)),
				nativeUint64(uint64(i*10 + j)),
			})
		}
	}
	dbis = append(dbis, fixtureDBI{"intdup", lmdb.DupSort | lmdb.IntegerDup, intdup})

	dbis = append(dbis, fixtureDBI{"empty", lmdb.DupSort, nil})

	return dbis
}

func openTestEnv(t *testing.T, path string, ver lmdb.Format, noSubdir, readonly bool) *lmdb.Env {
	t.Helper()
	env, err := lmdb.NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := env.SetFormat(ver); err != nil {
		t.Fatal(err)
	}
	if err := env.SetMaxDBs(64); err != nil {
		t.Fatal(err)
	}
	mapSize := int64(testMapSize)
	if readonly {
		mapSize = 0
	}
	if err := env.SetMapSize(mapSize); err != nil {
		t.Fatal(err)
	}
	var flags uint
	if noSubdir {
		flags |= lmdb.NoSubdir
	} else if err := os.MkdirAll(path, 0o755); err != nil {
		t.Fatal(err)
	}
	if readonly {
		flags |= lmdb.Readonly
	}
	if err := env.Open(path, flags, 0o644); err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	return env
}

// buildFixture creates a database with the fixtureData content at path and
// raises its last transaction ID to txnID.
func buildFixture(t *testing.T, path string, ver lmdb.Format, noSubdir bool, txnID int64) {
	t.Helper()
	env := openTestEnv(t, path, ver, noSubdir, false)
	defer env.Close()

	err := env.Update(func(txn *lmdb.Txn) error {
		for _, f := range fixtureData() {
			dbi, err := txn.OpenDBI(f.name, f.flags|lmdb.Create)
			if err != nil {
				return fmt.Errorf("create %q: %w", f.name, err)
			}
			for _, kv := range f.kvs {
				if err := txn.Put(dbi, kv[0], kv[1], 0); err != nil {
					return fmt.Errorf("put %q: %w", f.name, err)
				}
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	bumpTxnID(t, env, txnID)
}

// bumpTxnID commits Put+Del transactions until the environment's last
// transaction ID reaches target.
func bumpTxnID(t *testing.T, env *lmdb.Env, target int64) {
	t.Helper()
	info, err := env.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.LastTxnID > target {
		t.Fatalf("fixture transaction ID %d already beyond target %d", info.LastTxnID, target)
	}
	for i := info.LastTxnID; i < target; i++ {
		err := env.Update(func(txn *lmdb.Txn) error {
			root, err := txn.OpenRoot(0)
			if err != nil {
				return err
			}
			if err := txn.Put(root, []byte("\x00bump"), []byte{}, 0); err != nil {
				return err
			}
			return txn.Del(root, []byte("\x00bump"), nil)
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	info, err = env.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.LastTxnID != target {
		t.Fatalf("bumpTxnID: got %d, want %d", info.LastTxnID, target)
	}
}

// checkConverted verifies that the database at dstPath has the expected
// format and exactly the same DBIs, flags, data and transaction ID as the
// database at srcPath.
func checkConverted(t *testing.T, srcPath, dstPath string, noSubdir bool, srcVer, dstVer lmdb.Format) {
	t.Helper()
	var sniffFlags uint
	if noSubdir {
		sniffFlags = lmdb.NoSubdir
	}
	gotVer, err := lmdb.SniffFormat(dstPath, sniffFlags)
	if err != nil {
		t.Fatal(err)
	}
	if gotVer != dstVer {
		t.Fatalf("destination format = %s, want %s", gotVer, dstVer)
	}

	srcEnv := openTestEnv(t, srcPath, srcVer, noSubdir, true)
	defer srcEnv.Close()
	dstEnv := openTestEnv(t, dstPath, dstVer, noSubdir, true)
	defer dstEnv.Close()

	srcInfo, err := srcEnv.Info()
	if err != nil {
		t.Fatal(err)
	}
	dstInfo, err := dstEnv.Info()
	if err != nil {
		t.Fatal(err)
	}
	if srcInfo.LastTxnID != dstInfo.LastTxnID {
		t.Errorf("LastTxnID: destination %d, source %d", dstInfo.LastTxnID, srcInfo.LastTxnID)
	}

	err = srcEnv.View(func(srcTxn *lmdb.Txn) error {
		return dstEnv.View(func(dstTxn *lmdb.Txn) error {
			srcDBIs, err := enumerateDBIs(srcTxn, srcVer, 64)
			if err != nil {
				return fmt.Errorf("enumerate source: %w", err)
			}
			dstDBIs, err := enumerateDBIs(dstTxn, dstVer, 64)
			if err != nil {
				return fmt.Errorf("enumerate destination: %w", err)
			}
			if len(srcDBIs) != len(dstDBIs) {
				t.Fatalf("destination has %d DBIs, source %d", len(dstDBIs), len(srcDBIs))
			}
			for i, sdb := range srcDBIs {
				ddb := dstDBIs[i]
				if sdb.Name != ddb.Name {
					t.Fatalf("DBI %d: name %q != %q", i, ddb.Name, sdb.Name)
				}
				if sdb.Flags != ddb.Flags {
					t.Errorf("DBI %q: flags %s != %s", sdb.Name, flagString(ddb.Flags), flagString(sdb.Flags))
				}
				if sdb.Entries != ddb.Entries {
					t.Errorf("DBI %q: entries %d != %d", sdb.Name, ddb.Entries, sdb.Entries)
				}
				db := &dbInfo{Name: sdb.Name, src: sdb.src, dst: ddb.src}
				if err := verifyDBI(srcTxn, dstTxn, db, nopPhase{}); err != nil {
					t.Error(err)
				}
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
}

type nopPhase struct{}

func (nopPhase) Add(int64) {}

func fixturePaths(t *testing.T) (srcPath, dstPath string) {
	dir := t.TempDir()
	return filepath.Join(dir, "src"), filepath.Join(dir, "dst")
}

// TestBuildSampleEnv is a manual helper, not a real test: it builds a
// fixture database at LMDBCONVERT_SAMPLE_OUT for end-to-end testing of
// the CLI, e.g.:
//
//	LMDBCONVERT_SAMPLE_OUT=/tmp/src09 go test ./internal/convert -run TestBuildSampleEnv
func TestBuildSampleEnv(t *testing.T) {
	path := os.Getenv("LMDBCONVERT_SAMPLE_OUT")
	if path == "" {
		t.Skip("set LMDBCONVERT_SAMPLE_OUT to build a sample database")
	}
	ver := lmdb.V09
	if os.Getenv("LMDBCONVERT_SAMPLE_VERSION") == "1.0" {
		ver = lmdb.V10
	}
	buildFixture(t, path, ver, false, 57)
	t.Logf("built sample %s database at %s", ver, path)
}
