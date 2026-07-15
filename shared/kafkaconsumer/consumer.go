package kafkaconsumer

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	envelope "Betterfly2/proto/envelope"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type Outcome string

const (
	OutcomeSuccess   Outcome = "success"
	OutcomePermanent Outcome = "permanent"
	OutcomeTransient Outcome = "transient"
)

type Result struct {
	Outcome Outcome
	Err     error
}

func Success() Result { return Result{Outcome: OutcomeSuccess} }

func Permanent(err error) Result {
	return Result{Outcome: OutcomePermanent, Err: nonNilError(err)}
}

func Permanentf(format string, args ...any) Result {
	return Permanent(fmt.Errorf(format, args...))
}

func Transient(err error) Result {
	return Result{Outcome: OutcomeTransient, Err: nonNilError(err)}
}

func Transientf(format string, args ...any) Result {
	return Transient(fmt.Errorf(format, args...))
}

func nonNilError(err error) error {
	if err == nil {
		return errors.New("unspecified processing failure")
	}
	return err
}

type Processor func(context.Context, *sarama.ConsumerMessage) Result

type DLQPublisher func(context.Context, string, []byte, []sarama.RecordHeader) error

// RetryAfterError lets a transient dependency expose a safe retry boundary,
// such as the expiry of an ownership lease.
type RetryAfterError interface {
	error
	RetryAfter() time.Duration
}

type Config struct {
	Service        string
	DLQTopic       string
	MaxRetries     int
	InitialBackoff time.Duration
	MaxBackoff     time.Duration
	Now            func() time.Time
}

func LoadConfig(service, envPrefix, defaultDLQTopic string) Config {
	prefix := strings.ToUpper(strings.TrimSpace(envPrefix))
	if prefix != "" && !strings.HasSuffix(prefix, "_") {
		prefix += "_"
	}
	config := Config{
		Service:        service,
		DLQTopic:       envString(prefix+"KAFKA_DLQ_TOPIC", defaultDLQTopic),
		MaxRetries:     envInt(prefix+"KAFKA_PROCESS_MAX_RETRIES", 3),
		InitialBackoff: envDuration(prefix+"KAFKA_RETRY_INITIAL_BACKOFF", 100*time.Millisecond),
		MaxBackoff:     envDuration(prefix+"KAFKA_RETRY_MAX_BACKOFF", 2*time.Second),
		Now:            time.Now,
	}
	return config.normalized()
}

func (c Config) normalized() Config {
	if strings.TrimSpace(c.Service) == "" {
		c.Service = "unknown"
	}
	if c.MaxRetries < 0 {
		c.MaxRetries = 0
	}
	if c.InitialBackoff <= 0 {
		c.InitialBackoff = 100 * time.Millisecond
	}
	if c.MaxBackoff <= 0 {
		c.MaxBackoff = 2 * time.Second
	}
	if c.InitialBackoff > c.MaxBackoff {
		c.InitialBackoff = c.MaxBackoff
	}
	if c.Now == nil {
		c.Now = time.Now
	}
	return c
}

type Handler struct {
	config    Config
	processor Processor
	publish   DLQPublisher
}

func New(config Config, processor Processor, publish DLQPublisher) *Handler {
	return &Handler{config: config.normalized(), processor: processor, publish: publish}
}

func (h *Handler) Setup(sarama.ConsumerGroupSession) error   { return nil }
func (h *Handler) Cleanup(sarama.ConsumerGroupSession) error { return nil }

func (h *Handler) ConsumeClaim(session sarama.ConsumerGroupSession, claim sarama.ConsumerGroupClaim) error {
	if h.processor == nil {
		return errors.New("Kafka processor is not configured")
	}
	for message := range claim.Messages() {
		started := h.config.Now()
		if err := h.processWithRetry(session.Context(), message); err != nil {
			metrics.RecordReliableConsumerLatency(h.config.Service, started)
			logger.Sugar().Errorw("Kafka消息未确认，停止当前partition",
				"service", h.config.Service,
				"topic", message.Topic,
				"partition", message.Partition,
				"offset", message.Offset,
				"operation_key", OperationKey(message),
				"error", SummarizeError(err),
			)
			return err
		}
		session.MarkMessage(message, "")
		metrics.RecordReliableConsumerLatency(h.config.Service, started)
	}
	return nil
}

