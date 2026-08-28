package convert

import (
	"fmt"
	"strings"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// maxKeySize09 is the compile-time MDB_MAXKEYSIZE of LMDB 0.9. The 1.0
// engine derives its limit from the page size (~8 kB for 4 kB pages), so
// only 1.0 to 0.9 conversions can hit this. In a DupSort database the
// duplicate values are subject to the same limit as keys.
const maxKeySize09 = 511

const maxViolationSamples = 10

type sizeViolation struct {
	db    string
	what  string // "key" or "duplicate value"
	size  int
	value []byte
}

func (v sizeViolation) String() string {
	preview := v.value
	trunc := ""
	if len(preview) > 32 {
		preview = preview[:32]
		trunc = "..."
	}
	return fmt.Sprintf("database %q: %s of %d bytes (max %d): %x%s",
		v.db, v.what, v.size, maxKeySize09, preview, trunc)
}

// scanDowngradeLimits scans all data for keys and DupSort values that
// exceed the 0.9 maximum key size, so that an impossible 1.0 to 0.9
// conversion aborts before the destination is created.
func scanDowngradeLimits(txn *lmdb.Txn, p *plan, opts Options) error {
	ph := opts.Progress.Start("scan", int64(p.totalEntries))
	defer ph.Done()

	var violations []sizeViolation
	var total int
	for _, db := range p.dbis {
		cur, err := txn.OpenCursor(db.src)
		if err != nil {
			return fmt.Errorf("scan database %q: %w", db.Name, err)
		}
		dupSort := db.Flags&lmdb.DupSort != 0
		for {
			k, v, err := cur.Get(nil, nil, lmdb.Next)
			if lmdb.IsNotFound(err) {
				break
			}
			if err != nil {
				cur.Close()
				return fmt.Errorf("scan database %q: %w", db.Name, err)
			}
			if len(k) > maxKeySize09 {
				total++
				if len(violations) < maxViolationSamples {
					violations = append(violations, sizeViolation{db.Name, "key", len(k), append([]byte(nil), k...)})
				}
			}
			if dupSort && len(v) > maxKeySize09 {
				total++
				if len(violations) < maxViolationSamples {
					violations = append(violations, sizeViolation{db.Name, "duplicate value", len(v), append([]byte(nil), v...)})
				}
			}
			ph.Add(1)
		}
		cur.Close()
	}
	if total == 0 {
		return nil
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "cannot convert to the 0.9 format: %d entries exceed the 0.9 size limits:", total)
	for _, v := range violations {
		sb.WriteString("\n  ")
		sb.WriteString(v.String())
	}
	if total > len(violations) {
		fmt.Fprintf(&sb, "\n  ... and %d more", total-len(violations))
	}
	return fmt.Errorf("%s", sb.String())
}
