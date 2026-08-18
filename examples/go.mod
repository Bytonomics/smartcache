module github.com/Bytonomics/smartcache/examples

go 1.26

require (
	github.com/Bytonomics/smartcache v0.0.0-00010101000000-000000000000
	github.com/redis/go-redis/v9 v9.17.2
)

require (
	github.com/cespare/xxhash/v2 v2.3.0 // indirect
	github.com/dgryski/go-rendezvous v0.0.0-20200823014737-9f7001d12a5f // indirect
	golang.org/x/sync v0.20.0 // indirect
)

// examples is a nested module so its dependencies (and any it gains later)
// never touch the root module's go.mod/go.sum. This replace resolves the
// root module from the local checkout instead of a published version.
replace github.com/Bytonomics/smartcache => ../
