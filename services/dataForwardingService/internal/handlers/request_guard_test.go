package handlers

import (
	callpb "Betterfly2/proto/call"
	pb "Betterfly2/proto/data_forwarding"
	pushpb "Betterfly2/proto/push"
	"google.golang.org/protobuf/proto"
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

func TestCallRequestRejectsMissingJWT(t *testing.T) {
	request := &pb.RequestMessage{
		Payload: &pb.RequestMessage_CallRequest{CallRequest: &callpb.ClientRequest{
			Payload: &callpb.ClientRequest_GetConfig{GetConfig: &callpb.GetCallConfig{}},
		}},
	}
	_, err := authenticatedPayload(1001, request, "操作通话", "call_request", (*pb.RequestMessage).GetCallRequest)
	if err == nil || !strings.Contains(err.Error(), "用户未携带有效JWT") {
		t.Fatalf("expected unauthenticated call request to be rejected, got %v", err)
	}
}

func TestGroupCallRequestUsesExistingAuthenticatedEnvelope(t *testing.T) {
	request := &pb.RequestMessage{Payload: &pb.RequestMessage_CallRequest{CallRequest: &callpb.ClientRequest{RequestId: "join-id", Payload: &callpb.ClientRequest_JoinGroupCall{JoinGroupCall: &callpb.JoinGroupCall{CallId: strings.Repeat("a", 32)}}}}}
	if _, err := authenticatedPayload(1001, request, "操作通话", "call_request", (*pb.RequestMessage).GetCallRequest); err == nil {
		t.Fatal("group call bypassed JWT guard")
	}
	if request.GetCallRequest().GetJoinGroupCall().GetCallId() != strings.Repeat("a", 32) || request.GetCallRequest().GetRequestId() != "join-id" {
		t.Fatal("group payload changed")
	}
}

func TestGroupCallResponseSurvivesExistingDFWireEnvelope(t *testing.T) {
	event := &callpb.CallEvent{EventType: callpb.CallEventType_GROUP_CALL_JOINED, CallId: strings.Repeat("a", 32), RequestId: "join-id", SfuUrl: "wss://media.test", JoinToken: "private-token", TokenExpiresAt: "2026-10-03T10:00:00Z", GroupCall: &callpb.GroupCallInfo{GroupId: 10, CreatorUserId: 1, CallType: callpb.CallType_VIDEO, State: callpb.CallState_ACTIVE, Revision: 3, Participants: []*callpb.GroupCallParticipant{{UserId: 1, Identity: "opaque", Connected: false}}}}
	raw, err := proto.Marshal(&pb.ResponseMessage{Payload: &pb.ResponseMessage_CallEvent{CallEvent: event}})
	if err != nil {
		t.Fatal(err)
	}
	var response pb.ResponseMessage
	if err := proto.Unmarshal(raw, &response); err != nil || !proto.Equal(event, response.GetCallEvent()) {
		t.Fatal("DF response dropped SFU fields")
	}
}

func TestPushRequestRejectsMissingJWT(t *testing.T) {
	request := &pb.RequestMessage{
		Payload: &pb.RequestMessage_PushRequest{PushRequest: &pushpb.ClientRequest{
			Payload: &pushpb.ClientRequest_RegisterVoipToken{RegisterVoipToken: &pushpb.RegisterVoIPToken{}},
		}},
	}
	_, err := authenticatedPayload(1001, request, "管理推送设备", "push_request", (*pb.RequestMessage).GetPushRequest)
	if err == nil || !strings.Contains(err.Error(), "用户未携带有效JWT") {
		t.Fatalf("expected unauthenticated push request to be rejected, got %v", err)
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
