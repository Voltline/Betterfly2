package consumer

import (
	callpb "Betterfly2/proto/call"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	pushpb "Betterfly2/proto/push"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"context"
	"data_forwarding_service/internal/handlers"
	"data_forwarding_service/internal/publisher"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/IBM/sarama"
	"google.golang.org/protobuf/proto"
)

type failureClass string

const (
	failurePermanent failureClass = "permanent"
	failureTransient failureClass = "transient"
)

type classifiedError struct {
	class failureClass
	err   error
}

func (e *classifiedError) Error() string { return e.err.Error() }
func (e *classifiedError) Unwrap() error { return e.err }

func permanentError(format string, args ...any) error {
	return &classifiedError{class: failurePermanent, err: fmt.Errorf(format, args...)}
}

func classifyProcessingError(err error) failureClass {
	var classified *classifiedError
	if errors.As(err, &classified) {
		return classified.class
	}
	return failureTransient
}

type consumerRetryConfig struct {
	maxRetries     int
	initialBackoff time.Duration
	maxBackoff     time.Duration
	dlqTopic       string
}

func loadConsumerRetryConfig() consumerRetryConfig {
	config := consumerRetryConfig{
		maxRetries:     envInt("DF_KAFKA_PROCESS_MAX_RETRIES", 3),
		initialBackoff: envDuration("DF_KAFKA_RETRY_INITIAL_BACKOFF", 100*time.Millisecond),
		maxBackoff:     envDuration("DF_KAFKA_RETRY_MAX_BACKOFF", 2*time.Second),
		dlqTopic:       envString("DF_KAFKA_DLQ_TOPIC", "data-forwarding-dlq"),
	}
	if config.initialBackoff > config.maxBackoff {
		config.initialBackoff = config.maxBackoff
	}
	return config
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

type deadLetterMessage struct {
	OriginalTopic     string               `json:"original_topic"`
	OriginalPartition int32                `json:"original_partition"`
	OriginalOffset    int64                `json:"original_offset"`
	EnvelopeType      envelope.MessageType `json:"envelope_type"`
	OriginalPayload   []byte               `json:"original_payload"`
	ErrorClass        failureClass         `json:"error_class"`
	ErrorSummary      string               `json:"error_summary"`
	FirstFailureTime  time.Time            `json:"first_failure_time"`
	FinalFailureTime  time.Time            `json:"final_failure_time"`
	RetryCount        int                  `json:"retry_count"`
}

func (h *NewKafkaConsumerGroupHandler) initializeProcessingDefaults() {
	if h.retryConfig.dlqTopic == "" {
		h.retryConfig = loadConsumerRetryConfig()
	}
	if h.processMessageFn == nil {
		h.processMessageFn = h.processMessage
	}
	if h.publishDLQFn == nil {
		h.publishDLQFn = func(topic string, payload []byte) error {
			return publisher.PublishMessage(string(payload), topic)
		}
	}
}

func (h *NewKafkaConsumerGroupHandler) processWithRetry(ctx context.Context, msg *sarama.ConsumerMessage) error {
	h.initializeProcessingDefaults()
	firstFailure := time.Time{}
	backoff := h.retryConfig.initialBackoff

	for attempt := 0; ; attempt++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		err := h.processMessageFn(msg)
		if err == nil {
			return nil
		}
		if firstFailure.IsZero() {
			firstFailure = time.Now().UTC()
		}

		class := classifyProcessingError(err)
		if class == failurePermanent || attempt >= h.retryConfig.maxRetries {
			if err := ctx.Err(); err != nil {
				return err
			}
			return h.publishDeadLetter(msg, class, err, firstFailure, attempt)
		}

		logger.Sugar().Warnw("Kafka消息处理暂时失败，等待重试",
			"topic", msg.Topic,
			"partition", msg.Partition,
			"offset", msg.Offset,
			"retry", attempt+1,
			"error_class", class,
			"error", summarizeError(err),
		)
		metrics.RecordKafkaProcessingError()

		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			return ctx.Err()
		case <-timer.C:
		}
		backoff *= 2
		if backoff > h.retryConfig.maxBackoff {
			backoff = h.retryConfig.maxBackoff
		}
	}
}

func (h *NewKafkaConsumerGroupHandler) publishDeadLetter(msg *sarama.ConsumerMessage, class failureClass, processingErr error, firstFailure time.Time, retryCount int) error {
	record := deadLetterMessage{
		OriginalTopic:     msg.Topic,
		OriginalPartition: msg.Partition,
		OriginalOffset:    msg.Offset,
		EnvelopeType:      envelopeTypeOf(msg.Value),
		OriginalPayload:   msg.Value,
		ErrorClass:        class,
		ErrorSummary:      summarizeError(processingErr),
		FirstFailureTime:  firstFailure,
		FinalFailureTime:  time.Now().UTC(),
		RetryCount:        retryCount,
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return fmt.Errorf("序列化DLQ消息失败: %w", err)
	}
	if err := h.publishDLQFn(h.retryConfig.dlqTopic, payload); err != nil {
		return fmt.Errorf("写入Kafka DLQ失败: %w", err)
	}

	logger.Sugar().Errorw("Kafka消息已写入DLQ",
		"topic", msg.Topic,
		"partition", msg.Partition,
		"offset", msg.Offset,
		"envelope_type", record.EnvelopeType.String(),
		"error_class", class,
		"retry_count", retryCount,
		"error", record.ErrorSummary,
	)
	metrics.RecordKafkaProcessingError()
	metrics.RecordKafkaMessageProduced(h.retryConfig.dlqTopic)
	return nil
}

