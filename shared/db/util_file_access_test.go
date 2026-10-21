package db

import (
	"fmt"
	"strings"
	"testing"

	"gorm.io/gorm"
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

func storeFileAccessMessage(t *testing.T, database *gorm.DB, message Message) Message {
	t.Helper()
	if message.Timestamp == "" {
		message.Timestamp = "2026-03-01T00:00:00Z"
	}
	if err := database.Create(&message).Error; err != nil {
		t.Fatal(err)
	}
	return message
}

func assertDownload(t *testing.T, database *gorm.DB, fileHash string, want map[int64]bool) {
	t.Helper()
	for userID, allowed := range want {
		got, err := CanDownloadFileWithDB(database, fileHash, userID)
		if err != nil || got != allowed {
			t.Fatalf("file %s… user %d: got %v err=%v, want %v", fileHash[:4], userID, got, err, allowed)
		}
	}
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

func TestCanDownloadFilePostgresFollowsMessageVisibility(t *testing.T) {
	database := discussionPostgres(t)
	// 用户 1-4；公开频道 9001 与讨论群 9002 均由 1 管理。
	discussionFixture(t, database)
	for id := int64(5); id <= 7; id++ {
		if err := database.Create(&User{ID: id, Account: fmt.Sprintf("u%d", id), Name: "user"}).Error; err != nil {
			t.Fatal(err)
		}
	}

	granted := fileAccessHash('1')
	if err := GrantFileAccessWithDB(database, granted, 5); err != nil {
		t.Fatal(err)
	}
	assertDownload(t, database, granted, map[int64]bool{5: true, 6: false, 0: false})

	userAvatar, groupAvatar := fileAccessHash('2'), fileAccessHash('3')
	database.Model(&User{}).Where("id = ?", 2).Update("avatar", userAvatar)
	database.Model(&Group{}).Where("group_id = ?", 9002).Update("avatar", groupAvatar)
	assertDownload(t, database, userAvatar, map[int64]bool{7: true})
	assertDownload(t, database, groupAvatar, map[int64]bool{7: true})

	private := fileAccessHash('4')
	storeFileAccessMessage(t, database, Message{FromUserID: 1, ToUserID: 2, Content: private + ".jpg", MessageType: "image"})
	assertDownload(t, database, private, map[int64]bool{1: true, 2: true, 3: false})
	// 文本消息里出现哈希不构成文件引用。
	textOnly := fileAccessHash('5')
	storeFileAccessMessage(t, database, Message{FromUserID: 1, ToUserID: 2, Content: textOnly, MessageType: "text"})
	assertDownload(t, database, textOnly, map[int64]bool{2: false})

	// 普通群：入群不晚于消息才能看到；退群后成员失去权限，发送者本人保留。
	if err := database.Create(&Group{GroupID: 9100, Name: "group", OwnerUserID: 3, UpdateTime: "2026-01-01T00:00:00Z"}).Error; err != nil {
		t.Fatal(err)
	}
	for userID, joinedAt := range map[int64]string{3: "2026-01-01T00:00:00Z", 4: "2026-01-01T00:00:00Z", 5: "2026-06-01T00:00:00Z"} {
		if err := database.Create(&GroupMember{GroupID: 9100, UserID: userID, Role: "member", JoinedAt: joinedAt, UpdateTime: joinedAt}).Error; err != nil {
			t.Fatal(err)
		}
	}
	groupFile := fileAccessHash('6')
	storeFileAccessMessage(t, database, Message{FromUserID: 4, ToUserID: 9100, Content: groupFile + ".gif", MessageType: "gif", IsGroup: true})
	assertDownload(t, database, groupFile, map[int64]bool{3: true, 4: true, 5: false, 6: false})
	if err := database.Where("group_id = ? AND user_id IN ?", 9100, []int64{3, 4}).Delete(&GroupMember{}).Error; err != nil {
		t.Fatal(err)
	}
	assertDownload(t, database, groupFile, map[int64]bool{3: false, 4: true})

	// 撤回后该消息不再授权，但同一文件在其他可见消息中仍可下载。
	recalled := fileAccessHash('7')
	first := storeFileAccessMessage(t, database, Message{FromUserID: 1, ToUserID: 2, Content: recalled + ".mp4", MessageType: "video"})
	database.Model(&Message{}).Where("message_id = ?", first.MessageID).Update("is_recalled", true)
	assertDownload(t, database, recalled, map[int64]bool{1: false, 2: false})
	storeFileAccessMessage(t, database, Message{FromUserID: 3, ToUserID: 2, Content: recalled + ".mp4", MessageType: "video"})
	assertDownload(t, database, recalled, map[int64]bool{1: false, 2: true, 3: true})

	// 公开频道任何人可下载；私有频道只限订阅者；删除频道后全部失效。
	publicFile := fileAccessHash('8')
	storeFileAccessMessage(t, database, Message{FromUserID: 1, ToUserID: 9001, Content: publicFile + ".png", MessageType: "image", IsGroup: true})
	assertDownload(t, database, publicFile, map[int64]bool{7: true})
	if _, err := CreateChannelWithDB(database, 1, 9003, "private", "", "", "", false); err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&GroupMember{GroupID: 9003, UserID: 6, Role: "member", JoinedAt: "2026-09-01T00:00:00Z"}).Error; err != nil {
		t.Fatal(err)
	}
	privateChannelFile := fileAccessHash('9')
	storeFileAccessMessage(t, database, Message{FromUserID: 1, ToUserID: 9003, Content: privateChannelFile + ".pdf", MessageType: "file", IsGroup: true})
	assertDownload(t, database, privateChannelFile, map[int64]bool{6: true, 7: false})
	database.Model(&Group{}).Where("group_id = ?", 9003).Update("is_delete", true)
	assertDownload(t, database, privateChannelFile, map[int64]bool{6: false})

	// 讨论区：需要是讨论群成员，且来源频道仍可读。
	post, _, err := StoreNewMessageWithDB(database, 1, 9001, "post", "text", "", true, "file-post", "", 0)
	if err != nil {
		t.Fatal(err)
	}
	root, err := CreateDiscussionRootWithDB(database, post)
	if err != nil {
		t.Fatal(err)
	}
	if err := database.Create(&GroupMember{GroupID: 9002, UserID: 2, Role: "member", JoinedAt: "2026-09-01T00:00:00Z"}).Error; err != nil {
		t.Fatal(err)
	}
	discussionFile := fileAccessHash('b')
	storeFileAccessMessage(t, database, Message{FromUserID: 1, ToUserID: 9002, Content: discussionFile + ".m4a", MessageType: "voice", IsGroup: true, DiscussionRootMessageID: root.MessageID})
	assertDownload(t, database, discussionFile, map[int64]bool{2: true, 7: false})
}

func TestCanDownloadFilePostgresUsesFileHashIndex(t *testing.T) {
	database := discussionPostgres(t)
	var plan []string
	err := database.Transaction(func(tx *gorm.DB) error {
		if err := tx.Exec("SET LOCAL enable_seqscan = off").Error; err != nil {
			return err
		}
		userID := int64(1)
		return tx.Raw("EXPLAIN "+fileMessageReadableSQL, fileAccessHash('c'), userID, userID, userID, userID, userID, userID, userID).Scan(&plan).Error
	})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(strings.Join(plan, "\n"), "idx_messages_file_hash") {
		t.Fatalf("file message lookup does not use idx_messages_file_hash:\n%s", strings.Join(plan, "\n"))
	}
}
