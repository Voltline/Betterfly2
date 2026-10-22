package db

import (
	"strings"
	"testing"
)

func TestNormalizeUserName(t *testing.T) {
	tests := []struct {
		in, want string
		ok       bool
	}{
		{in: "  小明\n", want: "小明", ok: true},
		{in: strings.Repeat("名", MaxNameLen), want: strings.Repeat("名", MaxNameLen), ok: true},
		{in: strings.Repeat("名", MaxNameLen+1)},
		{in: ""},
		{in: " \t　"},
		{in: "\xff"},
	}
	for _, tt := range tests {
		got, ok := NormalizeUserName(tt.in)
		if got != tt.want || ok != tt.ok {
			t.Fatalf("NormalizeUserName(%q) = %q, %v; want %q, %v", tt.in, got, ok, tt.want, tt.ok)
		}
	}
}
