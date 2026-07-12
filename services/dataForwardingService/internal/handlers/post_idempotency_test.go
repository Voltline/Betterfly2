package handlers

import (
	"strings"
	"testing"

	pb "Betterfly2/proto/data_forwarding"
)

func TestEnsurePostClientMessageIDPreservesExplicitID(t *testing.T) {
	post := &pb.Post{ClientMessageId: "  client-42  "}
	if got := ensurePostClientMessageID(post); got != "client-42" || post.GetClientMessageId() != "client-42" {
		t.Fatalf("explicit client message ID was not normalized: %q", got)
	}
}

func TestEnsurePostClientMessageIDCreatesStableLegacyID(t *testing.T) {
	post := &pb.Post{
		FromId: 1, ToId: 2, Msg: "hello", MsgType: "text", Timestamp: "2026-07-12T09:00:00Z",
	}
	first := ensurePostClientMessageID(post)
	post.ClientMessageId = ""
	second := ensurePostClientMessageID(post)

	if first != second || !strings.HasPrefix(first, "legacy:") {
		t.Fatalf("legacy ID must be stable: first=%q second=%q", first, second)
	}
	post.ClientMessageId = ""
	post.Msg = "different"
	if got := ensurePostClientMessageID(post); got == first {
		t.Fatal("different messages received the same legacy ID")
	}
}

func TestValidatePostPayloadRejectsOversizedClientMessageID(t *testing.T) {
	post := &pb.Post{Msg: "hello", MsgType: "text", ClientMessageId: strings.Repeat("x", 129)}
	if err := validatePostPayload(post); err == nil {
		t.Fatal("expected oversized client_message_id to be rejected")
	}
}

func TestPostIdempotencyKeyScopesBySender(t *testing.T) {
	first := postIdempotencyKey(1, "same-id")
	if second := postIdempotencyKey(2, "same-id"); first == second {
		t.Fatal("idempotency keys must be scoped by sender")
	}
}
