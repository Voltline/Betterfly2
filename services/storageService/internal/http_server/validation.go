package http_server

import (
	"encoding/hex"
	"strings"
)

const sha512HexLength = 128
const maxStorageRequestBodyBytes = 64 << 10

// maxUploadFileBytes 与客户端 UploadFilePolicy 的主App上限一致，正常客户端不会触发。
const maxUploadFileBytes int64 = 512 << 20

func normalizeFileHash(value string) (string, bool) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) != sha512HexLength {
		return "", false
	}
	decoded, err := hex.DecodeString(value)
	return value, err == nil && len(decoded) == sha512HexLength/2
}
