package publisher

import (
	"context"
	"fmt"
	"net"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
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
	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		return nil, err
	}
	return &KafkaPublisher{producer: producer}, nil
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
