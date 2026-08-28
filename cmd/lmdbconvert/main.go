// Command lmdbconvert converts an LMDB database between the 0.9 and 1.0
// on-disk formats. The source is always opened read-only; the destination
// is only created after all compatibility checks have passed. The
// destination ends at the same last transaction ID as the source, which
// applications like LightningStream depend on.
package main

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"time"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
	"github.com/c2h5oh/datasize"
	"github.com/spf13/pflag"

	"github.com/wojas/lmdbconvert/internal/convert"
	"github.com/wojas/lmdbconvert/internal/progress"
)

var (
	flagTarget           = pflag.String("target", "", "Target LMDB format version: 0.9 or 1.0 (required unless --same-version is given)")
	flagSameVersion      = pflag.Bool("same-version", false, "Copy into the same format as the source (format-preserving rewrite)")
	flagMapSize          = pflag.String("map-size", "", "Map size for the destination, e.g. \"1GB\" or \"512MiB\" (default: same as source)")
	flagMaxDBs           = pflag.Int("max-dbs", 1024, "Maximum number of named databases expected in the source")
	flagForce            = pflag.Bool("force", false, "Remove an existing destination database before converting")
	flagVerify           = pflag.Bool("verify", false, "After converting, re-read both databases and compare every key and value")
	flagNoLock           = pflag.Bool("no-lock", false, "Open the source without a lock file (for read-only media; the source must not be written concurrently)")
	flagWriteMap         = pflag.Bool("writemap", false, "Use a writable memory map for the destination (faster for huge databases)")
	flagBatchSize        = pflag.Int64("batch-size", 0, "Commit destination entries in batches of N instead of a single transaction (escape hatch for huge databases; may prevent exact transaction ID matching)")
	flagBar              = pflag.Bool("bar", false, "Show an interactive progress bar on stderr instead of periodic log lines")
	flagProgressInterval = pflag.Duration("progress-interval", 10*time.Second, "Interval between progress log lines (ignored with --bar)")
	flagLogLevel         = pflag.String("log-level", "info", "Log level: debug, info, warn or error")
)

func main() {
	pflag.Usage = usage
	pflag.Parse()

	opts, err := parseOptions(pflag.Args())
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n\n", err)
		usage()
		os.Exit(2)
	}

	log, reporter, barWarning := setupOutput()
	opts.Log = log
	opts.Progress = reporter
	if barWarning != "" {
		log.Warn(barWarning)
	}

	if _, err := convert.Run(*opts); err != nil {
		log.Error("conversion failed", "error", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprintf(os.Stderr, "Usage: %s [flags] SOURCE DEST\n\n", os.Args[0])
	fmt.Fprintf(os.Stderr, "Converts the LMDB database at SOURCE into a new database at DEST in the\n"+
		"format selected with --target (or --same-version). SOURCE is opened\n"+
		"read-only and can be a database directory, a data.mdb file, or a plain\n"+
		"NoSubdir database file; DEST uses the same layout as SOURCE.\n\nFlags:\n")
	pflag.PrintDefaults()
}

func parseOptions(args []string) (*convert.Options, error) {
	if len(args) != 2 {
		return nil, fmt.Errorf("expected exactly 2 arguments (SOURCE and DEST), got %d", len(args))
	}
	opts := &convert.Options{
		Source:      args[0],
		Dest:        args[1],
		SameVersion: *flagSameVersion,
		MaxDBs:      *flagMaxDBs,
		Force:       *flagForce,
		NoLock:      *flagNoLock,
		Verify:      *flagVerify,
		WriteMap:    *flagWriteMap,
		BatchSize:   *flagBatchSize,
	}

	switch {
	case *flagTarget != "" && *flagSameVersion:
		return nil, fmt.Errorf("--target and --same-version are mutually exclusive")
	case *flagTarget == "" && !*flagSameVersion:
		return nil, fmt.Errorf("no target version specified: pass --target 0.9, --target 1.0 or --same-version")
	case *flagTarget != "":
		ver, err := parseVersion(*flagTarget)
		if err != nil {
			return nil, err
		}
		opts.TargetVersion = ver
	}

	if *flagMapSize != "" {
		var size datasize.ByteSize
		if err := size.UnmarshalText([]byte(*flagMapSize)); err != nil {
			return nil, fmt.Errorf("invalid --map-size %q: %w", *flagMapSize, err)
		}
		if size.Bytes() == 0 {
			return nil, fmt.Errorf("--map-size must be greater than 0")
		}
		opts.MapSize = int64(size.Bytes())
	}
	return opts, nil
}

func parseVersion(s string) (lmdb.LMDBVersion, error) {
	switch s {
	case "0.9", "09", "9":
		return lmdb.V09, nil
	case "1.0", "10":
		return lmdb.V10, nil
	}
	return 0, fmt.Errorf("invalid --target %q: must be 0.9 or 1.0", s)
}

func setupOutput() (log *slog.Logger, reporter progress.Reporter, barWarning string) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(*flagLogLevel)); err != nil {
		level = slog.LevelInfo
	}

	var logOut io.Writer = os.Stderr
	if *flagBar {
		if progress.IsTerminal(os.Stderr) {
			bar := progress.NewBar(os.Stderr)
			reporter = bar
			logOut = bar.LogWriter()
		} else {
			barWarning = "--bar requires a terminal on stderr; falling back to progress log lines"
		}
	}
	log = slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: level}))
	if reporter == nil {
		reporter = progress.NewLog(log, *flagProgressInterval)
	}
	return log, reporter, barWarning
}
