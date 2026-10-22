package utils

import (
	"strings"
	"testing"
)

func TestValidText(t *testing.T) {
	cases := []struct {
		value string
		want  bool
	}{
		{"", true},
		{strings.Repeat("字", 10), true},
		{strings.Repeat("字", 11), false},
		{"a\x00b", false},
		{"\xff", false},
	}
	for _, c := range cases {
		if got := ValidText(c.value, 10); got != c.want {
			t.Errorf("ValidText(%q) = %v, want %v", c.value, got, c.want)
		}
	}
}
