package main

import (
	"Betterfly2/shared/kafkaconsumer"
	"bytes"
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

func replayTestMessage(topic string) *sarama.ConsumerMessage {
	return &sarama.ConsumerMessage{
		Value:   []byte("raw-payload"),
		Headers: []*sarama.RecordHeader{{Key: []byte("original_topic"), Value: []byte(topic)}, {Key: []byte("original_partition"), Value: []byte("1")}, {Key: []byte("original_offset"), Value: []byte("7")}},
	}
}

func TestReplayDefaultsToDryRunWithoutPublishingOrConfirming(t *testing.T) {
	published := false
	handler := &replayHandler{
		config:  replayConfig{dryRun: true, allowed: parseAllowlist("allowed-topic")},
		publish: func(string, []byte, []sarama.RecordHeader) error { published = true; return nil },
	}
	mark, err := handler.process(replayTestMessage("allowed-topic"))
	if err != nil || mark || published {
		t.Fatalf("dry-run changed state: mark=%v published=%v err=%v", mark, published, err)
	}
}

func TestReplayRejectsTopicOutsideAllowlist(t *testing.T) {
	handler := &replayHandler{config: replayConfig{allowed: parseAllowlist("allowed-topic")}}
	if mark, err := handler.process(replayTestMessage("forbidden-topic")); err == nil || mark {
		t.Fatalf("forbidden topic was accepted: mark=%v err=%v", mark, err)
	}
}

func TestReplayMarksOnlyAfterSuccessfulPublish(t *testing.T) {
	handler := &replayHandler{
		config:  replayConfig{allowed: parseAllowlist("allowed-topic")},
		publish: func(string, []byte, []sarama.RecordHeader) error { return errors.New("publish failed") },
	}
	if mark, err := handler.process(replayTestMessage("allowed-topic")); err == nil || mark {
		t.Fatalf("failed publish was confirmed: mark=%v err=%v", mark, err)
	}
	handler.publish = func(topic string, payload []byte, headers []sarama.RecordHeader) error {
		if topic != "allowed-topic" || string(payload) != "raw-payload" {
			t.Fatalf("replay changed payload: topic=%s payload=%q", topic, payload)
		}
		if len(headers) != 1 || string(headers[0].Key) != "operation_key" || string(headers[0].Value) != "allowed-topic/1/7" {
			t.Fatalf("replay lost identity: %v", headers)
		}
		return nil
	}
	if mark, err := handler.process(replayTestMessage("allowed-topic")); err != nil || !mark {
		t.Fatalf("successful replay was not confirmed: mark=%v err=%v", mark, err)
	}
}

func TestReplayIdentitySurvivesNewOffsetAndRepeatedDLQ(t *testing.T) {
	for _, test := range []struct {
		name    string
		headers []*sarama.RecordHeader
		want    string
	}{
		{"legacy_offset", nil, "allowed-topic/1/7"},
		{"legacy_shared_event", []*sarama.RecordHeader{{Key: []byte("operation_key"), Value: []byte("event/event-42")}}, "event/event-42"},
		{"current_event", []*sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte("event-42")}, {Key: []byte("operation_key"), Value: []byte("event/event-42")}}, "event/event-42"},
		{"replayed_offset", []*sarama.RecordHeader{{Key: []byte("operation_key"), Value: []byte("allowed-topic/0/3")}}, "allowed-topic/0/3"},
	} {
		t.Run(test.name, func(t *testing.T) {
			message := replayTestMessage("allowed-topic")
			message.Headers = append(message.Headers, test.headers...)
			for round := 0; round < 2; round++ {
				var republished *sarama.ConsumerMessage
				handler := &replayHandler{config: replayConfig{allowed: parseAllowlist("allowed-topic")}, publish: func(topic string, payload []byte, headers []sarama.RecordHeader) error {
					republished = &sarama.ConsumerMessage{Topic: topic, Partition: 2, Offset: int64(99 + round), Value: payload}
					for _, header := range headers {
						copy := header
						republished.Headers = append(republished.Headers, &copy)
					}
					return nil
				}}
				mark, err := handler.process(message)
				if err != nil || !mark || !bytes.Equal(republished.Value, message.Value) {
					t.Fatalf("replay failed: mark=%v err=%v", mark, err)
				}
				if got := kafkaconsumer.OperationKey(republished); got != test.want {
					t.Fatalf("replay identity=%q want=%q", got, test.want)
				}
				consumer := kafkaconsumer.New(kafkaconsumer.Config{Service: "test", DLQTopic: "test-dlq"}, func(ctx context.Context, _ *sarama.ConsumerMessage) kafkaconsumer.Result {
					key, ok := kafkaconsumer.OperationKeyFromContext(ctx)
					if !ok || key != test.want {
						t.Fatalf("consumer identity=%q", key)
					}
					return kafkaconsumer.Success()
				}, nil)
				session := &replayTestSession{ctx: context.Background()}
				if err := consumer.ConsumeClaim(session, replayTestClaim(republished)); err != nil || len(session.marked) != 1 {
					t.Fatalf("consume failed: %v", err)
				}
				headers := kafkaconsumer.DLQHeaders("test", republished, kafkaconsumer.EnvelopeType(nil), kafkaconsumer.Permanent(errors.New("injected")), time.Now(), time.Now(), 0)
				message = &sarama.ConsumerMessage{Value: republished.Value}
				for _, header := range headers {
					copy := header
					message.Headers = append(message.Headers, &copy)
				}
			}
		})
	}
}

