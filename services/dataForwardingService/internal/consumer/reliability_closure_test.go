package consumer

import (
	callpb "Betterfly2/proto/call"
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/mq"
	"context"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/publisher"
	redisClient "data_forwarding_service/internal/redis"
	"data_forwarding_service/internal/router"
	"errors"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/IBM/sarama/mocks"
	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

func closureTestEnvironment(t *testing.T) (*miniredis.Miniredis, *handlers.WebSocketHandler, *mocks.SyncProducer) {
	t.Helper()
	t.Setenv("HOSTNAME", "local")
	server := miniredis.RunT(t)
	previousRedis := redisClient.Rdb
	client := redis.NewClient(&redis.Options{Addr: server.Addr(), MaxRetries: -1})
	redisClient.Rdb = client
	ws := handlers.NewWebSocketHandler()
	previousHandler := handlers.GetWebSocketHandler()
	handlers.SetGlobalWebSocketHandler(ws)
	previousProducer := publisher.KafkaProducer
	producer := mocks.NewSyncProducer(t, nil)
	publisher.KafkaProducer = producer
	waitSubscription := func(want int64) {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for time.Now().Before(deadline) {
			counts, err := client.PubSubNumSub(context.Background(), "user_kick:local").Result()
			if err == nil && counts["user_kick:local"] == want {
				return
			}
			time.Sleep(time.Millisecond)
		}
		t.Errorf("kick subscription did not reach %d subscribers", want)
	}
	t.Cleanup(func() {
		server.SetError("")
		ws.Close()
		waitSubscription(0)
		handlers.SetGlobalWebSocketHandler(previousHandler)
		publisher.KafkaProducer = previousProducer
		_ = producer.Close()
		redisClient.Rdb = previousRedis
		_ = client.Close()
	})
	waitSubscription(1)
	return server, ws, producer
}

func TestStoredPostOfflineThroughConsumerDoesNotRetryOrRepublishPush(t *testing.T) {
	for _, failure := range []string{"none", "cache", "push"} {
		t.Run(failure, func(t *testing.T) {
			server, _, producer := closureTestEnvironment(t)
			if failure == "push" {
				producer.ExpectSendMessageAndFail(errors.New("injected Kafka failure"))
			}
			producer.ExpectSendMessageAndSucceed()
			attempts, acks := 0, 0
			stored := &storage.StoreMsgRsp{MessageId: 42, Created: true, FromUserId: 1, ToUserId: 2, ClientMessageId: "client-42", MessageType: "text"}
			handler := newProcessingTestHandler(func(*sarama.ConsumerMessage) error {
				attempts++
				return processStoredPostResponse(stored, func() error {
					if failure == "cache" && attempts == 1 {
						return errors.New("injected cache failure")
					}
					return handlers.CompletePostIdempotency(context.Background(), 1, "client-42", 42)
				}, handlers.DeliverStoredPost, func(response *pb.ResponseMessage) error {
					acks++
					if response.GetPostAckRsp().GetMessageId() != 42 {
						t.Fatal("ACK changed message ID")
					}
					return nil
				})
			}, func(string, []byte, []sarama.RecordHeader) error {
				t.Fatal("recoverable/offline post entered DLQ")
				return nil
			})
			session := &consumerTestSession{ctx: context.Background()}
			if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "local", Offset: 7})); err != nil {
				t.Fatal(err)
			}
			wantAttempts := 1
			if failure != "none" {
				wantAttempts = 2
			}
			if attempts != wantAttempts || acks != wantAttempts || len(session.marked) != 1 {
				t.Fatalf("attempts=%d acks=%d marked=%d", attempts, acks, len(session.marked))
			}
			if value, err := server.Get("post:effects:42"); err != nil || value != "1" {
				t.Fatalf("effects not completed: %q %v", value, err)
			}
			// A second Kafka offset for the same stored result must not republish APNs.
			if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "local", Offset: 8})); err != nil {
				t.Fatal(err)
			}
			if len(session.marked) != 2 {
				t.Fatal("duplicate result was not confirmed")
			}
		})
	}
}

