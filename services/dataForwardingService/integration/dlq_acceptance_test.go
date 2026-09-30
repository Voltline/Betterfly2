package integration

import (
	pb "Betterfly2/proto/data_forwarding"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/mq"
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/redis/go-redis/v9"
)

func TestDLQAcceptance(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("set BETTERFLY_ACCEPTANCE=1 against a dedicated Compose test environment")
	}
	database := acceptanceDatabase(t)
	p, err := openNetworkProbe(envOr("BETTERFLY_DF_PORT_2", defaultDFPort2))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	if err := p.signup(randomAccount(t, "dlq")); err != nil {
		t.Fatal(err)
	}
	prepareAcceptanceOffsets(t, database, p)
	kafka, producer := acceptanceKafka(t)
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()
	r := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer r.Close()
	topic, err := r.HGet(ctx, "ws_connection_mapping", fmt.Sprint(p.userID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	const dlqTopic = "data-forwarding-dlq"
	partitions, err := kafka.Partitions(dlqTopic)
	if err != nil {
		t.Fatal(err)
	}
	starts := map[int32]int64{}
	for _, partition := range partitions {
		starts[partition], err = kafka.GetOffset(dlqTopic, partition, sarama.OffsetNewest)
		if err != nil {
			t.Fatal(err)
		}
	}
	marker := randomAccount(t, "bad")
	broken := []byte{0xff, 0xff, 0xff}
	originalPartition, originalOffset, err := producer.SendMessage(&sarama.ProducerMessage{Topic: topic, Partition: 0, Value: sarama.ByteEncoder(broken), Headers: []sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte(marker)}}})
	if err != nil {
		t.Fatal(err)
	}
	consumer, err := sarama.NewConsumerFromClient(kafka)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	found := false
	deadline := time.Now().Add(20 * time.Second)
	for !found && time.Now().Before(deadline) {
		for _, partition := range partitions {
			end, err := kafka.GetOffset(dlqTopic, partition, sarama.OffsetNewest)
			if err != nil {
				t.Fatal(err)
			}
			if end <= starts[partition] {
				continue
			}
			claim, err := consumer.ConsumePartition(dlqTopic, partition, starts[partition])
			if err != nil {
				t.Fatal(err)
			}
			select {
			case message := <-claim.Messages():
				metadata := map[string]string{}
				for _, header := range message.Headers {
					metadata[string(header.Key)] = string(header.Value)
				}
				if metadata["event_id"] == marker {
					if !bytes.Equal(message.Value, broken) || metadata["error_class"] != "permanent" || metadata["original_topic"] != topic || metadata["original_offset"] != fmt.Sprint(originalOffset) || metadata["original_partition"] != fmt.Sprint(originalPartition) {
						t.Fatal("DLQ lost payload/class/source identity")
					}
					found = true
				}
				starts[partition] = message.Offset + 1
			case <-time.After(5 * time.Second):
				_ = claim.Close()
				t.Fatal("DLQ partition read timed out")
			}
			_ = claim.Close()
		}
		if !found {
			time.Sleep(100 * time.Millisecond)
		}
	}
	if !found {
		t.Fatal("malformed protobuf was not preserved in DLQ")
	}
	t.Log("PASS real malformed protobuf -> DF DLQ, preserving permanent classification, payload and source identity")

	groupID := time.Now().UnixNano()%1000000000 + 8000000000
	probeRequest(t, p, &pb.RequestMessage{Payload: &pb.RequestMessage_InsertGroup{InsertGroup: &pb.InsertGroup{ToBeCreatedGroupId: groupID, ToBeCreatedGroupName: "before-replay"}}}, func(r *pb.ResponseMessage) bool { return r.GetServer() != nil })
	eventID := randomAccount(t, "replay")
	operationKey := "event/" + eventID
	payload, err := mq.MarshalEnvelope(envelope.MessageType_FRIEND_REQUEST, &friend.RequestMessage{FromKafkaTopic: topic, TargetUserId: p.userID, Payload: &friend.RequestMessage_UpdateGroupName{UpdateGroupName: &friend.UpdateGroupName{RequestUserId: p.userID, GroupId: groupID, GroupName: "after-replay"}}})
	if err != nil {
		t.Fatal(err)
	}
	sourcePartition, sourceOffset, err := producer.SendMessage(&sarama.ProducerMessage{Topic: "friend-service", Partition: 0, Value: sarama.ByteEncoder(payload), Headers: []sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte(eventID)}}})
	if err != nil {
		t.Fatal(err)
	}
	response, err := p.wait(15*time.Second, func(r *pb.ResponseMessage) bool { return r.GetGroupMemberOperationRsp() != nil })
	if err != nil || response.message.GetGroupMemberOperationRsp().GetResult() != "FRIEND_OK" {
		t.Fatal("original rename operation failed")
	}
	var before db.Group
	if err := database.First(&before, "group_id = ?", groupID).Error; err != nil {
		t.Fatal(err)
	}
	architecture, err := exec.CommandContext(ctx, "docker", "exec", "df2", "uname", "-m").Output()
	if err != nil {
		t.Fatal(err)
	}
	arch := "arm64"
	if strings.TrimSpace(string(architecture)) == "x86_64" {
		arch = "amd64"
	} else if strings.TrimSpace(string(architecture)) != "aarch64" {
		t.Fatal("unsupported test container architecture")
	}
	binary := filepath.Join(t.TempDir(), "dlq-replay")
	build := exec.CommandContext(ctx, "go", "build", "-o", binary, "./tools/dlq-replay")
	build.Dir = ".."
	build.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+arch, "CGO_ENABLED=0")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("replay tool build failed: %v\n%s", err, output)
	}
	remote := "/tmp/" + eventID + "-dlq-replay"
	if output, err := exec.CommandContext(ctx, "docker", "cp", binary, "df2:"+remote).CombinedOutput(); err != nil {
		t.Fatalf("copy test replay tool failed: %v\n%s", err, output)
	}
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cleanupCancel()
		_ = exec.CommandContext(cleanupCtx, "docker", "exec", "df2", "rm", "-f", remote).Run()
	})
	// Seed fresh test groups at current DLQ ends. Never replay historical records.
	admin, err := sarama.NewClusterAdminFromClient(kafka)
	if err != nil {
		t.Fatal(err)
	}
	defer admin.Close()
	sourceParts, err := kafka.Partitions("friend-service")
	if err != nil {
		t.Fatal(err)
	}
	for round := 0; round < 2; round++ {
		group := fmt.Sprintf("acceptance-%s-%d", eventID, round)
		manager, err := sarama.NewOffsetManagerFromClient(group, kafka)
		if err != nil {
			t.Fatal(err)
		}
		parts, err := kafka.Partitions("friend-service-dlq")
		if err != nil {
			t.Fatal(err)
		}
		for _, part := range parts {
			end, err := kafka.GetOffset("friend-service-dlq", part, sarama.OffsetNewest)
			if err != nil {
				t.Fatal(err)
			}
			offsets, err := manager.ManagePartition("friend-service-dlq", part)
			if err != nil {
				t.Fatal(err)
			}
			offsets.ResetOffset(end, "dedicated acceptance replay only")
			if err := offsets.Close(); err != nil {
				t.Fatal(err)
			}
		}
		if err := manager.Close(); err != nil {
			t.Fatal(err)
		}
		original := &sarama.ConsumerMessage{Topic: "friend-service", Partition: sourcePartition, Offset: sourceOffset, Headers: []*sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte(eventID)}}}
		headers := kafkaconsumer.DLQHeaders("friend", original, envelope.MessageType_FRIEND_REQUEST, kafkaconsumer.Transient(errors.New("synthetic completed-operation replay acceptance")), time.Now(), time.Now(), 1)
		if _, _, err := producer.SendMessage(&sarama.ProducerMessage{Topic: "friend-service-dlq", Partition: 0, Value: sarama.ByteEncoder(payload), Headers: headers}); err != nil {
			t.Fatal(err)
		}
		replayCtx, replayCancel := context.WithTimeout(context.Background(), 30*time.Second)
		output, replayErr := exec.CommandContext(replayCtx, "docker", "exec", "df2", remote, "-dry-run=false", "-max=1", "-allow-topics=friend-service", "-dlq-topic=friend-service-dlq", "-group-id="+group).CombinedOutput()
		replayCancel()
		if replayErr != nil || !strings.Contains(string(output), "DLQ replay success") {
			t.Fatalf("real replay tool failed: %v\n%s", replayErr, output)
		}
		ends := map[int32]int64{}
		for _, part := range sourceParts {
			ends[part], err = kafka.GetOffset("friend-service", part, sarama.OffsetNewest)
			if err != nil {
				t.Fatal(err)
			}
		}
		committed := false
		until := time.Now().Add(15 * time.Second)
		for time.Now().Before(until) {
			offsets, err := admin.ListConsumerGroupOffsets("friend-service-group", map[string][]int32{"friend-service": sourceParts})
			if err != nil {
				t.Fatal(err)
			}
			committed = true
			for part, end := range ends {
				block := offsets.GetBlock("friend-service", part)
				if block == nil || block.Err != sarama.ErrNoError || block.Offset < end {
					committed = false
				}
			}
			if committed {
				break
			}
			time.Sleep(100 * time.Millisecond)
		}
		if !committed {
			t.Fatal("Friend consumer did not commit the replayed record; cannot assert replay idempotency")
		}
		t.Logf("PASS existing DLQ tool published round=%d with stable operation identity (isolated test group)", round+1)
	}
	var after db.Group
	if err := database.First(&after, "group_id = ?", groupID).Error; err != nil {
		t.Fatal(err)
	}
	var inboxes, outboxes int64
	if err := database.Model(&db.ConsumerInbox{}).Where("service = ? AND operation_key = ?", "friend", operationKey).Count(&inboxes).Error; err != nil {
		t.Fatal(err)
	}
	if err := database.Model(&db.OutboxEvent{}).Where("service = ? AND operation_key = ?", "friend", operationKey).Count(&outboxes).Error; err != nil {
		t.Fatal(err)
	}
	if after.Name != "after-replay" || before.UpdateTime != after.UpdateTime || inboxes != 1 || outboxes != 1 {
		t.Fatalf("replay repeated business mutation or logical response: inboxes=%d outboxes=%d", inboxes, outboxes)
	}
	t.Log("PASS two real DLQ replays: group update timestamp unchanged, one completed Inbox and one logical Outbox event; not physical exactly-once delivery")
}
