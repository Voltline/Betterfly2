package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/kafkaconsumer/testutil"
	"Betterfly2/shared/mq"

	"github.com/IBM/sarama"
)

type fakeCallHandler struct {
	calls    int
	failures int
}

func (h *fakeCallHandler) Handle(context.Context, *callpb.InternalRequest) error {
	h.calls++
	if h.calls <= h.failures {
		return errors.New("redis unavailable")
	}
	return nil
}

func (h *fakeCallHandler) HandlePushResult(context.Context, *pushpb.VoIPPushResult) error { return nil }

func callMessage(t *testing.T, offset int64) *sarama.ConsumerMessage {
	t.Helper()
	payload, err := mq.MarshalEnvelope(envelope.MessageType_CALL_REQUEST, &callpb.InternalRequest{
		UserId: 1, FromKafkaTopic: "df-a",
		Request: &callpb.ClientRequest{Payload: &callpb.ClientRequest_GetConfig{GetConfig: &callpb.GetCallConfig{}}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sarama.ConsumerMessage{Topic: "call-service", Partition: 4, Offset: offset, Value: payload}
}

func callReliable(handler *Handler, maxRetries int, publish kafkaconsumer.DLQPublisher) {
	handler.reliable = kafkaconsumer.New(kafkaconsumer.Config{
		Service: "call", DLQTopic: "call-service-dlq", MaxRetries: maxRetries,
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	}, handler.process, publish)
}

func TestCallConsumerOffsetAndDLQSemantics(t *testing.T) {
	t.Run("transient retries", func(t *testing.T) {
		business := &fakeCallHandler{failures: 1}
		handler := NewHandler(business)
		callReliable(handler, 2, nil)
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(callMessage(t, 5))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d marked=%d", business.calls, len(session.Marked))
		}
	})

	t.Run("corrupt envelope enters DLQ", func(t *testing.T) {
		business := &fakeCallHandler{}
		handler := NewHandler(business)
		var dlq int
		callReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(&sarama.ConsumerMessage{Topic: "call-service", Offset: 6, Value: []byte{0xff}})); err != nil {
			t.Fatal(err)
		}
		if dlq != 1 || business.calls != 0 || len(session.Marked) != 1 {
			t.Fatalf("dlq=%d calls=%d marked=%d", dlq, business.calls, len(session.Marked))
		}
	})

	t.Run("retry exhaustion commits only after DLQ", func(t *testing.T) {
		business := &fakeCallHandler{failures: 10}
		handler := NewHandler(business)
		var dlq int
		callReliable(handler, 1, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(callMessage(t, 7))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || dlq != 1 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d dlq=%d marked=%d", business.calls, dlq, len(session.Marked))
		}
	})

	t.Run("failed DLQ blocks next call", func(t *testing.T) {
		business := &fakeCallHandler{failures: 10}
		handler := NewHandler(business)
		callReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error { return errors.New("down") })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(callMessage(t, 8), callMessage(t, 9))); err == nil {
			t.Fatal("expected failure")
		}
		if business.calls != 1 || len(session.Marked) != 0 {
			t.Fatalf("calls=%d marked=%d", business.calls, len(session.Marked))
		}
	})
}
