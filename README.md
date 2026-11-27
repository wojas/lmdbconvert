# LMDBConvert

Utilities for converting [LMDB](https://www.symas.com/lmdb) databases
between the 0.9 and 1.0 on-disk formats, built on the
[lmdb-go v2](https://github.com/PowerDNS/lmdb-go) dual-engine bindings.

LMDB 1.0 introduced a new on-disk format that is incompatible with the
0.9 format. LMDB itself never converts a database in place; migrating
means reading with one engine and writing with the other, which is what
`lmdbconvert` does.

## Usage

```
lmdbconvert --target 1.0 /path/to/source /path/to/dest     # upgrade 0.9 -> 1.0
lmdbconvert --target 0.9 /path/to/source /path/to/dest     # downgrade 1.0 -> 0.9
lmdbconvert --same-version /path/to/source /path/to/dest   # format-preserving rewrite
```

The target format is always explicit: exactly one of `--target 0.9`,
`--target 1.0` or `--same-version` is required. Requesting the format the
source already has is an error (use `--same-version` for that).

`SOURCE` may be a database directory, a `data.mdb` file inside one, or a
plain database file (`MDB_NOSUBDIR`); it is auto-detected and `DEST` is
created with the same layout. The source is never created or modified and
is always opened read-only (note that opening still creates a lock file
next to the source unless `--no-lock` is given).

### Guarantees

- **All checks run before the destination is created.** The named
  databases are enumerated up front (including the trailing-NUL name
  encoding used by the 1.0 format), and a 1.0 → 0.9 conversion first
  scans all keys and DupSort values against the 0.9 limit of 511 bytes.
  If anything cannot be migrated, the tool aborts without creating the
  destination.
- **DBI flags are preserved.** `DUPSORT`, `DUPFIXED`, `REVERSEKEY`,
  `INTEGERKEY`, `INTEGERDUP` and `REVERSEDUP` are read from the source
  and applied to the destination databases.
- **The last transaction ID is preserved.** Applications like
  [Lightning Stream](https://github.com/PowerDNS/lightningstream) depend
  on it. The copy happens in a single write transaction, after which the
  closed destination's metapages are updated directly to restore the
  source transaction ID without replaying every intervening transaction.
  This can be disabled with `--reset-txnid` (see below).
- **The map size is copied from the source** unless overridden with
  `--map-size` (e.g. `--map-size 1GB`; all units are binary multiples).

### Flags

Run `lmdbconvert --help` for the full list. Notable ones:

- `--verify` re-reads both databases after the copy and byte-compares
  every key and value.
- `--force` removes an existing destination first. It only removes the
  data and lock files, and refuses if the destination is not an LMDB
  database or an empty directory.
- `--reset-txnid` skips raising the destination's transaction ID to
  match the source's; the destination keeps the low transaction ID from
  the copy itself. **Warning:** this cannot be used with
  [Lightning Stream](https://github.com/PowerDNS/lightningstream), which
  depends on the transaction ID.
- `--prev-snapshot` converts the source's previous snapshot instead of
  the latest transaction (LMDB keeps the two most recent), e.g. to
  recover from a corrupted last commit. The destination then ends at the
  previous snapshot's transaction ID. Only 1.0-format sources support
  this; the 0.9 engine has no equivalent.
- `--bar` shows an interactive progress bar on stderr; the default is
  periodic log lines (every 10% or `--progress-interval`), suitable for
  non-interactive use. All logging uses `log/slog` on stderr.
- `--batch-size N` commits the destination every N entries instead of
  using one transaction, as an escape hatch for enormous databases. This
  can overshoot the source's transaction ID, in which case the tool
  reports an error at the end.

Exit codes: `0` success, `2` usage error, `1` any other failure.

### Caveats

- Databases written with a custom comparator (`mdb_set_compare` /
  `mdb_set_dupsort`) cannot be converted: LMDB does not persist the
  comparator, and the copy aborts when it detects entries that are not in
  default key order.
- The root database must only contain named-database references (the
  normal case); loose user data in the root database causes an abort.
- A crash mid-conversion leaves a partial destination; simply rerun with
  `--force`.

## Installation

No familiarity with Go development is needed; building produces a single
self-contained binary with both LMDB engines compiled in.

### 1. One-time prerequisites

- A 64-bit Linux or macOS system. (Windows is not supported: the LMDB
  1.0 engine is not available there.)
- **Go 1.21 or newer.** Install with your package manager or from
  <https://go.dev/dl/>:
  - macOS: `brew install go`
  - Debian/Ubuntu: `sudo apt install golang-go`

  Check that it works with `go version`.
- **A C compiler** (the LMDB engines are C code that gets compiled in):
  - macOS: `xcode-select --install`
  - Debian/Ubuntu: `sudo apt install build-essential`

### 2. Build

```
git clone https://github.com/wojas/lmdbconvert.git
cd lmdbconvert
go build -o lmdbconvert ./cmd/lmdbconvert
```

`go build` automatically downloads the required libraries; no further
setup is needed. The result is a single `lmdbconvert` binary in the
current directory with no runtime dependencies.

### 3. Install the binary

Copy it to a directory in your `PATH`, for example:

```
sudo install -m 0755 lmdbconvert /usr/local/bin/
lmdbconvert --help
```

> **Note:** `go install github.com/wojas/lmdbconvert/cmd/lmdbconvert@latest`
> does not work yet. lmdb-go v2 is unreleased, so this module pins a
> development commit from a fork via a `replace` directive in `go.mod`,
> which `go install` refuses; clone and build as shown above instead.

## Development

```
go build ./...
go test ./...
go vet ./...
```

The lmdb-go v2 dependency is pinned to a specific development commit,
fetched from the `wojas/lmdb-go` fork through the `replace` directive in
`go.mod`. To work against a local lmdb-go checkout instead, temporarily
point the replace at it:

```
go mod edit -replace github.com/PowerDNS/lmdb-go/v2=../lmdb-go
```

## Repository layout

- `cmd/lmdbconvert/` — the conversion CLI; more utilities may be added
  under `cmd/` over time.
- `internal/convert/` — the conversion pipeline (preflight checks, copy,
  transaction-ID restoration, verification).
- `internal/lmdbpath/` — database path/layout resolution.
- `internal/progress/` — progress reporting (log steps and ANSI bar).
