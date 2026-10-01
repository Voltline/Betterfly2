package utils

import (
	"errors"
	"strings"
	"testing"
)

func TestImageCaptionValidation(t *testing.T) {
	for _, test := range []struct {
		name, kind, caption string
		valid               bool
	}{
		{"legacy empty", "text", "", true},
		{"raw markdown", "image", "  # 公告\n**内容** 😀\n末尾  ", true},
		{"4096 ascii", "image", strings.Repeat("a", 4096), true},
		{"4096 unicode", "image", strings.Repeat("中", 4096), true},
		{"16KiB", "image", strings.Repeat("😀", 4096), true},
		{"code points overflow", "image", strings.Repeat("a", 4097), false},
		{"bytes overflow", "image", strings.Repeat("😀", 4097), false},
		{"invalid utf8", "image", string([]byte{0xff}), false},
		{"text with caption", "text", "caption", false},
		{"gif with caption", "gif", "caption", false},
		{"whitespace nonimage", "video", "\n", false},
	} {
		t.Run(test.name, func(t *testing.T) {
			err := ValidateImageCaption(test.kind, test.caption)
			if (err == nil) != test.valid || (err != nil && !errors.Is(err, ErrInvalidCaption)) {
				t.Fatalf("valid=%t err=%v", test.valid, err)
			}
		})
	}
}
