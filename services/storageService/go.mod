module storageService

go 1.24

toolchain go1.24.1

require (
	Betterfly2/proto/storage v0.0.0
	Betterfly2/shared v0.0.0
)

require (
	Betterfly2/proto v0.0.0 // indirect
	github.com/cespare/xxhash/v2 v2.1.1 // indirect
	github.com/dgraph-io/ristretto v0.2.0 // indirect
	github.com/dustin/go-humanize v1.0.1 // indirect
	github.com/pkg/errors v0.9.1 // indirect
	go.uber.org/multierr v1.11.0 // indirect
	go.uber.org/zap v1.27.0 // indirect
	golang.org/x/sys v0.11.0 // indirect
	google.golang.org/protobuf v1.36.6 // indirect
)

replace (
	Betterfly2/proto => ../../proto
	Betterfly2/proto/storage => ../../proto/storage
	Betterfly2/shared => ../../shared
)
