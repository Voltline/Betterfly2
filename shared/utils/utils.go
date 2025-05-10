package utils

import "time"

func NowTime() string {
	return time.Now().UTC().Format(time.RFC3339)
}
