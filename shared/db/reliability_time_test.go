package db

import (
	"sort"
	"testing"
	"time"
)

func TestReliabilityTimeIsFixedWidthAndLexicographicallySortable(t *testing.T) {
	base := time.Date(2026, 7, 15, 3, 0, 0, 0, time.UTC)
	chronological := []time.Time{
		base,
		base.Add(time.Microsecond),
		base.Add(100 * time.Millisecond),
		base.Add(900 * time.Millisecond),
		base.Add(time.Second),
	}
	formatted := make([]string, 0, len(chronological))
	for _, value := range chronological {
		formatted = append(formatted, FormatReliabilityTime(value))
	}
	for index := 1; index < len(formatted); index++ {
		if formatted[index-1] >= formatted[index] {
			t.Fatalf("reliability timestamps do not preserve order: %q >= %q", formatted[index-1], formatted[index])
		}
		if len(formatted[index-1]) != len(formatted[index]) {
			t.Fatalf("reliability timestamps are not fixed width: %q %q", formatted[index-1], formatted[index])
		}
	}
	reversed := append([]string(nil), formatted...)
	sort.Sort(sort.Reverse(sort.StringSlice(reversed)))
	sort.Strings(reversed)
	for index := range formatted {
		if reversed[index] != formatted[index] {
			t.Fatalf("lexicographic sort changed chronological order: got=%v want=%v", reversed, formatted)
		}
	}
}
