// Package convert copies an LMDB database into a new database in a
// different (or the same) on-disk format, preserving DBI flags, data and
// the last committed transaction ID.
package convert

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"

	"github.com/wojas/lmdbconvert/internal/lmdbpath"
	"github.com/wojas/lmdbconvert/internal/progress"
)

// Options configures a conversion run.
type Options struct {
	Source string
	Dest   string

	// TargetVersion is the requested destination format. Ignored when
	// SameVersion is set; required otherwise. Exactly one of the two must
	// be provided.
	TargetVersion lmdb.LMDBVersion
	// SameVersion copies into the same format as the source.
	SameVersion bool

	MapSize   int64 // destination map size; 0 = copy from source
	MaxDBs    int   // max named DBIs expected in the source; 0 = 1024
	Force     bool  // remove an existing destination
	NoLock    bool  // open the source with lmdb.NoLock
	Verify    bool  // full data comparison after copying
	WriteMap  bool  // open the destination with lmdb.WriteMap
	BatchSize int64 // commit destination every N entries; 0 = single txn

	// PrevSnapshot opens the source with lmdb.PrevSnapshot, converting the
	// previous snapshot instead of the latest transaction (e.g. to recover
	// from a corrupted last commit). The destination then also ends at the
	// previous snapshot's transaction ID. Only 1.0-format sources support
	// this.
	PrevSnapshot bool

	Progress progress.Reporter
	Log      *slog.Logger
}

// Result summarizes a successful conversion.
type Result struct {
	SourceVersion lmdb.LMDBVersion
	DestVersion   lmdb.LMDBVersion
	DBIs          int
	Entries       uint64
	TxnID         int64
	MapSize       int64
	Duration      time.Duration
}

type dbInfo struct {
	Name    string
	Flags   uint // persistent DBI flags (never contains lmdb.Create)
	Entries uint64
	src     lmdb.DBI
	dst     lmdb.DBI
}

type plan struct {
	srcPath, dstPath lmdbpath.Path
	srcVer, dstVer   lmdb.LMDBVersion
	mapSize          int64
	usedBytes        int64
	dbis             []*dbInfo
	totalEntries     uint64
	snapshotTxnID    int64
}

// Run performs the conversion. The destination is not created until all
// compatibility checks have passed.
func Run(opts Options) (*Result, error) {
	start := time.Now()
	if opts.Log == nil {
		opts.Log = slog.Default()
	}
	if opts.Progress == nil {
		opts.Progress = progress.NewNop()
	}
	if opts.MaxDBs <= 0 {
		opts.MaxDBs = 1024
	}

	p, srcEnv, err := preflight(opts)
	if err != nil {
		return nil, err
	}
	defer srcEnv.Close()

	logAttrs := []any{
		"source", p.srcPath.EnvPath,
		"source_version", p.srcVer.String(),
		"dest", p.dstPath.EnvPath,
		"dest_version", p.dstVer.String(),
		"map_size", p.mapSize,
	}
	if opts.PrevSnapshot {
		logAttrs = append(logAttrs, "prev_snapshot", true)
	}
	opts.Log.Info("converting database", logAttrs...)

	var dstEnv *lmdb.Env
	defer func() {
		if dstEnv != nil {
			dstEnv.Close()
		}
	}()

	err = srcEnv.View(func(srcTxn *lmdb.Txn) error {
		srcTxn.RawRead = true

		p.dbis, err = enumerateDBIs(srcTxn, p.srcVer, opts.MaxDBs)
		if err != nil {
			return err
		}
		for _, db := range p.dbis {
			p.totalEntries += db.Entries
			opts.Log.Debug("found database",
				"name", db.Name, "flags", flagString(db.Flags), "entries", db.Entries)
		}
		opts.Log.Info("discovered databases",
			"dbis", len(p.dbis), "entries", p.totalEntries)

		if p.srcVer == lmdb.V10 && p.dstVer == lmdb.V09 {
			if err := scanDowngradeLimits(srcTxn, p, opts); err != nil {
				return err
			}
		}
		p.snapshotTxnID = int64(srcTxn.ID())

		// All checks passed; only now is the destination created.
		dstEnv, err = openDest(p, opts)
		if err != nil {
			return err
		}
		if err := copyAll(srcTxn, dstEnv, p, opts); err != nil {
			return err
		}
		if err := verifyCounts(dstEnv, p.dbis); err != nil {
			return err
		}
		if opts.Verify {
			if err := verifyData(srcTxn, dstEnv, p, opts); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	if info, ierr := srcEnv.Info(); ierr == nil && info.LastTxnID != p.snapshotTxnID {
		opts.Log.Warn("source database was modified during the conversion; "+
			"the destination matches the snapshot at the start",
			"snapshot_txn_id", p.snapshotTxnID, "current_txn_id", info.LastTxnID)
	}

	if err := padTxnID(dstEnv, p.snapshotTxnID, opts); err != nil {
		return nil, err
	}

	// The destination was opened with NoSync; make everything durable.
	if err := dstEnv.Sync(true); err != nil {
		return nil, fmt.Errorf("sync destination: %w", err)
	}
	if err := dstEnv.Close(); err != nil {
		return nil, fmt.Errorf("close destination: %w", err)
	}
	dstEnv = nil

	var sniffFlags uint
	if p.dstPath.NoSubdir {
		sniffFlags = lmdb.NoSubdir
	}
	gotVer, err := lmdb.SniffFormat(p.dstPath.EnvPath, sniffFlags)
	if err != nil {
		return nil, fmt.Errorf("verify destination format: %w", err)
	}
	if gotVer != p.dstVer {
		return nil, fmt.Errorf("destination has format %s, expected %s", gotVer, p.dstVer)
	}

	res := &Result{
		SourceVersion: p.srcVer,
		DestVersion:   p.dstVer,
		DBIs:          len(p.dbis),
		Entries:       p.totalEntries,
		TxnID:         p.snapshotTxnID,
		MapSize:       p.mapSize,
		Duration:      time.Since(start),
	}
	opts.Log.Info("conversion complete",
		"dest", p.dstPath.EnvPath,
		"dest_version", res.DestVersion.String(),
		"dbis", res.DBIs,
		"entries", res.Entries,
		"txn_id", res.TxnID,
		"map_size", res.MapSize,
		"duration", res.Duration.Round(time.Millisecond).String(),
	)
	return res, nil
}

var dbiFlagNames = []struct {
	name string
	flag uint
}{
	{"REVERSEKEY", lmdb.ReverseKey},
	{"DUPSORT", lmdb.DupSort},
	{"INTEGERKEY", lmdb.IntegerKey},
	{"DUPFIXED", lmdb.DupFixed},
	{"INTEGERDUP", lmdb.IntegerDup},
	{"REVERSEDUP", lmdb.ReverseDup},
}

func flagString(flags uint) string {
	var s string
	for _, fd := range dbiFlagNames {
		if flags&fd.flag != 0 {
			if s != "" {
				s += ","
			}
			s += fd.name
		}
	}
	if s == "" {
		return "none"
	}
	return s
}
