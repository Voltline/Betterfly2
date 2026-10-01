package utils

import (
	"errors"
	"unicode/utf8"
)

const (
	MaxCaptionCodePoints = 4096
	MaxCaptionBytes      = 16 * 1024
)

var ErrInvalidCaption = errors.New("图片配文无效：仅image支持配文，须为有效UTF-8且不超过4096字符及16KiB")

func ValidateImageCaption(messageType, caption string) error {
	if caption == "" {
		return nil
	}
	if messageType != "image" || len(caption) > MaxCaptionBytes || !utf8.ValidString(caption) || utf8.RuneCountInString(caption) > MaxCaptionCodePoints {
		return ErrInvalidCaption
	}
	return nil
}
