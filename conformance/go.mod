module github.com/vlumi/wrinkle/conformance

go 1.27

require (
	github.com/coder/websocket v1.8.15
	github.com/vlumi/wrinkle/clients/go v0.0.0
	github.com/vlumi/wrinkle/proto/go v0.0.0
	google.golang.org/protobuf v1.36.12
)

replace (
	github.com/vlumi/wrinkle/clients/go => ../clients/go
	github.com/vlumi/wrinkle/proto/go => ../proto/go
)
