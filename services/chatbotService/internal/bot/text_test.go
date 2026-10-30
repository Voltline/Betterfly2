package bot

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestSanitizeReply(t *testing.T) {
	cases := map[string]string{
		"<think>想一想</think>\n\n答案":   "答案",
		"答案<think>没写完":               "答案",
		"a\x00b\r\nc\x07":            "ab\nc",
		"一\n\n\n\n二":                 "一\n\n二",
		"  \t好\xff的  ":               "好的",
		"<think>只有推理</think>   \n  ": "",
	}
	for input, want := range cases {
		if got := sanitizeReply(input); got != want {
			t.Errorf("sanitizeReply(%q) = %q, want %q", input, got, want)
		}
	}
}

func TestSplitReplyPrefersParagraphsAndSentences(t *testing.T) {
	first := strings.Repeat("甲", 60)
	second := strings.Repeat("乙", 60)
	parts := splitReply(first+"\n\n"+second, 100, 4)
	if len(parts) != 2 || parts[0] != first || parts[1] != second {
		t.Fatalf("paragraph split = %q", parts)
	}
	sentence := strings.Repeat("丙", 70) + "。" + strings.Repeat("丁", 50)
	parts = splitReply(sentence, 100, 4)
	if len(parts) != 2 || !strings.HasSuffix(parts[0], "。") {
		t.Fatalf("sentence split = %q", parts)
	}
	hard := strings.Repeat("戊", 250)
	parts = splitReply(hard, 100, 4)
	if len(parts) != 3 || utf8.RuneCountInString(parts[0]) != 100 || utf8.RuneCountInString(parts[2]) != 50 {
		t.Fatalf("hard split lengths = %d parts", len(parts))
	}
}

func TestSplitReplyTruncatesExtraParts(t *testing.T) {
	parts := splitReply(strings.Repeat("字", 1000), 100, 3)
	if len(parts) != 3 {
		t.Fatalf("parts = %d", len(parts))
	}
	last := parts[2]
	if utf8.RuneCountInString(last) != 100 || !strings.HasSuffix(last, "…") {
		t.Fatalf("last part = %d runes %q", utf8.RuneCountInString(last), last[len(last)-6:])
	}
	for _, part := range parts {
		if utf8.RuneCountInString(part) > 100 {
			t.Fatalf("part too long: %d", utf8.RuneCountInString(part))
		}
	}
	if parts := splitReply("短", 100, 1); len(parts) != 1 || parts[0] != "短" {
		t.Fatalf("single part = %q", parts)
	}
}
