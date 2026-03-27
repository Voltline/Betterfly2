package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	storage "Betterfly2/proto/storage"
	"testing"
)

func TestBuildSyncMessagesStorageRequestRoutesResponseToRequester(t *testing.T) {
	payload := &pb.QuerySyncMessages{
		ToUserId:  2002,
		Timestamp: "2026-03-27T08:00:00Z",
	}

	storeReq := buildSyncMessagesStorageRequest(1001, payload, "df-pod-1")

	if storeReq.FromKafkaTopic != "df-pod-1" {
		t.Fatalf("expected FromKafkaTopic to be df-pod-1, got %q", storeReq.FromKafkaTopic)
	}
	if storeReq.TargetUserId != 1001 {
		t.Fatalf("expected TargetUserId to be requester 1001, got %d", storeReq.TargetUserId)
	}

	queryPayload, ok := storeReq.Payload.(*storage.RequestMessage_QuerySyncMessages)
	if !ok {
		t.Fatalf("expected QuerySyncMessages payload, got %T", storeReq.Payload)
	}
	if queryPayload.QuerySyncMessages.GetToUserId() != 2002 {
		t.Fatalf("expected sync query target 2002, got %d", queryPayload.QuerySyncMessages.GetToUserId())
	}
	if queryPayload.QuerySyncMessages.GetTimestamp() != "2026-03-27T08:00:00Z" {
		t.Fatalf("unexpected timestamp: %q", queryPayload.QuerySyncMessages.GetTimestamp())
	}
}
