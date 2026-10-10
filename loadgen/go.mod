module github.com/vlumi/wrinkle/loadgen

go 1.27

require (
	github.com/vlumi/wrinkle/clients/go v0.0.0
	github.com/vlumi/wrinkle/proto/go v0.0.0
)

require (
	github.com/coder/websocket v1.8.15 // indirect
	google.golang.org/protobuf v1.36.12 // indirect
)

replace (
	github.com/vlumi/wrinkle/clients/go => ../clients/go
	github.com/vlumi/wrinkle/proto/go => ../proto/go
)