func summarizeError(err error) string {
	if err == nil {
		return ""
	}
	const maxSummaryBytes = 512
	summary := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(summary) > maxSummaryBytes {
		return summary[:maxSummaryBytes]
	}
	return summary
}

func envelopeTypeOf(payload []byte) envelope.MessageType {
	env := &envelope.Envelope{}
	if err := proto.Unmarshal(payload, env); err != nil {
		return envelope.MessageType_UNKNOWN
	}
	return env.GetType()
}

func (h *NewKafkaConsumerGroupHandler) processMessage(msg *sarama.ConsumerMessage) error {
	if matches := deleteUserPatternCapture.FindStringSubmatch(string(msg.Value)); len(matches) == 3 {
		currentContainerID := envString("HOSTNAME", "local")
		if matches[2] != currentContainerID {
			return nil
		}
		if h.wsHandler == nil {
			return errors.New("WebSocket处理器未设置，无法踢出用户")
		}
		h.wsHandler.StopClient(matches[1])
		return nil
	}

	env := &envelope.Envelope{}
	if err := proto.Unmarshal(msg.Value, env); err != nil {
		return permanentError("Envelope解析失败: %v", err)
	}
	if env.GetType() == envelope.MessageType_UNKNOWN || len(env.GetPayload()) == 0 {
		// 兼容迁移前未封装 Envelope 的 storage response 和 DF request。
		legacyStorage := &storage.ResponseMessage{}
		if err := proto.Unmarshal(msg.Value, legacyStorage); err == nil && legacyStorage.GetTargetUserId() > 0 {
			return h.handleStorageResponse(legacyStorage)
		}
		legacyRequest, err := handlers.HandleRequestData(msg.Value)
		if err == nil && legacyRequest.GetPost() != nil {
			return handlers.InplaceHandlePostMessage(legacyRequest)
		}
		return permanentError("Envelope字段不完整: type=%s payload_bytes=%d", env.GetType(), len(env.GetPayload()))
	}

	switch env.GetType() {
	case envelope.MessageType_STORAGE_RESPONSE:
		response := &storage.ResponseMessage{}
		if err := proto.Unmarshal(env.Payload, response); err != nil {
			return permanentError("STORAGE_RESPONSE payload解析失败: %v", err)
		}
		if response.GetTargetUserId() <= 0 {
			return permanentError("STORAGE_RESPONSE缺少有效target_user_id")
		}
		return h.handleStorageResponse(response)
	case envelope.MessageType_FRIEND_RESPONSE:
		response := &friend.ResponseMessage{}
		if err := proto.Unmarshal(env.Payload, response); err != nil {
			return permanentError("FRIEND_RESPONSE payload解析失败: %v", err)
		}
		if response.GetTargetUserId() <= 0 {
			return permanentError("FRIEND_RESPONSE缺少有效target_user_id")
		}
		return h.handleFriendResponse(response)
	case envelope.MessageType_CALL_RESPONSE:
		delivery := &callpb.Delivery{}
		if err := proto.Unmarshal(env.Payload, delivery); err != nil {
			return permanentError("CALL_RESPONSE payload解析失败: %v", err)
		}
		if delivery.GetTargetUserId() <= 0 || delivery.GetEvent() == nil {
			return permanentError("CALL_RESPONSE字段不完整")
		}
		return h.handleCallDelivery(delivery)
	case envelope.MessageType_PUSH_RESPONSE:
		response := &pushpb.ResponseMessage{}
		if err := proto.Unmarshal(env.Payload, response); err != nil {
			return permanentError("PUSH_RESPONSE payload解析失败: %v", err)
		}
		delivery := response.GetClientDelivery()
		if delivery == nil || delivery.GetTargetUserId() <= 0 || delivery.GetEvent() == nil {
			return permanentError("PUSH_RESPONSE字段不完整")
		}
		return h.handlePushResponse(response)
	case envelope.MessageType_DF_REQUEST:
		request, err := handlers.HandleRequestData(env.Payload)
		if err != nil {
			return permanentError("DF_REQUEST payload解析失败: %v", err)
		}
		if request.GetPost() == nil {
			return permanentError("DF_REQUEST不是Post报文")
		}
		if err := handlers.ValidatePostPayload(request.GetPost()); err != nil {
			return permanentError("DF_REQUEST字段不合法: %v", err)
		}
		return handlers.InplaceHandlePostMessage(request)
	case envelope.MessageType_DF_RESPONSE:
		if err := h.handleDFResponse(env.Payload); err != nil {
			return err
		}
		return nil
	default:
		return permanentError("data forwarding服务不处理Envelope类型: %s", env.GetType())
	}
}
