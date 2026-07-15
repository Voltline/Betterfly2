package consumer

import (
	"context"
	"errors"
	"testing"
	"time"

	envelope "Betterfly2/proto/envelope"
	storagepb "Betterfly2/proto/storage"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/kafkaconsumer/testutil"
	"Betterfly2/shared/mq"

	"github.com/IBM/sarama"
)

type fakeStorageHandler struct {
	calls       int
	failures    int
	operationID string
}

func (h *fakeStorageHandler) HandleMessage(ctx context.Context, _ []byte) error {
	h.calls++
	h.operationID, _ = kafkaconsumer.OperationKeyFromContext(ctx)
	if h.calls <= h.failures {
		return errors.New("database unavailable")
	}
	return nil
}

func storageMessage(t *testing.T, offset int64) *sarama.ConsumerMessage {
	t.Helper()
	payload, err := mq.MarshalEnvelope(envelope.MessageType_STORAGE_REQUEST, &storagepb.RequestMessage{
		FromKafkaTopic: "df-a", TargetUserId: 1,
		Payload: &storagepb.RequestMessage_QueryUser{QueryUser: &storagepb.QueryUser{UserId: 1}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return &sarama.ConsumerMessage{Topic: "storage-service", Partition: 2, Offset: offset, Value: payload}
}

func storageReliable(handler *KafkaConsumerGroupHandler, maxRetries int, publish kafkaconsumer.DLQPublisher) {
	handler.reliable = kafkaconsumer.New(kafkaconsumer.Config{
		Service: "storage", DLQTopic: "storage-service-dlq", MaxRetries: maxRetries,
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond,
	}, handler.process, publish)
}

func TestStorageConsumerOffsetAndDLQSemantics(t *testing.T) {
	t.Run("transient retry succeeds", func(t *testing.T) {
		business := &fakeStorageHandler{failures: 1}
		handler := NewKafkaConsumerGroupHandler(business)
		storageReliable(handler, 2, nil)
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(storageMessage(t, 7))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || business.operationID != "storage-service/2/7" || len(session.Marked) != 1 {
			t.Fatalf("calls=%d operation=%q marked=%d", business.calls, business.operationID, len(session.Marked))
		}
	})

	t.Run("malformed protobuf enters DLQ", func(t *testing.T) {
		business := &fakeStorageHandler{}
		handler := NewKafkaConsumerGroupHandler(business)
		var dlq int
		storageReliable(handler, 0, func(_ context.Context, topic string, payload []byte, _ []sarama.RecordHeader) error {
			dlq++
			if topic != "storage-service-dlq" || string(payload) != "\xff" {
				t.Fatalf("unexpected DLQ record: %s %q", topic, payload)
			}
			return nil
		})
		session := testutil.NewSession(context.Background())
		message := &sarama.ConsumerMessage{Topic: "storage-service", Offset: 8, Value: []byte{0xff}}
		if err := handler.ConsumeClaim(session, testutil.NewClaim(message)); err != nil {
			t.Fatal(err)
		}
		if dlq != 1 || business.calls != 0 || len(session.Marked) != 1 {
			t.Fatalf("dlq=%d calls=%d marked=%d", dlq, business.calls, len(session.Marked))
		}
	})

	t.Run("retry exhaustion commits only after DLQ", func(t *testing.T) {
		business := &fakeStorageHandler{failures: 10}
		handler := NewKafkaConsumerGroupHandler(business)
		var dlq int
		storageReliable(handler, 1, func(context.Context, string, []byte, []sarama.RecordHeader) error { dlq++; return nil })
		session := testutil.NewSession(context.Background())
		if err := handler.ConsumeClaim(session, testutil.NewClaim(storageMessage(t, 9))); err != nil {
			t.Fatal(err)
		}
		if business.calls != 2 || dlq != 1 || len(session.Marked) != 1 {
			t.Fatalf("calls=%d dlq=%d marked=%d", business.calls, dlq, len(session.Marked))
		}
	})

	t.Run("DLQ failure blocks later offset", func(t *testing.T) {
		business := &fakeStorageHandler{failures: 10}
		handler := NewKafkaConsumerGroupHandler(business)
		storageReliable(handler, 0, func(context.Context, string, []byte, []sarama.RecordHeader) error {
			return errors.New("DLQ unavailable")
		})
		session := testutil.NewSession(context.Background())
		err := handler.ConsumeClaim(session, testutil.NewClaim(storageMessage(t, 10), storageMessage(t, 11)))
		if err == nil || business.calls != 1 || len(session.Marked) != 0 {
			t.Fatalf("err=%v calls=%d marked=%d", err, business.calls, len(session.Marked))
		}
	})
}
