package db

import "testing"

func TestBuildSyncMessagesPageBoundaries(t *testing.T) {
	for _, test := range []struct {
		name     string
		messages []Message
		wantLen  int
		hasMore  bool
		cursorID int64
	}{
		{name: "empty"},
		{name: "less than page", messages: []Message{{MessageID: 1, Timestamp: "t1"}}, wantLen: 1, cursorID: 1},
		{name: "exact page", messages: []Message{{MessageID: 1, Timestamp: "t1"}, {MessageID: 2, Timestamp: "t2"}}, wantLen: 2, cursorID: 2},
		{name: "limit plus one", messages: []Message{{MessageID: 1, Timestamp: "t1"}, {MessageID: 2, Timestamp: "t2"}, {MessageID: 3, Timestamp: "t3"}}, wantLen: 2, hasMore: true, cursorID: 2},
	} {
		t.Run(test.name, func(t *testing.T) {
			page := buildSyncMessagesPage(test.messages, 2)
			if len(page.Messages) != test.wantLen || page.HasMore != test.hasMore || page.NextCursorMessageID != test.cursorID {
				t.Fatalf("unexpected page: %+v", page)
			}
		})
	}
}
