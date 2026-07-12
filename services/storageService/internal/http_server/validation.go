package http_server

import (
	"encoding/hex"
	"strings"
)

const sha512HexLength = 128
const maxStorageRequestBodyBytes = 64 << 10

func normalizeFileHash(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha512HexLength {
		return "", false
	}
	decoded, err := hex.DecodeString(value)
	return value, err == nil && len(decoded) == sha512HexLength/2
}
