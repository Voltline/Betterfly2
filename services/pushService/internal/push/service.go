package push

import (
	"context"
	"encoding/hex"
	"os"
	"strconv"
	"strings"
	"time"

	pushpb "Betterfly2/proto/push"
	"Betterfly2/shared/kafkaconsumer"
)

type Service struct {
	store          Store
	sender         Sender
	bundleID       string
	now            func() time.Time
	maxConcurrency int
	deliveryLease  time.Duration
	workerPoll     time.Duration
	sendTimeout    time.Duration
	maxAttempts    int
	retryInitial   time.Duration
	retryMax       time.Duration
}

func NewService(store Store, sender Sender, bundleID string) *Service {
	service := &Service{
		store: store, sender: sender, bundleID: strings.TrimSpace(bundleID), now: time.Now,
		maxConcurrency: envPositiveInt("PUSH_APNS_MAX_CONCURRENCY", 16),
		deliveryLease:  envPositiveDuration("PUSH_DELIVERY_LEASE", 30*time.Second),
		workerPoll:     envPositiveDuration("PUSH_WORKER_POLL_INTERVAL", 250*time.Millisecond),
		sendTimeout:    envPositiveDuration("PUSH_APNS_SEND_TIMEOUT", 10*time.Second),
		maxAttempts:    envPositiveInt("PUSH_DELIVERY_MAX_ATTEMPTS", 10),
		retryInitial:   envPositiveDuration("PUSH_DELIVERY_RETRY_INITIAL_BACKOFF", time.Second),
		retryMax:       envPositiveDuration("PUSH_DELIVERY_RETRY_MAX_BACKOFF", 15*time.Minute),
	}
	if service.sendTimeout >= service.deliveryLease {
		service.sendTimeout = service.deliveryLease / 2
	}
	if service.retryInitial > service.retryMax {
		service.retryInitial = service.retryMax
	}
	return service
}

func (s *Service) Ready(ctx context.Context) error {
	if err := s.store.Ping(ctx); err != nil {
		return err
	}
	return s.sender.Ready()
}

func (s *Service) Handle(ctx context.Context, request *pushpb.RequestMessage) error {
	if request == nil {
		return ErrInvalidRequest
	}
	operationKey, ok := kafkaconsumer.OperationKeyFromContext(ctx)
	if !ok || strings.TrimSpace(operationKey) == "" {
		return ErrInvalidRequest
	}
	return s.store.EnqueueRequest(ctx, operationKey, request, s.bundleID)
}

func uniquePushTargets(values []int64, senderUserID int64) []int64 {
	seen := make(map[int64]struct{}, len(values))
	result := make([]int64, 0, len(values))
	for _, value := range values {
		if value <= 0 || value == senderUserID {
			continue
		}
		if _, exists := seen[value]; exists {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	return result
}

func envPositiveInt(key string, fallback int) int {
	value, err := strconv.Atoi(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func envPositiveDuration(key string, fallback time.Duration) time.Duration {
	value, err := time.ParseDuration(strings.TrimSpace(os.Getenv(key)))
	if err != nil || value <= 0 {
		return fallback
	}
	return value
}

func defaultMessagePreview(messageType string) string {
	switch strings.ToLower(strings.TrimSpace(messageType)) {
	case "image":
		return "发送了一张图片"
	case "gif":
		return "发送了一个 GIF"
	case "file":
		return "发送了一个文件"
	case "audio":
		return "发送了一条语音"
	case "video":
		return "发送了一段视频"
	default:
		return "发来一条消息"
	}
}

func validToken(token string) bool {
	token = strings.TrimSpace(token)
	if len(token) < 32 || len(token) > 256 || len(token)%2 != 0 {
		return false
	}
	_, err := hex.DecodeString(token)
	return err == nil
}

func validDeviceID(deviceID string) bool {
	length := len(strings.TrimSpace(deviceID))
	return length > 0 && length <= 128
}

func validEnvironment(environment pushpb.PushEnvironment) bool {
	return environment == pushpb.PushEnvironment_SANDBOX || environment == pushpb.PushEnvironment_PRODUCTION
}

func environmentName(environment pushpb.PushEnvironment) string {
	if environment == pushpb.PushEnvironment_PRODUCTION {
		return "production"
	}
	return "sandbox"
}

func parseEnvironment(environment string) pushpb.PushEnvironment {
	if strings.EqualFold(environment, "production") {
		return pushpb.PushEnvironment_PRODUCTION
	}
	return pushpb.PushEnvironment_SANDBOX
}

func clientEvent(operation string, result pushpb.PushResult, deviceID string, environment pushpb.PushEnvironment, message string, now time.Time) *pushpb.ClientEvent {
	return &pushpb.ClientEvent{Operation: operation, Result: result, DeviceId: deviceID, Environment: environment, Message: message, Timestamp: now.UTC().Format(time.RFC3339Nano)}
}
