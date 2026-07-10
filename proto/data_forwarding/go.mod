module df_interface

require (
	Betterfly2/proto/call v0.0.0
	google.golang.org/protobuf v1.36.6
)

go 1.24

toolchain go1.24.4

replace Betterfly2/proto/call => ../call