func TestReplayRejectsMissingInvalidOrConflictingIdentity(t *testing.T) {
	for _, metadata := range []map[string]string{
		{"original_topic": "allowed-topic"},
		{"original_topic": "allowed-topic", "original_partition": "-1", "original_offset": "7"},
		{"original_topic": "allowed-topic", "original_partition": "0", "original_offset": "invalid"},
		{"original_topic": "allowed-topic", "operation_key": "event/"},
		{"original_topic": "allowed-topic", "operation_key": "event/one", "event_id": "two"},
	} {
		handler := &replayHandler{config: replayConfig{allowed: parseAllowlist("allowed-topic")}, publish: func(string, []byte, []sarama.RecordHeader) error { t.Fatal("unsafe replay published"); return nil }}
		message := &sarama.ConsumerMessage{}
		for key, value := range metadata {
			message.Headers = append(message.Headers, &sarama.RecordHeader{Key: []byte(key), Value: []byte(value)})
		}
		if mark, err := handler.process(message); err == nil || mark {
			t.Fatalf("unsafe identity accepted: %v mark=%v err=%v", metadata, mark, err)
		}
	}
}

type replayTestSession struct {
	ctx    context.Context
	marked []*sarama.ConsumerMessage
}

func (s *replayTestSession) Claims() map[string][]int32               { return nil }
func (s *replayTestSession) MemberID() string                         { return "test" }
func (s *replayTestSession) GenerationID() int32                      { return 1 }
func (s *replayTestSession) MarkOffset(string, int32, int64, string)  {}
func (s *replayTestSession) Commit()                                  {}
func (s *replayTestSession) ResetOffset(string, int32, int64, string) {}
func (s *replayTestSession) Context() context.Context                 { return s.ctx }
func (s *replayTestSession) MarkMessage(message *sarama.ConsumerMessage, _ string) {
	s.marked = append(s.marked, message)
}

type replayClaim struct {
	messages <-chan *sarama.ConsumerMessage
}

func (c replayClaim) Topic() string                            { return "test" }
func (c replayClaim) Partition() int32                         { return 0 }
func (c replayClaim) InitialOffset() int64                     { return 0 }
func (c replayClaim) HighWaterMarkOffset() int64               { return 0 }
func (c replayClaim) Messages() <-chan *sarama.ConsumerMessage { return c.messages }
func replayTestClaim(messages ...*sarama.ConsumerMessage) replayClaim {
	channel := make(chan *sarama.ConsumerMessage, len(messages))
	for _, message := range messages {
		channel <- message
	}
	close(channel)
	return replayClaim{channel}
}

func TestReplayFailureStopsPartitionBeforeHigherOffset(t *testing.T) {
	for _, invalid := range []bool{false, true} {
		first := replayTestMessage("allowed-topic")
		first.Offset = 7
		if invalid {
			first.Headers = first.Headers[:1]
		}
		second := replayTestMessage("allowed-topic")
		second.Offset = 8
		calls := 0
		handler := &replayHandler{config: replayConfig{allowed: parseAllowlist("allowed-topic"), max: 100}, publish: func(string, []byte, []sarama.RecordHeader) error { calls++; return errors.New("publish failed") }}
		session := &replayTestSession{ctx: context.Background()}
		if err := handler.ConsumeClaim(session, replayTestClaim(first, second)); err == nil || len(session.marked) != 0 {
			t.Fatalf("failure advanced partition: marked=%d err=%v", len(session.marked), err)
		}
		wantCalls := 1
		if invalid {
			wantCalls = 0
		}
		if calls != wantCalls {
			t.Fatalf("processed later offset: calls=%d", calls)
		}
	}
}
