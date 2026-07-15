package kafkaconsumer

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/IBM/sarama"
)

type testSession struct {
	ctx    context.Context
	marked []*sarama.ConsumerMessage
}

func (s *testSession) Claims() map[string][]int32               { return nil }
func (s *testSession) MemberID() string                         { return "test" }
func (s *testSession) GenerationID() int32                      { return 1 }
func (s *testSession) MarkOffset(string, int32, int64, string)  {}
func (s *testSession) Commit()                                  {}
func (s *testSession) ResetOffset(string, int32, int64, string) {}
func (s *testSession) MarkMessage(m *sarama.ConsumerMessage, _ string) {
	s.marked = append(s.marked, m)
}
func (s *testSession) Context() context.Context { return s.ctx }

type testClaim struct{ messages chan *sarama.ConsumerMessage }

type retryAfterTestError struct{ delay time.Duration }

func (e retryAfterTestError) Error() string             { return "lease is still owned" }
func (e retryAfterTestError) RetryAfter() time.Duration { return e.delay }

func (c *testClaim) Topic() string                            { return "source" }
func (c *testClaim) Partition() int32                         { return 0 }
func (c *testClaim) InitialOffset() int64                     { return 0 }
func (c *testClaim) HighWaterMarkOffset() int64               { return 0 }
func (c *testClaim) Messages() <-chan *sarama.ConsumerMessage { return c.messages }

func newTestClaim(messages ...*sarama.ConsumerMessage) *testClaim {
	claim := &testClaim{messages: make(chan *sarama.ConsumerMessage, len(messages))}
	for _, message := range messages {
		claim.messages <- message
	}
	close(claim.messages)
	return claim
}

func testConfig() Config {
	return Config{
		Service: "test", DLQTopic: "test-dlq", MaxRetries: 2,
		InitialBackoff: time.Millisecond, MaxBackoff: time.Millisecond, Now: time.Now,
	}
}

func TestSuccessMarksMessage(t *testing.T) {
	session := &testSession{ctx: context.Background()}
	handler := New(testConfig(), func(context.Context, *sarama.ConsumerMessage) Result { return Success() }, nil)
	message := &sarama.ConsumerMessage{Topic: "source", Partition: 2, Offset: 9}
	if err := handler.ConsumeClaim(session, newTestClaim(message)); err != nil {
		t.Fatal(err)
	}
	if len(session.marked) != 1 || session.marked[0] != message {
		t.Fatalf("successful message was not marked: %#v", session.marked)
	}
}

func TestTransientRetriesThenMarks(t *testing.T) {
	var attempts int
	session := &testSession{ctx: context.Background()}
	handler := New(testConfig(), func(context.Context, *sarama.ConsumerMessage) Result {
		attempts++
		if attempts < 3 {
			return Transient(errors.New("temporary"))
		}
		return Success()
	}, nil)
	if err := handler.ConsumeClaim(session, newTestClaim(&sarama.ConsumerMessage{Topic: "source", Offset: 1})); err != nil {
		t.Fatal(err)
	}
	if attempts != 3 || len(session.marked) != 1 {
		t.Fatalf("unexpected retry result: attempts=%d marked=%d", attempts, len(session.marked))
	}
}

func TestPermanentAndExhaustedTransientRequireSuccessfulDLQ(t *testing.T) {
	for _, test := range []struct {
		name      string
		processor Processor
		attempts  int
	}{
		{name: "permanent", processor: func(context.Context, *sarama.ConsumerMessage) Result { return Permanent(errors.New("bad payload")) }, attempts: 1},
		{name: "exhausted", processor: func(context.Context, *sarama.ConsumerMessage) Result { return Transient(errors.New("down")) }, attempts: 3},
	} {
		t.Run(test.name, func(t *testing.T) {
			var attempts, dlqCalls int
			session := &testSession{ctx: context.Background()}
			handler := New(testConfig(), func(ctx context.Context, message *sarama.ConsumerMessage) Result {
				attempts++
				return test.processor(ctx, message)
			}, func(_ context.Context, topic string, payload []byte, headers []sarama.RecordHeader) error {
				dlqCalls++
				if topic != "test-dlq" || string(payload) != "payload" || len(headers) == 0 {
					t.Fatalf("unexpected DLQ record: topic=%q payload=%q headers=%v", topic, payload, headers)
				}
				return nil
			})
			if err := handler.ConsumeClaim(session, newTestClaim(&sarama.ConsumerMessage{Topic: "source", Partition: 4, Offset: 7, Value: []byte("payload")})); err != nil {
				t.Fatal(err)
			}
			if attempts != test.attempts || dlqCalls != 1 || len(session.marked) != 1 {
				t.Fatalf("attempts=%d dlq=%d marked=%d", attempts, dlqCalls, len(session.marked))
			}
		})
	}
}

