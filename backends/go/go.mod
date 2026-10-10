module github.com/vlumi/wrinkle/backends/go

go 1.27

require (
	github.com/cespare/xxhash/v2 v2.3.0
	github.com/coder/websocket v1.8.15
	github.com/go-sql-driver/mysql v1.10.1
	github.com/redis/go-redis/v9 v9.23.0
	github.com/twmb/franz-go v1.22.1
	github.com/vlumi/wrinkle/proto/go v0.0.0
	google.golang.org/protobuf v1.36.12
)

require (
	filippo.io/edwards25519 v1.2.0 // indirect
	github.com/klauspost/compress v1.20.0 // indirect
	github.com/pierrec/lz4/v4 v4.1.30 // indirect
	github.com/twmb/franz-go/pkg/kmsg v1.14.0 // indirect
	go.uber.org/atomic v1.12.0 // indirect
	golang.org/x/sys v0.48.0 // indirect
)

replace github.com/vlumi/wrinkle/proto/go => ../../proto/go
