package publisher

import (
	"context"
	"fmt"
	"net"
	"os"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/mq"

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

func (p *KafkaPublisher) Publish(_ context.Context, topic string, delivery *callpb.Delivery) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty destination topic")
	}
	payload, err := mq.MarshalEnvelope(envelope.MessageType_CALL_RESPONSE, delivery)
	if err != nil {
		return err
	}
	_, _, err = p.producer.SendMessage(&sarama.ProducerMessage{Topic: topic, Value: sarama.ByteEncoder(payload)})
	return err
}

func (p *KafkaPublisher) PublishPush(_ context.Context, topic string, request *pushpb.RequestMessage) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty destination topic")
	}
	payload, err := mq.MarshalEnvelope(envelope.MessageType_PUSH_REQUEST, request)
	if err != nil {
		return err
	}
	_, _, err = p.producer.SendMessage(&sarama.ProducerMessage{Topic: topic, Value: sarama.ByteEncoder(payload)})
	return err
}

func (p *KafkaPublisher) PublishRaw(ctx context.Context, topic string, payload []byte, headers []sarama.RecordHeader) error {
	if strings.TrimSpace(topic) == "" {
		return fmt.Errorf("empty destination topic")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	_, _, err := p.producer.SendMessage(&sarama.ProducerMessage{
		Topic: topic, Value: sarama.ByteEncoder(payload), Headers: headers,
	})
	if err != nil {
		return err
	}
	return ctx.Err()
}

func (p *KafkaPublisher) Close() error {
	return p.producer.Close()
}

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
