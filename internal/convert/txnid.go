package convert

import (
	"encoding/binary"
	"fmt"
	"io"
	"math/bits"
	"os"

	"github.com/PowerDNS/lmdb-go/v2/lmdb"
)

// The bundled engines use native 64-bit layouts. MDB_meta begins after a
// 16-byte 0.9 page header or a 24-byte 1.0 page header. Within MDB_meta the
// fixed fields are magic@0, version@4 and address@8; the fields rewritten by
// mdb_env_write_meta start with mapsize@16, followed by two 48-byte MDB_db
// records, last_pg@120 and txnid@128.
const (
	metaMagic          = 0xBEEFC0DE
	metaPageFlag       = 0x08
	metaDynamicOffset  = 16
	metaPageSizeOffset = 24
	metaTxnIDOffset    = 128
	metaTxnIDSize      = 8
	metaVersion09      = 1
	metaVersion10      = 3
	pageHeaderSize09   = 16
	pageHeaderSize10   = 24
	pageFlagsOffset09  = 10
	pageFlagsOffset10  = 18
	metaPageCount      = 2
)

// metaLayout describes the 64-bit on-disk metapage layout of one bundled
// LMDB format. LMDB writes integers in the host's native byte order.
type metaLayout struct {
	pageHeaderSize  int
	pageFlagsOffset int
	version         uint32
}

// layoutForFormat returns the metapage layout used by the selected bundled
// LMDB engine.
func layoutForFormat(format lmdb.Format) (metaLayout, error) {
	switch format {
	case lmdb.V09:
		return metaLayout{
			pageHeaderSize:  pageHeaderSize09,
			pageFlagsOffset: pageFlagsOffset09,
			version:         metaVersion09,
		}, nil
	case lmdb.V10:
		return metaLayout{
			pageHeaderSize:  pageHeaderSize10,
			pageFlagsOffset: pageFlagsOffset10,
			version:         metaVersion10,
		}, nil
	default:
		return metaLayout{}, fmt.Errorf("unsupported LMDB format %s", format)
	}
}

// metaPage holds the fields needed to select and republish an LMDB metadata
// snapshot. Dynamic contains mm_mapsize, both MDB_db records and mm_last_pg,
// but excludes mm_txnid so the transaction ID can be published last.
type metaPage struct {
	txnID   uint64
	dynamic []byte
}

// restoreTxnID raises a closed destination environment's last transaction ID
// without creating every intervening transaction. It republishes the current
// metadata snapshot into the two metapages so they finish at target-1 and
// target, preserving LMDB's even/odd metapage convention.
func restoreTxnID(path string, format lmdb.Format, pageSize int, current, target int64) error {
	if bits.UintSize != 64 {
		return fmt.Errorf("transaction ID restore requires a 64-bit platform")
	}
	if current < 0 || target < 0 {
		return fmt.Errorf("transaction IDs must not be negative: current=%d target=%d", current, target)
	}
	if current > target {
		return fmt.Errorf("destination transaction ID %d exceeds the source's %d; "+
			"rerun without --batch-size to preserve the transaction ID", current, target)
	}
	if current == target {
		return nil
	}
	if pageSize <= 0 {
		return fmt.Errorf("invalid LMDB page size %d", pageSize)
	}

	layout, err := layoutForFormat(format)
	if err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return fmt.Errorf("open destination data file: %w", err)
	}
	defer f.Close()

	pages, err := readMetaPages(f, layout, pageSize)
	if err != nil {
		return err
	}
	active := 0
	if pages[1].txnID > pages[0].txnID {
		active = 1
	}
	if pages[active].txnID != uint64(current) {
		return fmt.Errorf("destination metadata transaction ID is %d, expected %d",
			pages[active].txnID, current)
	}
	if current != 0 && uint64(active) != pages[active].txnID&1 {
		return fmt.Errorf("destination metadata transaction ID %d is in metapage %d",
			pages[active].txnID, active)
	}

	// If target-1 would overwrite the only current snapshot, first publish
	// target-2 to the other metapage. This keeps a complete snapshot usable
	// through every write and still leaves the normal target-1/target pair.
	var publications []uint64
	if target-current > 1 && uint64(active) == (uint64(target)-1)&1 {
		publications = append(publications, uint64(target)-2)
	}
	if target-current > 1 {
		publications = append(publications, uint64(target)-1)
	}
	publications = append(publications, uint64(target))

	for _, txnID := range publications {
		slot := int(txnID & 1)
		if err := publishMetaPage(f, layout, pageSize, slot, pages[active].dynamic, txnID); err != nil {
			return err
		}
	}

	finalPages, err := readMetaPages(f, layout, pageSize)
	if err != nil {
		return fmt.Errorf("verify restored transaction ID: %w", err)
	}
	if got := finalPages[uint64(target)&1].txnID; got != uint64(target) {
		return fmt.Errorf("restored transaction ID is %d, expected %d", got, target)
	}
	if got := finalPages[(uint64(target)-1)&1].txnID; got != uint64(target)-1 {
		return fmt.Errorf("previous transaction ID is %d, expected %d", got, target-1)
	}
	return nil
}

