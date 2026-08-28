module github.com/wojas/lmdbconvert

go 1.21

require (
	github.com/PowerDNS/lmdb-go/v2 v2.0.0-20260828152212-7d658404dccc
	github.com/c2h5oh/datasize v0.0.0-20231215233829-aa82cc1e6500
	github.com/spf13/pflag v1.0.10
)

replace github.com/PowerDNS/lmdb-go/v2 => github.com/wojas/lmdb-go/v2 v2.0.0-20260828152212-7d658404dccc
