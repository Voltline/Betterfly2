package bot

import (
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

var (
	thinkBlock     = regexp.MustCompile(`(?s)<think>.*?</think>`)
	excessNewlines = regexp.MustCompile(`\n{3,}`)
)

// sanitizeReply 去掉推理模型的<think>段、非法UTF-8和控制字符，结果满足存储层的文本校验。
func sanitizeReply(text string) string {
	text = thinkBlock.ReplaceAllString(strings.ToValidUTF8(text, ""), "")
	if index := strings.Index(text, "<think>"); index >= 0 {
		text = text[:index] // 被max_tokens截断的推理段没有结束标签
	}
	text = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r == '\r' || unicode.IsControl(r) {
			return -1
		}
		return r
	}, text)
	return strings.TrimSpace(excessNewlines.ReplaceAllString(text, "\n\n"))
}

// splitReply 把回复拆成不超过maxRunes的若干条消息，优先在段落、换行、句末处断开；
// 超过maxParts条时截断并以省略号结尾。
func splitReply(text string, maxRunes, maxParts int) []string {
	var parts []string
	runes := []rune(text)
	for len(runes) > 0 {
		if len(parts) == maxParts-1 && len(runes) > maxRunes {
			parts = append(parts, strings.TrimSpace(string(runes[:maxRunes-1]))+"…")
			break
		}
		if len(runes) <= maxRunes {
			parts = append(parts, string(runes))
			break
		}
		cut := splitPoint(runes, maxRunes)
		if part := strings.TrimSpace(string(runes[:cut])); part != "" {
			parts = append(parts, part)
		}
		runes = []rune(strings.TrimLeftFunc(string(runes[cut:]), unicode.IsSpace))
	}
	return parts
}

func splitPoint(runes []rune, maxRunes int) int {
	window := runes[:maxRunes]
	minCut := maxRunes / 2
	if index := lastIndex(window, "\n\n"); index >= minCut {
		return index + 2
	}
	if index := lastIndex(window, "\n"); index >= minCut {
		return index + 1
	}
	for i := maxRunes - 1; i >= minCut; i-- {
		if strings.ContainsRune("。！？!?；;", window[i]) || (window[i] == '.' && i+1 < len(runes) && unicode.IsSpace(runes[i+1])) {
			return i + 1
		}
	}
	for i := maxRunes - 1; i >= minCut; i-- {
		if unicode.IsSpace(window[i]) {
			return i + 1
		}
	}
	return maxRunes
}

func lastIndex(runes []rune, sep string) int {
	text := string(runes)
	index := strings.LastIndex(text, sep)
	if index < 0 {
		return -1
	}
	return utf8.RuneCountInString(text[:index])
}
