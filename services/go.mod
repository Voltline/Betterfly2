module test_message_generator

go 1.24

toolchain go1.24.1

require (
	Betterfly2/proto/data_forwarding v0.0.0
	google.golang.org/protobuf v1.36.6
)

require (
	Betterfly2/proto/call v0.0.0 // indirect
	Betterfly2/proto/push v0.0.0 // indirect
)

replace (
	Betterfly2/proto => ../proto
	Betterfly2/proto/call => ../proto/call
	Betterfly2/proto/data_forwarding => ../proto/data_forwarding
	Betterfly2/proto/push => ../proto/push
)
