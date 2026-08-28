// Package convert copies an LMDB database into a new database in a
// different (or the same) on-disk format, preserving DBI flags, data and
// the last committed transaction ID.
package convert

import (
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
	"github.com/c2h5oh/datasize"

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
	TargetVersion lmdb.Format
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

	// ResetTxnID skips raising the destination's transaction ID to match
	// the source's, leaving it at the low value produced by the copy
	// itself. Must not be used with Lightning Stream, which depends on the
	// transaction ID.
	ResetTxnID bool

	Progress progress.Reporter
	Log      *slog.Logger
}

// Result summarizes a successful conversion.
type Result struct {
	SourceVersion lmdb.Format
	DestVersion   lmdb.Format
	DBIs          int
	Entries       uint64
	TxnID         int64
	MapSize       int64 // destination map size
	Duration      time.Duration

	SourceMapSize   int64
	SourceUsedBytes int64 // pages used by the source, in bytes
	DestUsedBytes   int64 // pages used by the destination, in bytes
	SourceFileSize  int64 // source data file size on disk
	DestFileSize    int64 // destination data file size on disk
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
	srcVer, dstVer   lmdb.Format
	mapSize          int64
	usedBytes        int64
	srcMapSize       int64
	srcFileSize      int64
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

	finalTxnID := p.snapshotTxnID
	if opts.ResetTxnID {
		info, err := dstEnv.Info()
		if err != nil {
			return nil, err
		}
		finalTxnID = info.LastTxnID
		opts.Log.Info("not matching the source transaction ID (--reset-txnid)",
			"dest_txn_id", finalTxnID, "source_txn_id", p.snapshotTxnID)
	} else if err := padTxnID(dstEnv, p.snapshotTxnID, opts); err != nil {
		return nil, err
	}

	// The destination was opened with NoSync; make everything durable.
	if err := dstEnv.Sync(true); err != nil {
		return nil, fmt.Errorf("sync destination: %w", err)
	}
	dstInfo, err := dstEnv.Info()
	if err != nil {
		return nil, err
	}
	dstStat, err := dstEnv.Stat()
	if err != nil {
		return nil, err
	}
	if err := dstEnv.Close(); err != nil {
		return nil, fmt.Errorf("close destination: %w", err)
	}
	dstEnv = nil
	dstUsed := (dstInfo.LastPNO + 1) * int64(dstStat.PSize)
	var dstFileSize int64
	if st, serr := os.Stat(p.dstPath.DataFile()); serr == nil {
		dstFileSize = st.Size()
	}

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

	opts.Log.Info("size statistics",
		"used_before", humanBytes(p.usedBytes),
		"used_after", humanBytes(dstUsed),
		"used_change", changePct(p.usedBytes, dstUsed),
		"file_before", humanBytes(p.srcFileSize),
		"file_after", humanBytes(dstFileSize),
		"file_change", changePct(p.srcFileSize, dstFileSize),
		"map_before", humanBytes(p.srcMapSize),
		"map_after", humanBytes(dstInfo.MapSize),
		"map_change", changePct(p.srcMapSize, dstInfo.MapSize),
	)

	res := &Result{
		SourceVersion: p.srcVer,
		DestVersion:   p.dstVer,
		DBIs:          len(p.dbis),
		Entries:       p.totalEntries,
		TxnID:         finalTxnID,
		MapSize:       dstInfo.MapSize,
		Duration:      time.Since(start),

		SourceMapSize:   p.srcMapSize,
		SourceUsedBytes: p.usedBytes,
		DestUsedBytes:   dstUsed,
		SourceFileSize:  p.srcFileSize,
		DestFileSize:    dstFileSize,
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

func humanBytes(n int64) string {
	return datasize.ByteSize(n).HumanReadable()
}

// changePct formats the relative change from before to after, e.g. "-25.0%".
func changePct(before, after int64) string {
	if before <= 0 {
		return "n/a"
	}
	return fmt.Sprintf("%+.1f%%", (float64(after)-float64(before))/float64(before)*100)
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
