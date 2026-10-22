package db

import (
	"strings"
	"testing"

	"gorm.io/gorm"
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

func TestSetUserJwtKeyIfEmptyPostgres(t *testing.T) {
	database := discussionPostgres(t)
	original := DB
	DB = func(...interface{}) *gorm.DB { return database }
	t.Cleanup(func() { DB = original })
	if err := database.Create(&User{ID: 1, Account: "legacy", Name: "user"}).Error; err != nil {
		t.Fatal(err)
	}
	first := []byte(strings.Repeat("a", 32))
	if set, err := SetUserJwtKeyIfEmpty(1, first); err != nil || !set {
		t.Fatalf("first set = %v, %v", set, err)
	}
	if set, err := SetUserJwtKeyIfEmpty(1, []byte(strings.Repeat("b", 32))); err != nil || set {
		t.Fatalf("second set overwrote key: %v, %v", set, err)
	}
	user, err := GetUserById(1)
	if err != nil || string(user.JwtKey) != string(first) {
		t.Fatalf("stored key = %q, %v", user.JwtKey, err)
	}
	// 空字节串同样视为没有密钥。
	if err := database.Model(&User{}).Where("id = ?", 1).Update("jwt_key", []byte{}).Error; err != nil {
		t.Fatal(err)
	}
	if set, err := SetUserJwtKeyIfEmpty(1, first); err != nil || !set {
		t.Fatalf("set over empty key = %v, %v", set, err)
	}
}
