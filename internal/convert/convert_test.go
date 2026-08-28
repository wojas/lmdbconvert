package convert

import (
	"path/filepath"
	"testing"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

const fixtureTxnID = 57

func runConvert(t *testing.T, opts Options) *Result {
	t.Helper()
	res, err := Run(opts)
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	return res
}

func TestConvert_Upgrade(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	res := runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10})
	if res.TxnID != fixtureTxnID {
		t.Errorf("result TxnID = %d, want %d", res.TxnID, fixtureTxnID)
	}
	checkConverted(t, src, dst, false, lmdb.V09, lmdb.V10)
}

func TestConvert_Downgrade(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V10, false, fixtureTxnID)

	runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V09})
	checkConverted(t, src, dst, false, lmdb.V10, lmdb.V09)
}

func TestConvert_SameVersion(t *testing.T) {
	for _, ver := range []lmdb.LMDBVersion{lmdb.V09, lmdb.V10} {
		t.Run(ver.String(), func(t *testing.T) {
			src, dst := fixturePaths(t)
			buildFixture(t, src, ver, false, fixtureTxnID)

			runConvert(t, Options{Source: src, Dest: dst, SameVersion: true})
			checkConverted(t, src, dst, false, ver, ver)
		})
	}
}

func TestConvert_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src09")
	mid := filepath.Join(dir, "mid10")
	back := filepath.Join(dir, "back09")
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	runConvert(t, Options{Source: src, Dest: mid, TargetVersion: lmdb.V10, Verify: true})
	runConvert(t, Options{Source: mid, Dest: back, TargetVersion: lmdb.V09, Verify: true})
	checkConverted(t, src, back, false, lmdb.V09, lmdb.V09)
}

func TestConvert_NoSubdir(t *testing.T) {
	dir := t.TempDir()
	src := filepath.Join(dir, "src.mdb")
	dst := filepath.Join(dir, "dst.mdb")
	buildFixture(t, src, lmdb.V09, true, fixtureTxnID)

	runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10})
	checkConverted(t, src, dst, true, lmdb.V09, lmdb.V10)
}

func TestConvert_DataMdbPath(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	// Pointing at the data.mdb file inside a database directory works too.
	runConvert(t, Options{
		Source:        filepath.Join(src, "data.mdb"),
		Dest:          dst,
		TargetVersion: lmdb.V10,
	})
	checkConverted(t, src, dst, false, lmdb.V09, lmdb.V10)
}

func TestConvert_MapSize(t *testing.T) {
	t.Run("copied from source", func(t *testing.T) {
		src, dst := fixturePaths(t)
		buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

		res := runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10})
		if res.MapSize != testMapSize {
			t.Errorf("MapSize = %d, want %d (copied from source)", res.MapSize, testMapSize)
		}
	})
	t.Run("override", func(t *testing.T) {
		src, dst := fixturePaths(t)
		buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

		const override = 256 << 20
		runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10, MapSize: override})
		env := openTestEnv(t, dst, lmdb.V10, false, true)
		defer env.Close()
		info, err := env.Info()
		if err != nil {
			t.Fatal(err)
		}
		if info.MapSize != override {
			t.Errorf("destination MapSize = %d, want %d", info.MapSize, override)
		}
	})
}

func TestConvert_HighTxnID(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, 500)

	res := runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10})
	if res.TxnID != 500 {
		t.Errorf("result TxnID = %d, want 500", res.TxnID)
	}
	checkConverted(t, src, dst, false, lmdb.V09, lmdb.V10)
}

func TestConvert_BatchSize(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)

	runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10, BatchSize: 100})
	checkConverted(t, src, dst, false, lmdb.V09, lmdb.V10)
}

func TestConvert_EmptyDatabase(t *testing.T) {
	// A database without any named DBIs, only a transaction ID: the copy
	// commits nothing, and padding alone must reproduce the ID.
	src, dst := fixturePaths(t)
	env := openTestEnv(t, src, lmdb.V09, false, false)
	bumpTxnID(t, env, 12)
	env.Close()

	res := runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10})
	if res.TxnID != 12 || res.DBIs != 0 || res.Entries != 0 {
		t.Errorf("got TxnID=%d DBIs=%d Entries=%d, want 12/0/0", res.TxnID, res.DBIs, res.Entries)
	}
	checkConverted(t, src, dst, false, lmdb.V09, lmdb.V10)
}

