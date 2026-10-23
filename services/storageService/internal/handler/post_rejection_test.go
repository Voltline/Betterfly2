package handler

import (
	storage "Betterfly2/proto/storage"
	"testing"
)

func TestRejectedStoreMessageCarriesClientMessageIDOnly(t *testing.T) {
	cases := []struct {
		name    string
		target  int64
		message *storage.StoreNewMessage
		result  storage.StorageResult
	}{
		{"invalid caption", 1, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "text", Content: "hi", Caption: "x", ClientMessageId: "c-1"}, storage.StorageResult_INVALID_ARGUMENT},
		{"NUL content", 1, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "text", Content: "a\x00b", ClientMessageId: "c-2"}, storage.StorageResult_INVALID_ARGUMENT},
		{"reply by other requester", 3, &storage.StoreNewMessage{FromUserId: 1, ToUserId: 2, MessageType: "text", Content: "hi", ReplyToMessageId: 9, ClientMessageId: "c-3"}, storage.StorageResult_FORBIDDEN},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			mock := useMockDB(t)
			h := &StorageHandler{}
			resp, err := h.handleStoreNewMessageWithDB(h.requestDatabase(), &storage.RequestMessage{TargetUserId: tc.target}, tc.message, nil)
			if err != nil || resp.GetResult() != tc.result || resp.GetTargetUserId() != tc.target {
				t.Fatalf("resp=%+v err=%v", resp, err)
			}
			stored := resp.GetStoreMsgRsp()
			if stored == nil || stored.GetClientMessageId() != tc.message.GetClientMessageId() || stored.GetMessageId() != 0 || stored.GetCreated() {
				t.Fatalf("rejection payload = %+v", stored)
			}
			if stored.GetFromUserId() != 1 || stored.GetToUserId() != 2 || stored.GetContent() != "" || stored.GetCaption() != "" {
				t.Fatalf("rejection payload leaked or lost fields: %+v", stored)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Fatal(err)
			}
		})
	}
}
