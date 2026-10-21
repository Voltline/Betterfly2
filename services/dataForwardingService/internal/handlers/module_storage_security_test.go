package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"errors"
	"testing"
)

func TestSyncMessagesCanOnlyBeRequestedForAuthenticatedUser(t *testing.T) {
	payload := func(target int64) *pb.QuerySyncMessages {
		return &pb.QuerySyncMessages{
			ToUserId:               target,
			Timestamp:              "2026-07-13T00:00:00Z",
			PageSize:               250,
			CursorTimestamp:        "2026-07-13T01:00:00Z",
			CursorMessageId:        42,
			IncludeRecalledChanges: true,
			RecallCursorTimestamp:  "2026-07-13T00:01:00Z",
			RecallCursorMessageId:  41,
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
		if !query.GetIncludeRecalledChanges() || query.GetRecallCursorTimestamp() != "2026-07-13T00:01:00Z" || query.GetRecallCursorMessageId() != 41 {
			t.Fatalf("recall cursor lost: %+v", query)
		}
	}
}

func TestProfileUpdatesCanOnlyTargetAuthenticatedUser(t *testing.T) {
	if req, err := prepareUpdateUserNameStorageRequest(1001, &pb.UpdateUserName{UserId: 2002, NewUserName: "x"}, "df-pod-1"); err == nil || req != nil {
		t.Fatalf("requester A renamed user B: req=%+v err=%v", req, err)
	}
	if req, err := prepareUpdateUserAvatarStorageRequest(1001, &pb.UpdateUserAvatar{UserId: 2002, NewAvatarUrl: "x"}, "df-pod-1"); err == nil || req != nil {
		t.Fatalf("requester A changed user B's avatar: req=%+v err=%v", req, err)
	}

	for _, target := range []int64{0, 1001} {
		nameReq, err := prepareUpdateUserNameStorageRequest(1001, &pb.UpdateUserName{UserId: target, NewUserName: "alice"}, "df-pod-1")
		if err != nil {
			t.Fatalf("own rename with user_id %d was rejected: %v", target, err)
		}
		if nameReq.GetTargetUserId() != 1001 || nameReq.GetUpdateUserName().GetUserId() != 1001 || nameReq.GetUpdateUserName().GetNewUserName() != "alice" {
			t.Fatalf("rename identity was not forced to authenticated user: %+v", nameReq)
		}
		avatarReq, err := prepareUpdateUserAvatarStorageRequest(1001, &pb.UpdateUserAvatar{UserId: target, NewAvatarUrl: "hash"}, "df-pod-1")
		if err != nil {
			t.Fatalf("own avatar update with user_id %d was rejected: %v", target, err)
		}
		if avatarReq.GetTargetUserId() != 1001 || avatarReq.GetUpdateUserAvatar().GetUserId() != 1001 || avatarReq.GetUpdateUserAvatar().GetNewAvatarUrl() != "hash" {
			t.Fatalf("avatar identity was not forced to authenticated user: %+v", avatarReq)
		}
	}
}

func TestForgedProfileUpdateReturnsWarn(t *testing.T) {
	_, err := prepareUpdateUserNameStorageRequest(1001, &pb.UpdateUserName{UserId: 2002}, "df-pod-1")
	result, err := profileUpdateResult(err)
	if err != nil {
		t.Fatalf("forged update should be answered with a warning, got error %v", err)
	}
	if result.response.GetWarn().GetWarningMessage() != errProfileForbidden.Error() {
		t.Fatalf("expected forbidden warning, got %+v", result.response)
	}

	other := errors.New("kafka unavailable")
	if result, err := profileUpdateResult(other); !errors.Is(err, other) || result.response != nil {
		t.Fatalf("non-authorization errors must pass through unchanged: result=%+v err=%v", result, err)
	}
}
