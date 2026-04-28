package handlers

import (
	pb "Betterfly2/proto/data_forwarding"
	"strings"
	"testing"
)

func TestAuthenticatedPayloadRejectsMissingJWT(t *testing.T) {
	_, err := authenticatedPayload(
		1001,
		&pb.RequestMessage{Payload: &pb.RequestMessage_Post{Post: &pb.Post{}}},
		"转发消息",
		"post",
		(*pb.RequestMessage).GetPost,
	)
	if err == nil {
		t.Fatal("expected missing JWT error")
	}
	if !strings.Contains(err.Error(), "用户未携带有效JWT") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestIsNilPayloadRecognizesTypedNil(t *testing.T) {
	var post *pb.Post
	if !isNilPayload(post) {
		t.Fatal("expected typed nil pointer to be treated as nil")
	}
	if isNilPayload(&pb.Post{}) {
		t.Fatal("expected non-nil pointer to be treated as present")
	}
}

func TestIDGuards(t *testing.T) {
	if err := requirePositiveID("target_user_id", 0); err == nil {
		t.Fatal("expected non-positive id to be rejected")
	}
	if err := requirePositiveID("target_user_id", 1001); err != nil {
		t.Fatalf("expected positive id to pass: %v", err)
	}
	if err := requireNonSelfID("to_delete_user_id", 1001, 1001); err == nil {
		t.Fatal("expected self id to be rejected")
	}
}