func TestGroupBatchOfflineThroughConsumerDoesNotReplayOnlineTarget(t *testing.T) {
	server, ws, producer := closureTestEnvironment(t)
	server.HSet("ws_connection_mapping", "2", "remote")
	server.Set("ws_route_lease:2", "remote|owner")
	producer.ExpectSendMessageAndSucceed()
	encoded, err := proto.Marshal(&pb.DFInternalDelivery{Payload: &pb.DFInternalDelivery_GroupPostBatchDelivery{GroupPostBatchDelivery: &pb.GroupPostBatchDelivery{
		TargetUserIds: []int64{3, 2}, Post: &pb.Post{MessageId: 42, FromId: 1, ToId: 77, IsGroup: true, MsgType: "text"},
	}}})
	if err != nil {
		t.Fatal(err)
	}
	value, err := mq.MarshalEnvelopeBytes(envelope.MessageType_DF_RESPONSE, encoded)
	if err != nil {
		t.Fatal(err)
	}
	attempts := 0
	handler := newProcessingTestHandler(nil, func(string, []byte, []sarama.RecordHeader) error {
		t.Fatal("offline group member entered DLQ")
		return nil
	})
	handler.wsHandler = ws
	handler.processMessageFn = func(message *sarama.ConsumerMessage) error { attempts++; return handler.processMessage(message) }
	session := &consumerTestSession{ctx: context.Background()}
	if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "local", Offset: 7, Value: value})); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || len(session.marked) != 1 {
		t.Fatalf("whole batch replayed: attempts=%d marked=%d", attempts, len(session.marked))
	}
}

func TestOfflineDeferralDoesNotSwallowRedisFaultOrLegacyPostOrCall(t *testing.T) {
	server, ws, _ := closureTestEnvironment(t)
	handler := &NewKafkaConsumerGroupHandler{wsHandler: ws}
	server.SetError("injected Redis fault")
	err := handler.deliverGroupPostToUsers(&pb.Post{MessageId: 42}, []int64{2})
	if err == nil || errors.Is(err, router.ErrUserOffline) {
		t.Fatalf("Redis fault was deferred: %v", err)
	}
	server.SetError("")
	if err := handler.deliverGroupPostToUsers(&pb.Post{}, []int64{2}); !errors.Is(err, router.ErrUserOffline) {
		t.Fatalf("unproven legacy persistence silently deferred: %v", err)
	}
	if err := handler.handleCallDelivery(&callpb.Delivery{TargetUserId: 2, Event: &callpb.CallEvent{}}); !errors.Is(err, router.ErrUserOffline) {
		t.Fatalf("Call offline behavior changed: %v", err)
	}
}

func TestKafkaKickRecognizesLegacyAndOwnedInstructions(t *testing.T) {
	_, ws, _ := closureTestEnvironment(t)
	for _, command := range []string{"DELETE USER 2 TARGET local", "DELETE USER 2 TARGET local OWNER deadbeef", "DELETE USER 2 TARGET other-pod OWNER deadbeef"} {
		t.Run(command, func(t *testing.T) {
			handler := newProcessingTestHandler(nil, func(string, []byte, []sarama.RecordHeader) error { t.Fatal("valid kick entered DLQ"); return nil })
			handler.wsHandler = ws
			session := &consumerTestSession{ctx: context.Background()}
			if err := handler.ConsumeClaim(session, newConsumerTestClaim(&sarama.ConsumerMessage{Topic: "user-kick-topic", Value: []byte(command)})); err != nil {
				t.Fatal(err)
			}
			if len(session.marked) != 1 {
				t.Fatal("valid kick was not confirmed")
			}
		})
	}
	if err := (&NewKafkaConsumerGroupHandler{}).processMessage(&sarama.ConsumerMessage{Value: []byte("DELETE USER 2 TARGET other-pod OWNER deadbeef")}); err != nil {
		t.Fatalf("another Pod's kick required a local handler: %v", err)
	}
	for _, command := range []string{"DELETE USER 2 TARGET local OWNER not-hex", "DELETE USER TARGET local"} {
		err := (&NewKafkaConsumerGroupHandler{}).processMessage(&sarama.ConsumerMessage{Value: []byte(command)})
		if err == nil || classifyProcessingError(err) != failurePermanent {
			t.Fatalf("malformed control accepted: %q err=%v", command, err)
		}
	}
}

func TestDataForwardingDLQKeepsEventAndReplayIdentity(t *testing.T) {
	message := &sarama.ConsumerMessage{Topic: "local", Partition: 2, Offset: 99, Headers: []*sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte("event-42")}}}
	headers := headerMap(dlqHeaders(message, envelope.MessageType_STORAGE_RESPONSE, failureTransient, errors.New("injected"), time.Now(), time.Now(), 2))
	if headers["event_id"] != "event-42" || headers["operation_key"] != "event/event-42" {
		t.Fatalf("lost event identity: %v", headers)
	}
	message.Headers = []*sarama.RecordHeader{{Key: []byte("operation_key"), Value: []byte("local/1/7")}}
	headers = headerMap(dlqHeaders(message, envelope.MessageType_STORAGE_RESPONSE, failureTransient, errors.New("injected"), time.Now(), time.Now(), 2))
	if headers["operation_key"] != "local/1/7" {
		t.Fatalf("replayed offset replaced identity: %v", headers)
	}
}
