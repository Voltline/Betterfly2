package db

import (
	"strings"
	"testing"
)

func TestFileHashFromContent(t *testing.T) {
	valid := strings.Repeat("0123abcd", 16)
	cases := []struct {
		content string
		hash    string
		ok      bool
	}{
		{valid, valid, true},
		{valid + ".gif", valid, true},
		{valid + ".tar.gz", valid, true},
		{strings.ToUpper(valid) + ".jpg", strings.ToUpper(valid), false},
		{valid[:127] + ".jpg", valid[:127], false},
		{strings.Repeat("g", 128), strings.Repeat("g", 128), false},
		{"", "", false},
		{"../" + valid, "", false},
	}
	for _, tc := range cases {
		hash, ok := FileHashFromContent(tc.content)
		if ok != tc.ok || (tc.hash != "" && hash != tc.hash) {
			t.Fatalf("%q: got %q %v", tc.content, hash, ok)
		}
	}
}

func fileAccessHash(seed byte) string {
	return strings.Repeat(string(seed), 128)
}

func TestCanUseFilePostgresRequiresVerifiedGrant(t *testing.T) {
	database := discussionPostgres(t)
	fileHash := fileAccessHash('a')
	if err := UpsertPendingFileMetadataWithDB(database, fileHash, 3, "aa/"+fileHash); err != nil {
		t.Fatal(err)
	}
	if err := GrantFileAccessWithDB(database, fileHash, 1); err != nil {
		t.Fatal(err)
	}
	if ok, err := CanUseFileWithDB(database, fileHash, 1); err != nil || ok {
		t.Fatalf("unverified file was usable: %v %v", ok, err)
	}
	if err := StoreVerifiedFileWithDB(database, fileHash, 3, "aa/"+fileHash, 1); err != nil {
		t.Fatal(err)
	}
	for userID, want := range map[int64]bool{1: true, 2: false, 0: false} {
		if ok, err := CanUseFileWithDB(database, fileHash, userID); err != nil || ok != want {
			t.Fatalf("user %d: got %v err=%v", userID, ok, err)
		}
	}
	if ok, err := CanUseFileWithDB(database, strings.ToUpper(fileHash), 1); err != nil || ok {
		t.Fatalf("uppercase hash matched: %v %v", ok, err)
	}
}
