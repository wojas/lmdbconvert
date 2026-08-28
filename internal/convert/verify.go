package convert

import (
	"bytes"
	"fmt"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// verifyCounts checks that every destination DBI holds exactly as many
// entries as its source counterpart. Always runs after the copy.
func verifyCounts(dstEnv *lmdb.Env, dbis []*dbInfo) error {
	return dstEnv.View(func(txn *lmdb.Txn) error {
		for _, db := range dbis {
			st, err := txn.Stat(db.dst)
			if err != nil {
				return fmt.Errorf("stat destination database %q: %w", db.Name, err)
			}
			if st.Entries != db.Entries {
				return fmt.Errorf("destination database %q has %d entries, expected %d",
					db.Name, st.Entries, db.Entries)
			}
		}
		return nil
	})
}

// verifyData walks source and destination in lockstep and byte-compares
// every key and value, which inherently also verifies duplicate order.
// It must run while the source snapshot transaction is still open.
func verifyData(srcTxn *lmdb.Txn, dstEnv *lmdb.Env, p *plan, opts Options) error {
	ph := opts.Progress.Start("verify", int64(p.totalEntries))
	defer ph.Done()

	return dstEnv.View(func(dstTxn *lmdb.Txn) error {
		dstTxn.RawRead = true
		for _, db := range p.dbis {
			if err := verifyDBI(srcTxn, dstTxn, db, ph); err != nil {
				return err
			}
		}
		return nil
	})
}

func verifyDBI(srcTxn, dstTxn *lmdb.Txn, db *dbInfo, ph copyPhase) error {
	srcCur, err := srcTxn.OpenCursor(db.src)
	if err != nil {
		return fmt.Errorf("database %q: %w", db.Name, err)
	}
	defer srcCur.Close()
	dstCur, err := dstTxn.OpenCursor(db.dst)
	if err != nil {
		return fmt.Errorf("database %q: %w", db.Name, err)
	}
	defer dstCur.Close()

	for i := uint64(0); ; i++ {
		sk, sv, serr := srcCur.Get(nil, nil, lmdb.Next)
		dk, dv, derr := dstCur.Get(nil, nil, lmdb.Next)
		sEnd, dEnd := lmdb.IsNotFound(serr), lmdb.IsNotFound(derr)
		switch {
		case sEnd && dEnd:
			return nil
		case sEnd != dEnd:
			return fmt.Errorf("verify database %q: source and destination have different entry counts", db.Name)
		case serr != nil:
			return fmt.Errorf("verify database %q: read source: %w", db.Name, serr)
		case derr != nil:
			return fmt.Errorf("verify database %q: read destination: %w", db.Name, derr)
		}
		if !bytes.Equal(sk, dk) || !bytes.Equal(sv, dv) {
			return fmt.Errorf("verify database %q: mismatch at entry %d (key %q)", db.Name, i, sk)
		}
		ph.Add(1)
	}
}
