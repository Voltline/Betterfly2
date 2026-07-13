package main

import (
	"context"
	"flag"
	"fmt"
	"log"
	"os"
	"strings"
	"sync/atomic"

	"github.com/IBM/sarama"
)

type replayConfig struct {
	dryRun   bool
	max      int64
	allowed  map[string]struct{}
	dlqTopic string
	groupID  string
}

type replayHandler struct {
	config  replayConfig
	publish func(string, []byte) error
	cancel  context.CancelFunc
	seen    atomic.Int64
}

func (h *replayHandler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *replayHandler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *replayHandler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	for message := range claim.Messages() {
		mark, err := h.process(message)
		if err != nil {
			return err
		}
		if mark {
			session.MarkMessage(message, "")
		}
		if h.seen.Add(1) >= h.config.max {
			h.cancel()
			return nil
		}
	}
	return nil
}

func (h *replayHandler) process(message *sarama.ConsumerMessage) (bool, error) {
	headers := headerValues(message.Headers)
	topic := headers["original_topic"]
	if _, allowed := h.config.allowed[topic]; !allowed {
		return false, fmt.Errorf("original_topic不在重放allowlist中: %q", topic)
	}
	if h.config.dryRun {
		log.Printf("DLQ dry-run: topic=%s partition=%s offset=%s envelope_type=%s error_class=%s",
			topic, headers["original_partition"], headers["original_offset"], headers["envelope_type"], headers["error_class"])
		return false, nil
	}
	if err := h.publish(topic, message.Value); err != nil {
		return false, err
	}
	log.Printf("DLQ replay success: topic=%s partition=%s offset=%s", topic, headers["original_partition"], headers["original_offset"])
	return true, nil
}

func headerValues(headers []*sarama.RecordHeader) map[string]string {
	result := make(map[string]string, len(headers))
	for _, header := range headers {
		result[string(header.Key)] = string(header.Value)
	}
	return result
}

func parseAllowlist(raw string) map[string]struct{} {
	result := make(map[string]struct{})
	for _, topic := range strings.Split(raw, ",") {
		if topic = strings.TrimSpace(topic); topic != "" {
			result[topic] = struct{}{}
		}
	}
	return result
}

func main() {
	dryRun := flag.Bool("dry-run", true, "inspect without publishing or committing DLQ offsets")
	maxMessages := flag.Int64("max", 100, "maximum number of DLQ messages to inspect or replay")
	allowTopics := flag.String("allow-topics", "", "comma-separated original topic allowlist")
	dlqTopic := flag.String("dlq-topic", "data-forwarding-dlq", "DLQ topic")
	groupID := flag.String("group-id", "data-forwarding-dlq-replay", "dedicated replay consumer group")
	flag.Parse()
	if *maxMessages <= 0 || len(parseAllowlist(*allowTopics)) == 0 {
		log.Fatal("-max must be positive and -allow-topics must not be empty")
	}

	config := sarama.NewConfig()
	config.Consumer.Offsets.Initial = sarama.OffsetOldest
	config.Producer.Return.Successes = true
	brokerValue := strings.TrimSpace(os.Getenv("KAFKA_BROKER"))
	if brokerValue == "" {
		brokerValue = "localhost:9092"
	}
	brokers := strings.Split(brokerValue, ",")
	producer, err := sarama.NewSyncProducer(brokers, config)
	if err != nil {
		log.Fatal(err)
	}
	defer producer.Close()
	group, err := sarama.NewConsumerGroup(brokers, *groupID, config)
	if err != nil {
		log.Fatal(err)
	}
	defer group.Close()

	ctx, cancel := context.WithCancel(context.Background())
	handler := &replayHandler{
		config: replayConfig{dryRun: *dryRun, max: *maxMessages, allowed: parseAllowlist(*allowTopics), dlqTopic: *dlqTopic, groupID: *groupID},
		cancel: cancel,
		publish: func(topic string, value []byte) error {
			_, _, err := producer.SendMessage(&sarama.ProducerMessage{Topic: topic, Value: sarama.ByteEncoder(value)})
			return err
		},
	}
	for ctx.Err() == nil {
		if err := group.Consume(ctx, []string{*dlqTopic}, handler); err != nil && ctx.Err() == nil {
			log.Fatal(err)
		}
	}
}
