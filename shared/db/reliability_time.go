package db

import "time"

const ReliabilityTimeLayout = "2006-01-02T15:04:05.000000Z"

// FormatReliabilityTime is fixed-width so varchar lease and retry columns retain
// chronological ordering under ordinary PostgreSQL string comparison.
func FormatReliabilityTime(value time.Time) string {
	return value.UTC().Format(ReliabilityTimeLayout)
}
