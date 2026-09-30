package integration

import (
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	db "Betterfly2/shared/db"
	"Betterfly2/shared/mq"
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/IBM/sarama"
	"github.com/redis/go-redis/v9"
	"gorm.io/gorm"
)

func acceptanceDatabase(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("PGSQL_DSN")
	if dsn == "" {
		dsn = loadDSNFromDotEnv(t)
	}
	t.Setenv("PGSQL_DSN", strings.Trim(dsn, "\"'"))
	t.Setenv("DB_MAX_OPEN_CONNS", "2")
	t.Setenv("DB_MAX_IDLE_CONNS", "1")
	database, err := db.Open()
	if err != nil {
		t.Fatal("unable to connect to acceptance database; DSN omitted")
	}
	sqlDB, err := database.DB()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sqlDB.Close() })
	return database
}

// Compose advertises container DNS; host-side tests map only these known brokers.
type composeBrokerDialer struct{ net.Dialer }

func (d composeBrokerDialer) Dial(network, address string) (net.Conn, error) {
	switch address {
	case "kafka1:9092":
		address = "127.0.0.1:9092"
	case "kafka2:9094":
		address = "127.0.0.1:9094"
	}
	return d.Dialer.Dial(network, address)
}

func acceptanceKafka(t *testing.T) (sarama.Client, sarama.SyncProducer) {
	t.Helper()
	config := sarama.NewConfig()
	config.Version = sarama.V2_1_0_0
	config.Net.DialTimeout, config.Net.ReadTimeout, config.Net.WriteTimeout = 5*time.Second, 5*time.Second, 5*time.Second
	config.Net.Proxy.Enable = true
	config.Net.Proxy.Dialer = composeBrokerDialer{net.Dialer{Timeout: 5 * time.Second}}
	config.Producer.Return.Successes = true
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Partitioner = sarama.NewManualPartitioner
	client, err := sarama.NewClient([]string{"127.0.0.1:9092", "127.0.0.1:9094"}, config)
	if err != nil {
		t.Fatal(err)
	}
	producer, err := sarama.NewSyncProducerFromClient(client)
	if err != nil {
		_ = client.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = producer.Close(); _ = client.Close() })
	return client, producer
}

type inboxHistory struct {
	Service   string
	Topic     string
	Partition int32
	MaxOffset int64
}

// Do not delete history. Advance only colliding offsets with read-only requests.
// This isolates measurements, not a production fix for Kafka log loss.
func prepareAcceptanceOffsets(t *testing.T, database *gorm.DB, probe *networkProbe) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	var rows []inboxHistory
	if err := database.WithContext(ctx).Raw(`SELECT service, split_part(operation_key,'/',1) AS topic, CAST(split_part(operation_key,'/',2) AS integer) AS partition, MAX(CAST(split_part(operation_key,'/',3) AS bigint)) AS max_offset FROM consumer_inboxes WHERE operation_key ~ '^(friend-service|storage-service)/[0-9]+/[0-9]+$' GROUP BY service, topic, partition ORDER BY topic, partition`).Scan(&rows).Error; err != nil {
		t.Fatal(err)
	}
	redisClient := redis.NewClient(&redis.Options{Addr: "127.0.0.1:6379"})
	defer redisClient.Close()
	container, err := redisClient.HGet(ctx, "ws_connection_mapping", fmt.Sprint(probe.userID)).Result()
	if err != nil {
		t.Fatal(err)
	}
	kafka, producer := acceptanceKafka(t)
	advanced := 0
	for _, row := range rows {
		end, err := kafka.GetOffset(row.Topic, row.Partition, sarama.OffsetNewest)
		if err != nil {
			t.Fatal(err)
		}
		if end > row.MaxOffset {
			continue
		}
		count := row.MaxOffset - end + 1
		if count > 512 {
			t.Fatalf("unsafe historical offset gap: topic=%s partition=%d gap=%d; no data deleted", row.Topic, row.Partition, count)
		}
		t.Logf("ENVIRONMENT_FAILURE Kafka offset reused: topic=%s partition=%d end=%d historical_max=%d; advancing %d read-only records for measurement only", row.Topic, row.Partition, end, row.MaxOffset, count)
		var payload []byte
		if row.Topic == "friend-service" {
			payload, err = mq.MarshalEnvelope(envelope.MessageType_FRIEND_REQUEST, &friend.RequestMessage{FromKafkaTopic: container, TargetUserId: probe.userID, Payload: &friend.RequestMessage_QueryJoinedGroups{QueryJoinedGroups: &friend.QueryJoinedGroups{UserId: probe.userID}}})
		} else {
			payload, err = mq.MarshalEnvelope(envelope.MessageType_STORAGE_REQUEST, &storage.RequestMessage{FromKafkaTopic: container, TargetUserId: probe.userID, Payload: &storage.RequestMessage_QuerySyncMessages{QuerySyncMessages: &storage.QuerySyncMessages{ToUserId: probe.userID, Timestamp: time.Now().UTC().Format(time.RFC3339), PageSize: 1}}})
		}
		if err != nil {
			t.Fatal(err)
		}
		for i := int64(0); i < count; i++ {
			if ctx.Err() != nil {
				t.Fatal(ctx.Err())
			}
			if _, _, err := producer.SendMessage(&sarama.ProducerMessage{Topic: row.Topic, Partition: row.Partition, Value: sarama.ByteEncoder(payload)}); err != nil {
				t.Fatal(err)
			}
		}
		advanced += int(count)
	}
	if advanced > 0 {
		t.Logf("measurement isolation advanced %d read-only records; persistent Kafka storage remains an unresolved deployment issue", advanced)
	}
}

func acceptanceDLQOffsets(t *testing.T, client sarama.Client) map[string]int64 {
	t.Helper()
	result := map[string]int64{}
	for _, topic := range []string{"data-forwarding-dlq", "storage-service-dlq", "friend-service-dlq", "call-service-dlq", "push-service-dlq"} {
		partitions, err := client.Partitions(topic)
		if err != nil {
			t.Fatal(err)
		}
		for _, partition := range partitions {
			offset, err := client.GetOffset(topic, partition, sarama.OffsetNewest)
			if err != nil {
				t.Fatal(err)
			}
			result[topic] += offset
		}
	}
	return result
}
