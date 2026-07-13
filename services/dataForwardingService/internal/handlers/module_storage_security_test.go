package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"testing"
)

func TestSyncMessagesCanOnlyBeRequestedForAuthenticatedUser(t *testing.T) {
	payload := func(target int64) *pb.QuerySyncMessages {
		return &pb.QuerySyncMessages{
			ToUserId:        target,
			Timestamp:       "2026-07-13T00:00:00Z",
			PageSize:        250,
			CursorTimestamp: "2026-07-13T01:00:00Z",
			CursorMessageId: 42,
		}
	}

	request, err := prepareSyncMessagesStorageRequest(1001, payload(2002), "df-pod-1")
	if err == nil {
		t.Fatal("requester A was allowed to sync user B")
	}
	if request != nil {
		t.Fatalf("forbidden sync produced a publishable request: %+v", request)
	}

	for _, target := range []int64{0, 1001} {
		req, err := prepareSyncMessagesStorageRequest(1001, payload(target), "df-pod-1")
		if err != nil {
			t.Fatalf("own sync target %d was rejected: %v", target, err)
		}
		if req.GetTargetUserId() != 1001 || req.GetQuerySyncMessages().GetToUserId() != 1001 {
			t.Fatalf("identity was not forced to authenticated user: %+v", req)
		}
		query := req.GetQuerySyncMessages()
		if query.GetPageSize() != 250 || query.GetCursorTimestamp() != "2026-07-13T01:00:00Z" || query.GetCursorMessageId() != 42 {
			t.Fatalf("pagination cursor was not preserved: %+v", query)
		}
	}
}
