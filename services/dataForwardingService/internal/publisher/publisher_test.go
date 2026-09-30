package publisher

import (
	"Betterfly2/shared/kafkaconsumer"
	"encoding/hex"
	"errors"
	"testing"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"
)

func testProducer(t *testing.T) *mocks.SyncProducer {
	t.Helper()
	previous := KafkaProducer
	producer := mocks.NewSyncProducer(t, nil)
	KafkaProducer = producer
	t.Cleanup(func() { KafkaProducer = previous; _ = producer.Close() })
	return producer
}

func TestPublishedRequestsRemainDistinctAfterOffsetReuse(t *testing.T) {
	producer := testProducer(t)
	var keys []string
	for i := 0; i < 2; i++ {
		producer.ExpectSendMessageWithMessageCheckerFunctionAndSucceed(func(message *sarama.ProducerMessage) error {
			payload, _ := message.Value.Encode()
			if message.Topic != "storage-service" || string(payload) != "unchanged envelope bytes" {
				return errors.New("changed payload/topic")
			}
			if len(message.Headers) != 1 || string(message.Headers[0].Key) != "event_id" {
				return errors.New("request has no event identity")
			}
			identity, err := hex.DecodeString(string(message.Headers[0].Value))
			if err != nil || len(identity) != 16 {
				return errors.New("invalid event identity")
			}
			// Two different Kafka incarnations may reuse the exact same location.
			keys = append(keys, kafkaconsumer.OperationKey(&sarama.ConsumerMessage{Topic: "storage-service", Partition: 0, Offset: 72, Headers: []*sarama.RecordHeader{&message.Headers[0]}}))
			return nil
		})
		if err := PublishMessage("unchanged envelope bytes", "storage-service"); err != nil {
			t.Fatal(err)
		}
	}
	if keys[0] == keys[1] {
		t.Fatal("offset reuse collided with an earlier operation")
	}
}

func TestRawPublishPreservesReplayIdentity(t *testing.T) {
	producer := testProducer(t)
	headers := []sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte("existing-event")}, {Key: []byte("operation_key"), Value: []byte("event/existing-event")}}
	producer.ExpectSendMessageWithMessageCheckerFunctionAndSucceed(func(message *sarama.ProducerMessage) error {
		if len(message.Headers) != 2 || string(message.Headers[0].Value) != "existing-event" || string(message.Headers[1].Value) != "event/existing-event" {
			return errors.New("replay identity replaced")
		}
		return nil
	})
	if err := PublishRawMessage([]byte("original bytes"), "storage-service", headers); err != nil {
		t.Fatal(err)
	}
}

func TestPublishFailureStillReturnsError(t *testing.T) {
	producer := testProducer(t)
	producer.ExpectSendMessageAndFail(errors.New("injected broker failure"))
	if err := PublishMessage("payload", "storage-service"); err == nil {
		t.Fatal("broker failure must not report success")
	}
}
