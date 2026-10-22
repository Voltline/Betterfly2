package utils

import (
	"strings"
	"unicode/utf8"
)

// ValidText 校验写入Postgres文本列的用户输入：须为合法UTF-8、不含NUL（Postgres文本类型不接受），
// 且不超过maximum个字符（与varchar(n)一样按Unicode码点计）。
func ValidText(value string, maximum int) bool {
	return utf8.ValidString(value) && !strings.ContainsRune(value, 0) && utf8.RuneCountInString(value) <= maximum
}