func (h *Handler) processWithRetry(ctx context.Context, message *sarama.ConsumerMessage) error {
	firstFailure := time.Time{}
	backoff := h.config.InitialBackoff
	operationCtx := WithOperationKey(ctx, OperationKey(message))

	for attempt := 0; ; attempt++ {
		if err := operationCtx.Err(); err != nil {
			return err
		}
		result := h.processor(operationCtx, message)
		switch result.Outcome {
		case OutcomeSuccess:
			metrics.RecordReliableConsumerOutcome(h.config.Service, string(OutcomeSuccess))
			return nil
		case OutcomePermanent, OutcomeTransient:
			result.Err = nonNilError(result.Err)
		default:
			result = Permanent(fmt.Errorf("processor returned unknown outcome %q", result.Outcome))
		}

		if firstFailure.IsZero() {
			firstFailure = h.config.Now().UTC()
		}
		if result.Outcome == OutcomePermanent || attempt >= h.config.MaxRetries {
			if err := operationCtx.Err(); err != nil {
				return err
			}
			return h.publishDeadLetter(operationCtx, message, result, firstFailure, attempt)
		}

		metrics.RecordReliableConsumerRetry(h.config.Service)
		delay := backoff
		var retryAfter RetryAfterError
		if errors.As(result.Err, &retryAfter) && retryAfter.RetryAfter() > delay {
			delay = retryAfter.RetryAfter()
		}
		logger.Sugar().Warnw("Kafka消息处理暂时失败，等待重试",
			"service", h.config.Service,
			"topic", message.Topic,
			"partition", message.Partition,
			"offset", message.Offset,
			"operation_key", OperationKey(message),
			"retry", attempt+1,
			"retry_delay", delay,
			"error", SummarizeError(result.Err),
		)
		timer := time.NewTimer(delay)
		select {
		case <-operationCtx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return operationCtx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > h.config.MaxBackoff {
			backoff = h.config.MaxBackoff
		}
	}
}

func (h *Handler) publishDeadLetter(ctx context.Context, message *sarama.ConsumerMessage, result Result, firstFailure time.Time, retries int) error {
	if strings.TrimSpace(h.config.DLQTopic) == "" || h.publish == nil {
		return errors.New("Kafka DLQ publisher is not configured")
	}
	envelopeType := EnvelopeType(message.Value)
	headers := DLQHeaders(h.config.Service, message, envelopeType, result, firstFailure, h.config.Now().UTC(), retries)
	if err := h.publish(ctx, h.config.DLQTopic, message.Value, headers); err != nil {
		metrics.RecordReliableConsumerDLQFailure(h.config.Service)
		return fmt.Errorf("publish %s DLQ: %w", h.config.Service, err)
	}

	metrics.RecordReliableConsumerOutcome(h.config.Service, "dlq")
	metrics.RecordReliableConsumerDLQ(h.config.Service, string(result.Outcome), envelopeType.String())
	logger.Sugar().Errorw("Kafka消息已写入服务DLQ",
		"service", h.config.Service,
		"dlq_topic", h.config.DLQTopic,
		"source_topic", message.Topic,
		"partition", message.Partition,
		"offset", message.Offset,
		"operation_key", OperationKey(message),
		"envelope_type", envelopeType.String(),
		"error_class", result.Outcome,
		"retry_count", retries,
		"error", SummarizeError(result.Err),
	)
	return nil
}

func DLQHeaders(service string, message *sarama.ConsumerMessage, envelopeType envelope.MessageType, result Result, firstFailure, finalFailure time.Time, retries int) []sarama.RecordHeader {
	values := [][2]string{
		{"schema_version", "1"},
		{"service", service},
		{"operation_key", OperationKey(message)},
		{"original_topic", message.Topic},
		{"original_partition", strconv.FormatInt(int64(message.Partition), 10)},
		{"original_offset", strconv.FormatInt(message.Offset, 10)},
		{"envelope_type", envelopeType.String()},
		{"error_class", string(result.Outcome)},
		{"sanitized_error_summary", SummarizeError(result.Err)},
		{"first_failure_time", firstFailure.Format(time.RFC3339Nano)},
		{"final_failure_time", finalFailure.Format(time.RFC3339Nano)},
		{"retry_count", strconv.Itoa(retries)},
	}
	headers := make([]sarama.RecordHeader, 0, len(values))
	for _, value := range values {
		headers = append(headers, sarama.RecordHeader{Key: []byte(value[0]), Value: []byte(value[1])})
	}
	return headers
}

func EnvelopeType(payload []byte) envelope.MessageType {
	message := &envelope.Envelope{}
	if err := proto.Unmarshal(payload, message); err != nil {
		return envelope.MessageType_UNKNOWN
	}
	return message.GetType()
}

func SummarizeError(err error) string {
	if err == nil {
		return ""
	}
	const maxBytes = 512
	summary := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(summary) > maxBytes {
		return summary[:maxBytes]
	}
	return summary
}

func OperationKey(message *sarama.ConsumerMessage) string {
	if message == nil {
		return ""
	}
	return message.Topic + "/" + strconv.FormatInt(int64(message.Partition), 10) + "/" + strconv.FormatInt(message.Offset, 10)
}

type operationKeyContextKey struct{}

func WithOperationKey(ctx context.Context, operationKey string) context.Context {
	return context.WithValue(ctx, operationKeyContextKey{}, operationKey)
}

func OperationKeyFromContext(ctx context.Context) (string, bool) {
	value, ok := ctx.Value(operationKeyContextKey{}).(string)
	return value, ok && value != ""
}

func envString(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}

func envInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value < 0 {
		return fallback
	}
	return value
}

func envDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}
