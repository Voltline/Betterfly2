package Utils

import (
	"encoding/base64"
	"regexp"
)

func Enbase64(message string) []byte {
	message = base64.StdEncoding.EncodeToString([]byte(message))
	message = "-S-" + message + "-E-"
	return []byte(message)
}

func Debase64(message []byte) []string {
	messageString := string(message)
	rgxp, err := regexp.Compile("-S-([^-]*?)-E-")
	if err != nil {
		return []string{}
	}

	result := make([]string, 0)

	for _, match := range rgxp.FindAllStringSubmatch(messageString, -1) {
		decodeString, err := base64.StdEncoding.DecodeString(match[1])
		if err != nil {
			return []string{}
		}
		result = append(result, string(decodeString))
	}
	return result
}

func Encode(message string) []byte {
	return Enbase64(message)
}

func Decode(message []byte) []string {
	return Debase64(message)
}
