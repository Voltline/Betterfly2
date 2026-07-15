package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/kafkaconsumer/testutil"
	"Betterfly2/shared/mq"

	"github.com/IBM/sarama"
)

type fakePushHandler struct {
	calls    int
	failures int
}

func (h *fakePushHandler) Handle(context.Context, *pushpb.RequestMessage) error {
	h.calls++
	if h.calls <= h.failures {
		return errors.New("APNs unavailable")
	}
	return nil
}

func pushMessage(t *testing.T, offset int64) *sarama.ConsumerMessage {
	t.Helper()
	payload, err := mq.MarshalEnvelope(envelope.MessageType_PUSH_REQUEST, &pushpb.RequestMessage{
		Payload: &pushpb.RequestMessage_MessagePush{MessagePush: &pushpb.MessagePushRequest{
			MessageId: 10, SenderUserId: 1, TargetUserIds: []int64{2}, ConversationId: 2,
			MessageType: "text", SentAt: time.Now().UTC().Format(time.RFC3339Nano),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sarama.ConsumerMessage{Topic: "push-service", Partition: 3, Offset: offset, Value: payload}
}

func pushReliable(handler *Handler, maxRetries int, publish kafkaconsumer.DLQPublisher) {
	handler.reliable = kafkaconsumer.New(kafkaconsumer.Config{
		Service: "push", DLQTopic: "push-service-dlq", MaxRetries: maxRetries,
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	}, handler.process, publish)
}

func TestPushConsumerOffsetAndDLQSemantics(t *testing.T) {
	t.Run("APNs transient failure retries", func(t *testing.T) {
		business := &fakePushHandler{failures: 1}
		handler := NewHandler(business)
		pushReliable(handler, 2, nil)
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(pushMessage(t, 1))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d marked=%d", business.calls, len(session.Marked))
		}
	})

	t.Run("unknown envelope enters DLQ", func(t *testing.T) {
		business := &fakePushHandler{}
		handler := NewHandler(business)
		var dlq int
		pushReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		payload, err := mq.MarshalEnvelope(envelope.MessageType_CALL_REQUEST, &pushpb.RequestMessage{})
		if err != nil {
			t.Fatal(err)
		}
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(&sarama.ConsumerMessage{Topic: "push-service", Offset: 2, Value: payload})); err != nil {
			t.Fatal(err)
		}
		if dlq != 1 || business.calls != 0 || len(session.Marked) != 1 {
			t.Fatalf("dlq=%d calls=%d marked=%d", dlq, business.calls, len(session.Marked))
		}
	})

	t.Run("retry exhaustion commits only after DLQ", func(t *testing.T) {
		business := &fakePushHandler{failures: 10}
		handler := NewHandler(business)
		var dlq int
		pushReliable(handler, 1, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(pushMessage(t, 3))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || dlq != 1 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d dlq=%d marked=%d", business.calls, dlq, len(session.Marked))
		}
	})

	t.Run("DLQ failure blocks higher offset", func(t *testing.T) {
		business := &fakePushHandler{failures: 10}
		handler := NewHandler(business)
		pushReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error { return errors.New("down") })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(pushMessage(t, 4), pushMessage(t, 5))); err == nil {
			t.Fatal("expected failure")
		}
		if business.calls != 1 || len(session.Marked) != 0 {
			t.Fatalf("calls=%d marked=%d", business.calls, len(session.Marked))
		}
	})
}
