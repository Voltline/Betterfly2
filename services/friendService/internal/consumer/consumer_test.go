package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	envelope "Betterfly2/proto/envelope"
	friendpb "Betterfly2/proto/friend"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/kafkaconsumer/testutil"
	"Betterfly2/shared/mq"

	"github.com/IBM/sarama"
)

type fakeFriendHandler struct {
	calls    int
	failures int
}

func (h *fakeFriendHandler) HandleMessage(context.Context, []byte) error {
	h.calls++
	if h.calls <= h.failures {
		return errors.New("response publish failed")
	}
	return nil
}

func friendMessage(t *testing.T, offset int64) *sarama.ConsumerMessage {
	t.Helper()
	payload, err := mq.MarshalEnvelope(envelope.MessageType_FRIEND_REQUEST, &friendpb.RequestMessage{
		FromKafkaTopic: "df-a", TargetUserId: 1,
		Payload: &friendpb.RequestMessage_QueryFriendList{QueryFriendList: &friendpb.QueryFriendList{UserId: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sarama.ConsumerMessage{Topic: "friend-service", Partition: 1, Offset: offset, Value: payload}
}

func friendReliable(handler *KafkaConsumerGroupHandler, maxRetries int, publish kafkaconsumer.DLQPublisher) {
	handler.reliable = kafkaconsumer.New(kafkaconsumer.Config{
		Service: "friend", DLQTopic: "friend-service-dlq", MaxRetries: maxRetries,
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	}, handler.process, publish)
}

func TestFriendConsumerOffsetAndDLQSemantics(t *testing.T) {
	t.Run("response failure retries before commit", func(t *testing.T) {
		business := &fakeFriendHandler{failures: 1}
		handler := NewKafkaConsumerGroupHandler(business)
		friendReliable(handler, 2, nil)
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(friendMessage(t, 1))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d marked=%d", business.calls, len(session.Marked))
		}
	})

	t.Run("wrong envelope enters DLQ", func(t *testing.T) {
		business := &fakeFriendHandler{}
		handler := NewKafkaConsumerGroupHandler(business)
		var dlq int
		friendReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		payload, err := mq.MarshalEnvelope(envelope.MessageType_STORAGE_REQUEST, &friendpb.RequestMessage{})
		if err != nil {
			t.Fatal(err)
		}
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(&sarama.ConsumerMessage{Topic: "friend-service", Offset: 2, Value: payload})); err != nil {
			t.Fatal(err)
		}
		if dlq != 1 || business.calls != 0 || len(session.Marked) != 1 {
			t.Fatalf("dlq=%d calls=%d marked=%d", dlq, business.calls, len(session.Marked))
		}
	})

	t.Run("retry exhaustion commits only after DLQ", func(t *testing.T) {
		business := &fakeFriendHandler{failures: 10}
		handler := NewKafkaConsumerGroupHandler(business)
		var dlq int
		friendReliable(handler, 1, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(friendMessage(t, 3))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || dlq != 1 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d dlq=%d marked=%d", business.calls, dlq, len(session.Marked))
		}
	})

	t.Run("DLQ failure stops partition", func(t *testing.T) {
		business := &fakeFriendHandler{failures: 10}
		handler := NewKafkaConsumerGroupHandler(business)
		friendReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error { return errors.New("down") })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(friendMessage(t, 4), friendMessage(t, 5))); err == nil {
			t.Fatal("expected failure")
		}
		if business.calls != 1 || len(session.Marked) != 0 {
			t.Fatalf("calls=%d marked=%d", business.calls, len(session.Marked))
		}
	})
}