func TestDLQFailureStopsPartitionWithoutMarkingHigherOffset(t *testing.T) {
	var processed []int64
	session := &testSession{ctx: context.Background()}
	handler := New(testConfig(), func(_ context.Context, message *sarama.ConsumerMessage) Result {
		processed = append(processed, message.Offset)
		if message.Offset == 10 {
			return Permanent(errors.New("invalid"))
		}
		return Success()
	}, func(context.Context, string, []byte, []sarama.RecordHeader) error {
		return errors.New("DLQ unavailable")
	})
	err := handler.ConsumeClaim(session, newTestClaim(
		&sarama.ConsumerMessage{Topic: "source", Offset: 10},
		&sarama.ConsumerMessage{Topic: "source", Offset: 11},
	))
	if err == nil {
		t.Fatal("expected DLQ failure")
	}
	if len(processed) != 1 || processed[0] != 10 || len(session.marked) != 0 {
		t.Fatalf("partition advanced after failed offset: processed=%v marked=%v", processed, session.marked)
	}
}

func TestContextCancellationStopsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	session := &testSession{ctx: ctx}
	config := testConfig()
	config.InitialBackoff = time.Hour
	config.MaxBackoff = time.Hour
	entered := make(chan struct{})
	var once sync.Once
	handler := New(config, func(context.Context, *sarama.ConsumerMessage) Result {
		once.Do(func() { close(entered) })
		return Transient(errors.New("temporary"))
	}, nil)
	done := make(chan error, 1)
	go func() {
		done <- handler.ConsumeClaim(session, newTestClaim(&sarama.ConsumerMessage{Topic: "source", Offset: 1}))
	}()
	<-entered
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, got %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation did not stop retry backoff")
	}
	if len(session.marked) != 0 {
		t.Fatal("canceled message was marked")
	}
}

func TestTransientHonorsRetryAfterBoundary(t *testing.T) {
	var attempts int
	delay := 30 * time.Millisecond
	started := time.Now()
	handler := New(testConfig(), func(context.Context, *sarama.ConsumerMessage) Result {
		attempts++
		if attempts == 1 {
			return Transient(retryAfterTestError{delay: delay})
		}
		return Success()
	}, nil)
	session := &testSession{ctx: context.Background()}
	if err := handler.ConsumeClaim(session, newTestClaim(&sarama.ConsumerMessage{Topic: "source", Offset: 2})); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < delay {
		t.Fatalf("retry occurred before lease boundary: elapsed=%s delay=%s", elapsed, delay)
	}
	if attempts != 2 || len(session.marked) != 1 {
		t.Fatalf("unexpected retry result: attempts=%d marked=%d", attempts, len(session.marked))
	}
}

func TestOperationKeyIsStableAndAvailableInProcessorContext(t *testing.T) {
	message := &sarama.ConsumerMessage{Topic: "friend-service", Partition: 3, Offset: 99}
	want := "friend-service/3/99"
	if got := OperationKey(message); got != want {
		t.Fatalf("OperationKey()=%q want %q", got, want)
	}
	handler := New(testConfig(), func(ctx context.Context, _ *sarama.ConsumerMessage) Result {
		got, ok := OperationKeyFromContext(ctx)
		if !ok || got != want {
			t.Fatalf("processor operation key=%q ok=%v", got, ok)
		}
		return Success()
	}, nil)
	if err := handler.ConsumeClaim(&testSession{ctx: context.Background()}, newTestClaim(message)); err != nil {
		t.Fatal(err)
	}
}
