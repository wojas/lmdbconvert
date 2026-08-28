package convert

import (
	"bytes"
	"fmt"
	"os"
	"syscall"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// openDest creates and opens the destination environment. This is the
// first operation with destination side effects; with Force, an existing
// destination (validated during preflight) is removed here.
func openDest(p *plan, opts Options) (*lmdb.Env, error) {
	if p.dstPath.Exists && opts.Force {
		opts.Log.Info("removing existing destination", "dest", p.dstPath.EnvPath)
		if err := removeDest(p.dstPath); err != nil {
			return nil, fmt.Errorf("remove existing destination: %w", err)
		}
	}
	if !p.dstPath.NoSubdir {
		if err := os.MkdirAll(p.dstPath.EnvPath, 0o755); err != nil {
			return nil, err
		}
	}

	env, err := lmdb.NewEnv()
	if err != nil {
		return nil, err
	}
	if err := env.SetFormat(p.dstVer); err != nil {
		return nil, err
	}
	if err := env.SetMapSize(p.mapSize); err != nil {
		return nil, err
	}
	if err := env.SetMaxDBs(max(1, len(p.dbis))); err != nil {
		return nil, err
	}
	// NoSync is safe here: the destination is new, and Run performs an
	// explicit Env.Sync before closing. A crash mid-conversion just means
	// rerunning with --force.
	flags := uint(lmdb.NoSync | lmdb.NoMemInit)
	if p.dstPath.NoSubdir {
		flags |= lmdb.NoSubdir
	}
	if opts.WriteMap {
		flags |= lmdb.WriteMap
	}
	if err := env.Open(p.dstPath.EnvPath, flags, 0o644); err != nil {
		return nil, fmt.Errorf("open destination %s: %w", p.dstPath.EnvPath, err)
	}
	return env, nil
}

// appendPutter picks the put flags for bulk-loading entries that arrive in
// the source's storage order. Append may only be passed while the key
// strictly increases: mdb_cursor_put returns KeyExist for key <= last-key
// before its duplicate handling is ever reached, so additional duplicates
// of the current DupSort key must be written with AppendDup alone.
type appendPutter struct {
	dupSort bool
	prevKey []byte // references the source mmap; valid for the whole source txn
}

func newAppendPutter(dbiFlags uint) *appendPutter {
	return &appendPutter{dupSort: dbiFlags&lmdb.DupSort != 0}
}

func (a *appendPutter) flags(key []byte) uint {
	if !a.dupSort {
		return lmdb.Append
	}
	if a.prevKey != nil && bytes.Equal(key, a.prevKey) {
		return lmdb.AppendDup
	}
	a.prevKey = key
	return lmdb.Append | lmdb.AppendDup
}

// copyAll creates all destination DBIs with the source flags and copies
// all data. By default everything happens in a single write transaction,
// so the destination ends at transaction ID 1 and padTxnID only ever needs
// to count upward. With BatchSize > 0 the destination commits every
// BatchSize entries instead.
func copyAll(srcTxn *lmdb.Txn, dstEnv *lmdb.Env, p *plan, opts Options) error {
	if len(p.dbis) == 0 {
		return nil
	}
	ph := opts.Progress.Start("copy", int64(p.totalEntries))
	defer ph.Done()

	if opts.BatchSize <= 0 {
		return dstEnv.Update(func(dstTxn *lmdb.Txn) error {
			if err := createDBIs(dstTxn, p.dbis); err != nil {
				return err
			}
			for _, db := range p.dbis {
				if err := copyDBI(srcTxn, dstTxn, db, ph); err != nil {
					return err
				}
			}
			return nil
		})
	}
	return copyBatched(srcTxn, dstEnv, p, opts, ph)
}

func createDBIs(dstTxn *lmdb.Txn, dbis []*dbInfo) error {
	for _, db := range dbis {
		// Txn.Flags never reports Create, so it must be ORed in here.
		dbi, err := dstTxn.OpenDBI(db.Name, db.Flags|lmdb.Create)
		if err != nil {
			return fmt.Errorf("create database %q: %w", db.Name, err)
		}
		db.dst = dbi
	}
	return nil
}

type copyPhase interface{ Add(n int64) }

func copyDBI(srcTxn, dstTxn *lmdb.Txn, db *dbInfo, ph copyPhase) error {
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

	putter := newAppendPutter(db.Flags)
	for {
		k, v, err := srcCur.Get(nil, nil, lmdb.Next)
		if lmdb.IsNotFound(err) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read database %q: %w", db.Name, err)
		}
		if err := dstCur.Put(k, v, putter.flags(k)); err != nil {
			return wrapPutError(err, db.Name)
		}
		ph.Add(1)
	}
}

func wrapPutError(err error, dbName string) error {
	if lmdb.IsErrno(err, lmdb.KeyExist) || lmdb.IsErrnoSys(err, syscall.EINVAL) {
		return fmt.Errorf("database %q is not in default key order, "+
			"probably because it was written with a custom comparator (mdb_set_compare); "+
			"such databases cannot be converted: %w", dbName, err)
	}
	return fmt.Errorf("write database %q: %w", dbName, err)
}

// copyBatched copies with a destination commit every BatchSize entries.
// This keeps write transactions small for huge databases, at the price of
// a higher destination transaction ID: padTxnID errors out if it exceeds
// the source's.
func copyBatched(srcTxn *lmdb.Txn, dstEnv *lmdb.Env, p *plan, opts Options, ph copyPhase) error {
	err := dstEnv.Update(func(dstTxn *lmdb.Txn) error {
		return createDBIs(dstTxn, p.dbis)
	})
	if err != nil {
		return err
	}

	for _, db := range p.dbis {
		srcCur, err := srcTxn.OpenCursor(db.src)
		if err != nil {
			return fmt.Errorf("database %q: %w", db.Name, err)
		}
		putter := newAppendPutter(db.Flags)
		done := false
		for !done {
			err := dstEnv.Update(func(dstTxn *lmdb.Txn) error {
				dstCur, err := dstTxn.OpenCursor(db.dst)
				if err != nil {
					return err
				}
				defer dstCur.Close()
				for i := int64(0); i < opts.BatchSize; i++ {
					k, v, err := srcCur.Get(nil, nil, lmdb.Next)
					if lmdb.IsNotFound(err) {
						done = true
						return nil
					}
					if err != nil {
						return fmt.Errorf("read database %q: %w", db.Name, err)
					}
					if err := dstCur.Put(k, v, putter.flags(k)); err != nil {
						return wrapPutError(err, db.Name)
					}
					ph.Add(1)
				}
				return nil
			})
			if err != nil {
				srcCur.Close()
				return err
			}
		}
		srcCur.Close()
	}
	return nil
}
