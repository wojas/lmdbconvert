package convert

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// TestRestoreTxnIDParity verifies every current/target parity relationship in
// both on-disk formats, including a large jump that must remain constant-time.
func TestRestoreTxnIDParity(t *testing.T) {
	cases := []struct {
		name            string
		current, target int64
	}{
		{name: "odd current to even target", current: 1, target: 6},
		{name: "odd current to odd target", current: 1, target: 5},
		{name: "even current to odd target", current: 2, target: 5},
		{name: "even current to even target", current: 2, target: 6},
		{name: "large jump", current: 1, target: 1_000_000},
	}

	for _, format := range []lmdb.Format{lmdb.V09, lmdb.V10} {
		for _, tc := range cases {
			t.Run(fmt.Sprintf("%s/%s", format, tc.name), func(t *testing.T) {
				path := filepath.Join(t.TempDir(), "env")
				buildFixture(t, path, format, false, tc.current)
				pageSize := fixturePageSize(t, path, format)

				if err := restoreTxnID(filepath.Join(path, "data.mdb"), format,
					pageSize, tc.current, tc.target); err != nil {
					t.Fatalf("restoreTxnID: %v", err)
				}

				assertMetaTxnIDs(t, filepath.Join(path, "data.mdb"), format,
					pageSize, tc.target)
				assertRestoredEnvironment(t, path, format, tc.target)
			})
		}
	}
}

// fixturePageSize opens an existing fixture and returns its LMDB page size.
func fixturePageSize(t *testing.T, path string, format lmdb.Format) int {
	t.Helper()
	env := openTestEnv(t, path, format, false, true)
	stat, err := env.Stat()
	if err != nil {
		env.Close()
		t.Fatal(err)
	}
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}
	return int(stat.PSize)
}

// assertMetaTxnIDs verifies that the raw metapages contain the normal
// target-1/target pair in parity-selected slots.
func assertMetaTxnIDs(t *testing.T, path string, format lmdb.Format, pageSize int, target int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	layout, err := layoutForFormat(format)
	if err != nil {
		t.Fatal(err)
	}
	pages, err := readMetaPages(f, layout, pageSize)
	if err != nil {
		t.Fatal(err)
	}
	want := [2]uint64{}
	want[uint64(target)&1] = uint64(target)
	want[(uint64(target)-1)&1] = uint64(target) - 1
	for slot := range pages {
		if pages[slot].txnID != want[slot] {
			t.Errorf("metapage %d transaction ID = %d, want %d", slot, pages[slot].txnID, want[slot])
		}
	}
}

// assertRestoredEnvironment reopens the restored database, checks the current
// and previous snapshots, and proves the next normal write uses target+1.
func assertRestoredEnvironment(t *testing.T, path string, format lmdb.Format, target int64) {
	t.Helper()
	env := openTestEnv(t, path, format, false, true)
	assertEnvironmentTxnIDAndData(t, env, target)
	if err := env.Close(); err != nil {
		t.Fatal(err)
	}

	if format == lmdb.V10 {
		previous := openPreviousTestEnv(t, path)
		assertEnvironmentTxnIDAndData(t, previous, target-1)
		if err := previous.Close(); err != nil {
			t.Fatal(err)
		}
	}

	writable := openTestEnv(t, path, format, false, false)
	err := writable.Update(func(txn *lmdb.Txn) error {
		if got := int64(txn.ID()); got != target+1 {
			t.Errorf("next write transaction ID = %d, want %d", got, target+1)
		}
		dbi, err := txn.OpenDBI("plain", 0)
		if err != nil {
			return err
		}
		return txn.Put(dbi, []byte("after-restore"), []byte("ok"), 0)
	})
	if err != nil {
		writable.Close()
		t.Fatal(err)
	}
	info, err := writable.Info()
	if err != nil {
		writable.Close()
		t.Fatal(err)
	}
	if info.LastTxnID != target+1 {
		t.Errorf("LastTxnID after write = %d, want %d", info.LastTxnID, target+1)
	}
	if err := writable.Close(); err != nil {
		t.Fatal(err)
	}
}

// assertEnvironmentTxnIDAndData checks the selected snapshot's transaction ID
// and one copied record through the public LMDB API.
func assertEnvironmentTxnIDAndData(t *testing.T, env *lmdb.Env, wantTxnID int64) {
	t.Helper()
	info, err := env.Info()
	if err != nil {
		t.Fatal(err)
	}
	if info.LastTxnID != wantTxnID {
		t.Errorf("LastTxnID = %d, want %d", info.LastTxnID, wantTxnID)
	}
	err = env.View(func(txn *lmdb.Txn) error {
		dbi, err := txn.OpenDBI("plain", 0)
		if err != nil {
			return err
		}
		value, err := txn.Get(dbi, []byte("key-0000"))
		if err != nil {
			return err
		}
		if string(value) != "val-0" {
			t.Errorf("key-0000 value = %q, want %q", value, "val-0")
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// openPreviousTestEnv opens the previous snapshot of a 1.0-format fixture.
func openPreviousTestEnv(t *testing.T, path string) *lmdb.Env {
	t.Helper()
	env, err := lmdb.NewEnv()
	if err != nil {
		t.Fatal(err)
	}
	if err := env.SetFormat(lmdb.V10); err != nil {
		t.Fatal(err)
	}
	if err := env.SetMapSize(0); err != nil {
		t.Fatal(err)
	}
	if err := env.SetMaxDBs(64); err != nil {
		t.Fatal(err)
	}
	if err := env.Open(path, lmdb.Readonly|lmdb.PrevSnapshot, 0o644); err != nil {
		t.Fatalf("open previous snapshot: %v", err)
	}
	return env
}
