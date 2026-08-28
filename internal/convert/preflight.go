package convert

import (
	"errors"
	"fmt"
	"os"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"

	"github.com/wojas/lmdbconvert/internal/lmdbpath"
)

// preflight performs all checks that must pass before the destination is
// created, and opens the source read-only. It never creates or modifies
// the destination.
func preflight(opts Options) (*plan, *lmdb.Env, error) {
	p := &plan{}

	var err error
	p.srcPath, err = lmdbpath.ResolveExisting(opts.Source)
	if err != nil {
		return nil, nil, fmt.Errorf("source: %w", err)
	}

	var srcSniffFlags uint
	if p.srcPath.NoSubdir {
		srcSniffFlags = lmdb.NoSubdir
	}
	p.srcVer, err = lmdb.SniffFormat(p.srcPath.EnvPath, srcSniffFlags)
	if err != nil {
		return nil, nil, fmt.Errorf("source %s: %w", opts.Source, err)
	}
	if p.srcVer == lmdb.FormatUnknown {
		return nil, nil, fmt.Errorf("source %s does not exist or is empty", opts.Source)
	}

	p.dstVer, err = direction(p.srcVer, opts)
	if err != nil {
		return nil, nil, err
	}

	if opts.PrevSnapshot && p.srcVer != lmdb.V10 {
		return nil, nil, fmt.Errorf("--prev-snapshot requires a 1.0-format source; " +
			"the 0.9 engine has no previous-snapshot support")
	}

	p.dstPath, err = lmdbpath.ResolveNew(opts.Dest, p.srcPath.NoSubdir)
	if err != nil {
		return nil, nil, err
	}
	if err := checkDest(p, opts); err != nil {
		return nil, nil, err
	}

	srcEnv, err := openSource(p, opts)
	if err != nil {
		return nil, nil, err
	}

	info, err := srcEnv.Info()
	if err != nil {
		srcEnv.Close()
		return nil, nil, err
	}
	stat, err := srcEnv.Stat()
	if err != nil {
		srcEnv.Close()
		return nil, nil, err
	}
	p.usedBytes = (info.LastPNO + 1) * int64(stat.PSize)
	p.srcMapSize = info.MapSize
	if st, err := os.Stat(p.srcPath.DataFile()); err == nil {
		p.srcFileSize = st.Size()
	}
	p.mapSize, err = computeMapSize(info.MapSize, p.usedBytes, opts.MapSize)
	if err != nil {
		srcEnv.Close()
		return nil, nil, err
	}
	return p, srcEnv, nil
}

// direction determines the destination format. The target version must
// always be explicit: either Options.TargetVersion or Options.SameVersion.
func direction(srcVer lmdb.Format, opts Options) (lmdb.Format, error) {
	if opts.SameVersion {
		if opts.TargetVersion != lmdb.FormatUnknown {
			return 0, errors.New("--target and --same-version are mutually exclusive")
		}
		return srcVer, nil
	}
	switch opts.TargetVersion {
	case lmdb.V09, lmdb.V10:
	case lmdb.FormatUnknown:
		return 0, errors.New("no target version specified: pass --target 0.9, --target 1.0 or --same-version")
	default:
		return 0, fmt.Errorf("invalid target version %d", opts.TargetVersion)
	}
	if opts.TargetVersion == srcVer {
		return 0, fmt.Errorf("source is already in the %s format; "+
			"use --same-version for a format-preserving copy", srcVer)
	}
	return opts.TargetVersion, nil
}

// checkDest validates the destination path. With Force it only checks that
// removal will be permissible; the actual removal happens in openDest, so
// that a later preflight failure leaves the old destination untouched.
func checkDest(p *plan, opts Options) error {
	if !p.dstPath.Exists {
		return nil
	}
	if !opts.Force {
		return fmt.Errorf("destination %s already exists (use --force to replace it)", opts.Dest)
	}
	var sniffFlags uint
	if p.dstPath.NoSubdir {
		sniffFlags = lmdb.NoSubdir
	}
	ver, err := lmdb.SniffFormat(p.dstPath.EnvPath, sniffFlags)
	if err != nil || (ver != lmdb.V09 && ver != lmdb.V10 && ver != lmdb.FormatUnknown) {
		return fmt.Errorf("destination %s exists but is not an LMDB database; refusing to remove it", opts.Dest)
	}
	return nil
}

// removeDest removes an existing destination database. It only removes the
// data and lock files, never anything else.
func removeDest(p lmdbpath.Path) error {
	for _, f := range []string{p.DataFile(), p.LockFile()} {
		if err := os.Remove(f); err != nil && !os.IsNotExist(err) {
			return err
		}
	}
	return nil
}

func openSource(p *plan, opts Options) (*lmdb.Env, error) {
	env, err := lmdb.NewEnv()
	if err != nil {
		return nil, err
	}
	if err := env.SetFormat(p.srcVer); err != nil {
		return nil, err
	}
	if err := env.SetMapSize(0); err != nil { // determine automatically
		return nil, err
	}
	if err := env.SetMaxDBs(opts.MaxDBs); err != nil {
		return nil, err
	}
	flags := uint(lmdb.Readonly)
	if p.srcPath.NoSubdir {
		flags |= lmdb.NoSubdir
	}
	if opts.NoLock {
		flags |= lmdb.NoLock
	}
	if opts.PrevSnapshot {
		flags |= lmdb.PrevSnapshot
	}
	if err := env.Open(p.srcPath.EnvPath, flags, 0o644); err != nil {
		return nil, fmt.Errorf("open source %s: %w", p.srcPath.EnvPath, err)
	}
	return env, nil
}

func computeMapSize(srcMapSize, usedBytes, override int64) (int64, error) {
	if override > 0 {
		if override < usedBytes {
			return 0, fmt.Errorf("requested map size %d is smaller than the %d bytes used by the source; "+
				"use at least --map-size %d", override, usedBytes, usedBytes)
		}
		return override, nil
	}
	return max(srcMapSize, usedBytes), nil
}
