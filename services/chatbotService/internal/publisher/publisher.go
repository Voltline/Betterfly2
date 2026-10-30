package publisher

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	"github.com/IBM/sarama"
)

type KafkaPublisher struct {
	producer sarama.SyncProducer
}

func NewKafkaPublisher(brokers []string) (*KafkaPublisher, error) {
	config := sarama.NewConfig()
	config.Producer.Return.Successes = true
	config.Producer.RequiredAcks = sarama.WaitForAll
	config.Producer.Retry.Max = 5
	config.Producer.Partitioner = sarama.NewHashPartitioner
	networkTimeout := kafkaNetworkTimeout()
	config.Net.DialTimeout = networkTimeout
	config.Net.ReadTimeout = networkTimeout
	config.Net.WriteTimeout = networkTimeout
	config.Producer.Timeout = networkTimeout
	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{producer: producer}, nil
}

func kafkaNetworkTimeout() time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv("KAFKA_NETWORK_TIMEOUT")))
	if err != nil || value <= 0 {
		return 10 * time.Second
	}
	return value
}

// PublishKeyed 同一key进同一分区；带event_id头，与DF发给下游的消息一致。
func (p *KafkaPublisher) PublishKeyed(ctx context.Context, topic, key string, payload []byte) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty destination topic")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	var id [16]byte
	if _, err := rand.Read(id[:]); err != nil {
		return err
	}
	_, _, err := p.producer.SendMessage(&sarama.ProducerMessage{
		Topic: topic, Key: sarama.StringEncoder(key), Value: sarama.ByteEncoder(payload),
		Headers: []sarama.RecordHeader{{Key: []byte("event_id"), Value: []byte(hex.EncodeToString(id[:]))}},
	})
	return err
}

// PublishRaw 供死信队列使用。
func (p *KafkaPublisher) PublishRaw(ctx context.Context, topic string, payload []byte, headers []sarama.RecordHeader) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty destination topic")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, err := p.producer.SendMessage(&sarama.ProducerMessage{Topic: topic, Value: sarama.ByteEncoder(payload), Headers: headers})
	return err
}

func (p *KafkaPublisher) Close() error { return p.producer.Close() }

func WaitForBrokers(brokers []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for _, broker := range brokers {
		for {
			connection, err := net.DialTimeout("tcp", broker, 2*time.Second)
			if err == nil {
				_ = connection.Close()
				break
			}
			if time.Now().After(deadline) {
				return fmt.Errorf("kafka broker %s not ready: %w", broker, err)
			}
			time.Sleep(time.Second)
		}
	}
	return nil
}
