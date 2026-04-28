package mq

import (
	envelope "Betterfly2/proto/envelope"
	"testing"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/wrapperspb"
)

func TestMarshalEnvelope(t *testing.T) {
	envBytes, err := MarshalEnvelope(envelope.MessageType_STORAGE_RESPONSE, wrapperspb.String("payload"))
	if err != nil {
		t.Fatalf("MarshalEnvelope returned error: %v", err)
	}

	env := &envelope.Envelope{}
	if err := proto.Unmarshal(envBytes, env); err != nil {
		t.Fatalf("failed to unmarshal envelope: %v", err)
	}
	if env.GetType() != envelope.MessageType_STORAGE_RESPONSE {
		t.Fatalf("unexpected envelope type: %v", env.GetType())
	}
	if len(env.GetPayload()) == 0 {
		t.Fatal("expected payload bytes")
	}
}

func TestPublishEnvelope(t *testing.T) {
	var gotTopic string
	var gotMessage string

	envBytes, err := PublishEnvelope(func(message string, targetTopic string) error {
		gotMessage = message
		gotTopic = targetTopic
		return nil
	}, "target-topic", envelope.MessageType_FRIEND_RESPONSE, wrapperspb.String("payload"))
	if err != nil {
		t.Fatalf("PublishEnvelope returned error: %v", err)
	}
	if gotTopic != "target-topic" {
		t.Fatalf("unexpected topic: %s", gotTopic)
	}
	if gotMessage != string(envBytes) {
		t.Fatal("published message does not match returned envelope")
	}
}
