module friendService

go 1.23.0

toolchain go1.24.1

require (
	Betterfly2/proto v0.0.0
	Betterfly2/shared v0.0.0
)

replace (
	Betterfly2/proto => ../../proto
	Betterfly2/shared => ../../shared
)

