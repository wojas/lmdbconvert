package convert

import (
	"fmt"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// padSentinelKey is written and deleted again within each padding
// transaction. The leading NUL byte guarantees it can never collide with a
// named database reference in the root DBI: database names are C strings
// in both formats and cannot start with a NUL byte.
var padSentinelKey = []byte("\x00lmdbconvert.txnid-pad")

// padTxnID raises the destination's last committed transaction ID to
// target by committing dummy write transactions. Applications like
// Lightning Stream depend on the transaction ID, so a converted database
// must end at the same ID as the source.
//
// Committing an empty write transaction does NOT increment the
// transaction ID (both engines skip the meta page write when nothing was
// dirtied), so each dummy transaction performs a Put and Del of a sentinel
// key: a net no-op that still dirties pages and thus increments the ID by
// exactly one.
func padTxnID(dstEnv *lmdb.Env, target int64, opts Options) error {
	info, err := dstEnv.Info()
	if err != nil {
		return err
	}
	n := target - info.LastTxnID
	if n < 0 {
		return fmt.Errorf("destination transaction ID %d exceeds the source's %d; "+
			"rerun without --batch-size to preserve the transaction ID",
			info.LastTxnID, target)
	}
	if n == 0 {
		return nil
	}

	opts.Log.Info("raising destination transaction ID to match the source",
		"current", info.LastTxnID, "target", target)
	ph := opts.Progress.Start("pad", n)
	defer ph.Done()

	for i := int64(0); i < n; i++ {
		err := dstEnv.Update(func(txn *lmdb.Txn) error {
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
			return fmt.Errorf("transaction ID padding: %w", err)
		}
		ph.Add(1)
	}

	info, err = dstEnv.Info()
	if err != nil {
		return err
	}
	if info.LastTxnID != target {
		return fmt.Errorf("transaction ID padding failed: destination is at %d, expected %d",
			info.LastTxnID, target)
	}
	return nil
}