func TestConvert_ForceOverwrite(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V09, false, fixtureTxnID)
	buildFixture(t, dst, lmdb.V10, false, 3) // existing destination

	if _, err := Run(Options{Source: src, Dest: dst, TargetVersion: lmdb.V10}); err == nil {
		t.Fatal("expected error for existing destination without Force")
	}
	runConvert(t, Options{Source: src, Dest: dst, TargetVersion: lmdb.V10, Force: true})
	checkConverted(t, src, dst, false, lmdb.V09, lmdb.V10)
}

func TestConvert_PrevSnapshot(t *testing.T) {
	src, dst := fixturePaths(t)
	buildFixture(t, src, lmdb.V10, false, fixtureTxnID)

	// One more committed transaction; a previous-snapshot conversion must
	// not include it and must end at the previous transaction ID.
	env := openTestEnv(t, src, lmdb.V10, false, false)
	err := env.Update(func(txn *lmdb.Txn) error {
		dbi, err := txn.OpenDBI("plain", 0)
		if err != nil {
			return err
		}
		return txn.Put(dbi, []byte("marker"), []byte("latest"), 0)
	})
	if err != nil {
		t.Fatal(err)
	}
	env.Close()

	res := runConvert(t, Options{
		Source:        src,
		Dest:          dst,
		TargetVersion: lmdb.V09,
		PrevSnapshot:  true,
	})
	if res.TxnID != fixtureTxnID {
		t.Errorf("result TxnID = %d, want %d (previous snapshot)", res.TxnID, fixtureTxnID)
	}

	denv := openTestEnv(t, dst, lmdb.V09, false, true)
	defer denv.Close()
	err = denv.View(func(txn *lmdb.Txn) error {
		dbi, err := txn.OpenDBI("plain", 0)
		if err != nil {
			return err
		}
		if _, err := txn.Get(dbi, []byte("marker")); !lmdb.IsNotFound(err) {
			t.Errorf("marker from the latest transaction present in previous-snapshot conversion (err=%v)", err)
		}
		if _, err := txn.Get(dbi, []byte("key-0000")); err != nil {
			t.Errorf("fixture data missing from previous-snapshot conversion: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestEmptyCommitDoesNotIncrementTxnID pins the engine behavior that the
// transaction ID padding in pad.go depends on: an empty write transaction
// does not increment the transaction ID, while a Put+Del of the same key
// in one transaction increments it by exactly one.
func TestEmptyCommitDoesNotIncrementTxnID(t *testing.T) {
	for _, ver := range []lmdb.LMDBVersion{lmdb.V09, lmdb.V10} {
		t.Run(ver.String(), func(t *testing.T) {
			env := openTestEnv(t, filepath.Join(t.TempDir(), "env"), ver, false, false)
			defer env.Close()

			txnID := func() int64 {
				info, err := env.Info()
				if err != nil {
					t.Fatal(err)
				}
				return info.LastTxnID
			}

			before := txnID()
			err := env.Update(func(txn *lmdb.Txn) error {
				_, err := txn.OpenRoot(0)
				return err
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := txnID(); got != before {
				t.Errorf("empty commit changed LastTxnID from %d to %d", before, got)
			}

			err = env.Update(func(txn *lmdb.Txn) error {
				root, err := txn.OpenRoot(0)
				if err != nil {
					return err
				}
				if err := txn.Put(root, padSentinelKey, []byte{}, 0); err != nil {
					return err
				}
				return txn.Del(root, padSentinelKey, nil)
			})
			if err != nil {
				t.Fatal(err)
			}
			if got := txnID(); got != before+1 {
				t.Errorf("Put+Del commit moved LastTxnID from %d to %d, want +1", before, got)
			}
		})
	}
}
