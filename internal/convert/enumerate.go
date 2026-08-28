package convert

import (
	"bytes"
	"fmt"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
	"github.com/PowerDNS/lmdb-go/v2/lmdbscan"
)

// mdbDBSize is the size of the C MDB_db struct, identical in the 0.9 and
// 1.0 engines. Every named-database entry in the root DBI has a value of
// this size.
const mdbDBSize = 48

// enumerateDBIs lists all named databases via the root DBI. The root DBI
// must contain only named-database references; anything else is an error,
// as silently skipping entries could lose data during a conversion.
func enumerateDBIs(txn *lmdb.Txn, ver lmdb.LMDBVersion, maxDBs int) ([]*dbInfo, error) {
	root, err := txn.OpenRoot(0)
	if err != nil {
		return nil, err
	}

	var names []string
	s := lmdbscan.New(txn, root)
	defer s.Close()
	for s.Scan() {
		if len(s.Val()) != mdbDBSize {
			return nil, fmt.Errorf(
				"root database entry %q is not a named database reference (value size %d, expected %d); "+
					"databases with loose data in the root database cannot be converted",
				s.Key(), len(s.Val()), mdbDBSize)
		}
		name, err := dbiKeyName(s.Key(), ver)
		if err != nil {
			return nil, err
		}
		names = append(names, name)
	}
	if err := s.Err(); err != nil {
		return nil, err
	}
	if len(names) > maxDBs {
		return nil, fmt.Errorf("found %d named databases, more than the configured maximum; "+
			"rerun with --max-dbs %d or higher", len(names), len(names))
	}

	var dbis []*dbInfo
	for _, name := range names {
		dbi, err := txn.OpenDBI(name, 0)
		if err != nil {
			return nil, fmt.Errorf("open database %q: %w", name, err)
		}
		flags, err := txn.Flags(dbi)
		if err != nil {
			return nil, fmt.Errorf("flags of database %q: %w", name, err)
		}
		st, err := txn.Stat(dbi)
		if err != nil {
			return nil, fmt.Errorf("stat of database %q: %w", name, err)
		}
		dbis = append(dbis, &dbInfo{
			Name:    name,
			Flags:   flags,
			Entries: st.Entries,
			src:     dbi,
		})
	}
	return dbis, nil
}

// dbiKeyName converts a root DBI key into a database name. The 1.0 format
// stores names with a trailing NUL byte; the 0.9 format does not.
func dbiKeyName(key []byte, ver lmdb.LMDBVersion) (string, error) {
	if ver == lmdb.V10 {
		if len(key) == 0 || key[len(key)-1] != 0 {
			return "", fmt.Errorf("root database key %q lacks the trailing NUL byte expected in the 1.0 format", key)
		}
		key = key[:len(key)-1]
	}
	if len(key) == 0 {
		return "", fmt.Errorf("root database contains an empty database name")
	}
	if bytes.IndexByte(key, 0) >= 0 {
		return "", fmt.Errorf("root database key %q contains a NUL byte", key)
	}
	return string(key), nil
}
