package integration

import (
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	pb "Betterfly2/proto/data_forwarding"
	"github.com/IBM/sarama"
)

func TestRequestIdentityEndToEnd(t *testing.T) {
	if os.Getenv("BETTERFLY_ACCEPTANCE") != "1" {
		t.Skip("requires a dedicated test environment with no concurrent Storage producers")
	}
	database := acceptanceDatabase(t)
	p, err := openNetworkProbe(envOr("BETTERFLY_DF_PORT_1", defaultDFPort1))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(p.close)
	if err := p.signup(randomAccount(t, "identity")); err != nil {
		t.Fatal(err)
	}
	kafka, _ := acceptanceKafka(t)
	partitions, err := kafka.Partitions("storage-service")
	if err != nil {
		t.Fatal(err)
	}
	marker := []byte(randomAccount(t, "fixture"))
	var keys []string
	t.Cleanup(func() {
		if len(keys) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		if err := database.WithContext(ctx).Where("service = ? AND operation_key IN ? AND response_payload = ?", "storage", keys, marker).Delete(&db.ConsumerInbox{}).Error; err != nil {
			t.Errorf("test fixture cleanup: %v", err)
		}
	})
	starts := map[int32]int64{}
	for _, partition := range partitions {
		end, err := kafka.GetOffset("storage-service", partition, sarama.OffsetNewest)
		if err != nil {
			t.Fatal(err)
		}
		starts[partition] = end
		key := fmt.Sprintf("storage-service/%d/%d", partition, end)
		now := db.FormatReliabilityTime(time.Now())
		// Seed only this test's future offsets, never overwrite historic rows.
		row := db.ConsumerInbox{Service: "storage", OperationKey: key, Status: db.InboxStatusCompleted, ResponsePayload: marker, CreatedAt: now, CompletedAt: now}
		if err := database.Create(&row).Error; err != nil {
			t.Fatal("unable to create isolated offset-collision fixture")
		}
		keys = append(keys, key)
	}
	probeSync(t, p, &pb.QuerySyncMessages{Timestamp: time.Now().UTC().Format(time.RFC3339Nano), PageSize: 1})
	consumer, err := sarama.NewConsumerFromClient(kafka)
	if err != nil {
		t.Fatal(err)
	}
	defer consumer.Close()
	requests := 0
	for _, partition := range partitions {
		end, err := kafka.GetOffset("storage-service", partition, sarama.OffsetNewest)
		if err != nil {
			t.Fatal(err)
		}
		if end == starts[partition] {
			continue
		}
		if end != starts[partition]+1 {
			t.Fatal("concurrent Storage traffic invalidated isolation")
		}
		claim, err := consumer.ConsumePartition("storage-service", partition, starts[partition])
		if err != nil {
			t.Fatal(err)
		}
		select {
		case message := <-claim.Messages():
			key := kafkaconsumer.OperationKey(message)
			if key == fmt.Sprintf("storage-service/%d/%d", partition, message.Offset) {
				t.Fatal("new DF request still depends on reused Kafka offset")
			}
			var count int64
			if err := database.Model(&db.ConsumerInbox{}).Where("service = ? AND operation_key = ? AND status = ?", "storage", key, db.InboxStatusCompleted).Count(&count).Error; err != nil || count != 1 {
				t.Fatal("event-identified request did not create its own completed Inbox")
			}
			requests++
		case <-time.After(5 * time.Second):
			_ = claim.Close()
			t.Fatal("source request read timeout")
		}
		_ = claim.Close()
	}
	if requests != 1 {
		t.Fatalf("expected one isolated source request, got %d", requests)
	}
	t.Log("PASS actual DF request completes despite completed Inbox fixtures at every possible source offset; fixtures removed without resetting Kafka")
}