// verifyEnvironmentTxnID reopens a destination through LMDB and verifies that
// the engine selects the expected metapage after the offline update.
func verifyEnvironmentTxnID(path string, noSubdir bool, format lmdb.Format, target int64) error {
	env, err := lmdb.NewEnv()
	if err != nil {
		return err
	}
	defer env.Close()
	if err := env.SetFormat(format); err != nil {
		return err
	}
	if err := env.SetMapSize(0); err != nil {
		return err
	}
	flags := uint(lmdb.Readonly)
	if noSubdir {
		flags |= lmdb.NoSubdir
	}
	if err := env.Open(path, flags, 0o644); err != nil {
		return err
	}
	info, err := env.Info()
	if err != nil {
		return err
	}
	if info.LastTxnID != target {
		return fmt.Errorf("destination transaction ID is %d, expected %d", info.LastTxnID, target)
	}
	return nil
}

// readMetaPages reads and validates both LMDB metapages before any direct
// metadata update. The layouts are native 64-bit structures matching the
// two LMDB engines bundled by lmdb-go.
func readMetaPages(r io.ReaderAt, layout metaLayout, pageSize int) ([metaPageCount]metaPage, error) {
	var pages [metaPageCount]metaPage
	metaEnd := layout.pageHeaderSize + metaTxnIDOffset + metaTxnIDSize
	if pageSize < metaEnd {
		return pages, fmt.Errorf("LMDB page size %d is too small for metadata ending at %d", pageSize, metaEnd)
	}

	for slot := range pages {
		buf := make([]byte, metaEnd)
		if _, err := r.ReadAt(buf, int64(slot*pageSize)); err != nil {
			return pages, fmt.Errorf("read metapage %d: %w", slot, err)
		}
		if pgno := binary.NativeEndian.Uint64(buf[:8]); pgno != uint64(slot) {
			return pages, fmt.Errorf("metapage %d carries page number %d", slot, pgno)
		}
		if flags := binary.NativeEndian.Uint16(buf[layout.pageFlagsOffset:]); flags != metaPageFlag {
			return pages, fmt.Errorf("page %d has flags %#x, expected metadata flag %#x", slot, flags, metaPageFlag)
		}
		meta := buf[layout.pageHeaderSize:]
		if magic := binary.NativeEndian.Uint32(meta); magic != metaMagic {
			return pages, fmt.Errorf("metapage %d has magic %#x, expected %#x", slot, magic, metaMagic)
		}
		if version := binary.NativeEndian.Uint32(meta[4:]); version != layout.version {
			return pages, fmt.Errorf("metapage %d has format version %d, expected %d", slot, version, layout.version)
		}
		if storedPageSize := binary.NativeEndian.Uint32(meta[metaPageSizeOffset:]); storedPageSize != uint32(pageSize) {
			return pages, fmt.Errorf("metapage %d has page size %d, expected %d", slot, storedPageSize, pageSize)
		}
		pages[slot] = metaPage{
			txnID:   binary.NativeEndian.Uint64(meta[metaTxnIDOffset:]),
			dynamic: append([]byte(nil), meta[metaDynamicOffset:metaTxnIDOffset]...),
		}
		if pages[slot].txnID != 0 && pages[slot].txnID&1 != uint64(slot) {
			return pages, fmt.Errorf("metapage %d carries transaction ID %d with the wrong parity", slot, pages[slot].txnID)
		}
	}
	return pages, nil
}

// publishMetaPage copies one complete dynamic metadata snapshot and only then
// publishes its transaction ID. Syncing between the writes ensures LMDB cannot
// select a partially copied snapshot after a crash.
func publishMetaPage(f *os.File, layout metaLayout, pageSize, slot int, dynamic []byte, txnID uint64) error {
	pageOffset := int64(slot * pageSize)
	dynamicOffset := pageOffset + int64(layout.pageHeaderSize+metaDynamicOffset)
	if err := writeFullAt(f, dynamic, dynamicOffset); err != nil {
		return fmt.Errorf("write metapage %d snapshot: %w", slot, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync metapage %d snapshot: %w", slot, err)
	}

	var encoded [metaTxnIDSize]byte
	binary.NativeEndian.PutUint64(encoded[:], txnID)
	txnIDOffset := pageOffset + int64(layout.pageHeaderSize+metaTxnIDOffset)
	if err := writeFullAt(f, encoded[:], txnIDOffset); err != nil {
		return fmt.Errorf("write metapage %d transaction ID: %w", slot, err)
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("sync metapage %d transaction ID: %w", slot, err)
	}
	return nil
}

// writeFullAt retries short positional writes until the complete buffer has
// been written or the file reports an error.
func writeFullAt(f *os.File, buf []byte, offset int64) error {
	for len(buf) > 0 {
		n, err := f.WriteAt(buf, offset)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		buf = buf[n:]
		offset += int64(n)
	}
	return nil
}
