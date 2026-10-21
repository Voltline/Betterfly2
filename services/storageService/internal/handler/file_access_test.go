package handler

import (
	storage "Betterfly2/proto/storage"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
)

func expectNoFileGrant(mock sqlmock.Sqlmock, fileHash string, userID int64) {
	mock.ExpectQuery(`(?s)SELECT count\(\*\) FROM file_access_grants AS g JOIN file_metadata AS f .*is_verified = TRUE.*g.file_hash = \$1 AND g.user_id = \$2`).
		WithArgs(fileHash, userID).WillReturnRows(sqlmock.NewRows([]string{"count"}).AddRow(0))
}

func TestFileMessagesRequireSenderGrantWithoutStoring(t *testing.T) {
	fileHash := strings.Repeat("c", 128)
	for _, messageType := range []string{"image", "gif", "video", "audio", "voice", "file"} {
		database, mock := setupMockDB(t)
		expectNoFileGrant(mock, fileHash, 1)
		resp, err := (&StorageHandler{}).handleStoreNewMessageWithDB(database, &storage.RequestMessage{TargetUserId: 1},
			&storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, Content: fileHash + ".gif", MessageType: messageType, RealFileName: "a.gif", ClientMessageId: "m-" + messageType}, nil)
		if err != nil || resp.GetResult() != storage.StorageResult_FORBIDDEN {
			t.Fatalf("%s referencing an unowned file was accepted: %v %v", messageType, resp, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}

func TestFileMessagesRejectMalformedOrForeignSenderWithoutQuery(t *testing.T) {
	valid := strings.Repeat("d", 128)
	cases := []struct {
		name    string
		content string
		target  int64
		result  storage.StorageResult
	}{
		{"not a hash", "hello", 1, storage.StorageResult_INVALID_ARGUMENT},
		{"uppercase hash", strings.ToUpper(valid) + ".jpg", 1, storage.StorageResult_INVALID_ARGUMENT},
		{"empty", "", 1, storage.StorageResult_INVALID_ARGUMENT},
		{"sender mismatch", valid + ".jpg", 3, storage.StorageResult_FORBIDDEN},
	}
	for _, tc := range cases {
		database, mock := setupMockDB(t)
		resp, err := (&StorageHandler{}).handleStoreNewMessageWithDB(database, &storage.RequestMessage{TargetUserId: tc.target},
			&storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, Content: tc.content, MessageType: "image"}, nil)
		if err != nil || resp.GetResult() != tc.result {
			t.Fatalf("%s: got %v %v", tc.name, resp, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
	}
}

func TestAvatarRequiresOwnerGrantWithoutUpdating(t *testing.T) {
	fileHash := strings.Repeat("e", 128)
	for _, avatar := range []string{fileHash, "not-a-hash"} {
		database, mock := setupMockDB(t)
		if avatar == fileHash {
			expectNoFileGrant(mock, fileHash, 1000)
		}
		var cacheKeys []string
		resp, err := (&StorageHandler{l1Cache: newMockCache()}).handleUpdateUserAvatarWithDB(database, &storage.RequestMessage{TargetUserId: 1000},
			&storage.UpdateUserAvatar{UserId: 1000, NewAvatarUrl: avatar}, &cacheKeys)
		if err != nil || resp.GetResult() != storage.StorageResult_FORBIDDEN || len(cacheKeys) != 0 {
			t.Fatalf("avatar %q without a grant was accepted: %v %v", avatar, resp, err)
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Fatal(err)
		}
	}
}
