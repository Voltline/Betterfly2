package utils

import "errors"

const (
	MaxCaptionCodePoints = 4096
	MaxCaptionBytes      = 16 * 1024
)

var ErrInvalidCaption = errors.New("图片配文无效：仅image支持配文，须为有效UTF-8、不含NUL且不超过4096字符及16KiB")

func ValidateImageCaption(messageType, caption string) error {
	if caption == "" {
		return nil
	}
	if messageType != "image" || len(caption) > MaxCaptionBytes || !ValidText(caption, MaxCaptionCodePoints) {
		return ErrInvalidCaption
	}
	return nil
}
