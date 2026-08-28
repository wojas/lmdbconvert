module github.com/wojas/lmdbconvert

go 1.21

replace github.com/PowerDNS/lmdb-go/v2 => ../lmdb-go

require (
	github.com/PowerDNS/lmdb-go/v2 v2.0.0-00010101000000-000000000000
	github.com/c2h5oh/datasize v0.0.0-20231215233829-aa82cc1e6500
	github.com/spf13/pflag v1.0.10
)
