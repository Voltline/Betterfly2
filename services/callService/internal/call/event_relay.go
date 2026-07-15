package call

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"Betterfly2/shared/logger"
	"github.com/IBM/sarama"
	"github.com/redis/go-redis/v9"
)

const callOutboxGroup = "call-service-outbox"

type RawEventPublisher func(context.Context, string, []byte, []sarama.RecordHeader) error

type EventRelay struct {
	client    *redis.Client
	publish   RawEventPublisher
	consumer  string
	claimIdle time.Duration
	ledgerTTL time.Duration
}

func NewEventRelay(client *redis.Client, publish RawEventPublisher) *EventRelay {
	host, _ := os.Hostname()
	replayWindow := callDurationEnv("KAFKA_MAX_REPLAY_WINDOW", 7*24*time.Hour)
	ledgerTTL := callDurationEnv("CALL_OUTBOX_SENT_RETENTION", 30*24*time.Hour)
	if ledgerTTL <= replayWindow {
		ledgerTTL = replayWindow + 24*time.Hour
	}
	return &EventRelay{
		client: client, publish: publish,
		consumer:  fmt.Sprintf("%s-%d", host, os.Getpid()),
		claimIdle: 30 * time.Second, ledgerTTL: ledgerTTL,
	}
}

func (r *EventRelay) Run(ctx context.Context) error {
	if r.client == nil || r.publish == nil {
		return errors.New("call event relay is not configured")
	}
	if err := r.client.XGroupCreateMkStream(ctx, callOutboxStream, callOutboxGroup, "0").Err(); err != nil && !strings.Contains(err.Error(), "BUSYGROUP") {
		return err
	}
	for {
		if ctx.Err() != nil {
			return nil
		}
		messages, _, err := r.client.XAutoClaim(ctx, &redis.XAutoClaimArgs{
			Stream: callOutboxStream, Group: callOutboxGroup, Consumer: r.consumer,
			MinIdle: r.claimIdle, Start: "0-0", Count: 100,
		}).Result()
		if err != nil && !errors.Is(err, redis.Nil) {
			logger.Sugar().Warnw("领取过期Call事件失败", "error", err)
		}
		if len(messages) > 0 {
			if err := r.process(ctx, messages); err != nil {
				logger.Sugar().Warnw("发布领取的Call事件失败", "error", err)
			}
			continue
		}

		streams, err := r.client.XReadGroup(ctx, &redis.XReadGroupArgs{
			Group: callOutboxGroup, Consumer: r.consumer,
			Streams: []string{callOutboxStream, ">"}, Count: 100, Block: 500 * time.Millisecond,
		}).Result()
		if errors.Is(err, redis.Nil) {
			continue
		}
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			logger.Sugar().Warnw("读取Call事件流失败", "error", err)
			timer := time.NewTimer(time.Second)
			select {
			case <-ctx.Done():
				timer.Stop()
				return nil
			case <-timer.C:
			}
			continue
		}
		for _, stream := range streams {
			if err := r.process(ctx, stream.Messages); err != nil {
				logger.Sugar().Warnw("发布Call事件失败，保留pending等待重试", "error", err)
			}
		}
	}
}

func (r *EventRelay) process(ctx context.Context, messages []redis.XMessage) error {
	for _, message := range messages {
		eventID := streamString(message.Values["event_id"])
		operationKey := streamString(message.Values["operation_key"])
		topic := streamString(message.Values["topic"])
		payload := streamBytes(message.Values["payload"])
		if eventID == "" || topic == "" || len(payload) == 0 {
			logger.Sugar().Errorw("Call事件流记录损坏，确认并丢弃", "stream_id", message.ID)
			if err := r.client.XAck(ctx, callOutboxStream, callOutboxGroup, message.ID).Err(); err != nil {
				return err
			}
			continue
		}
		ledgerKey := "call:outbox:sent:" + eventID
		sent, err := r.client.Exists(ctx, ledgerKey).Result()
		if err != nil {
			return err
		}
		if sent == 0 {
			headers := []sarama.RecordHeader{
				{Key: []byte("event_id"), Value: []byte(eventID)},
				{Key: []byte("operation_key"), Value: []byte(operationKey)},
				{Key: []byte("outbox_service"), Value: []byte("call")},
			}
			if err := r.publish(ctx, topic, payload, headers); err != nil {
				return err
			}
		}
		_, err = r.client.TxPipelined(ctx, func(pipe redis.Pipeliner) error {
			pipe.Set(ctx, ledgerKey, strconv.FormatInt(time.Now().UTC().Unix(), 10), r.ledgerTTL)
			pipe.XAck(ctx, callOutboxStream, callOutboxGroup, message.ID)
			pipe.XDel(ctx, callOutboxStream, message.ID)
			return nil
		})
		if err != nil {
			return err
		}
	}
	return nil
}

func streamString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case []byte:
		return string(typed)
	default:
		return fmt.Sprint(typed)
	}
}

func streamBytes(value any) []byte {
	switch typed := value.(type) {
	case []byte:
		return append([]byte(nil), typed...)
	case string:
		return []byte(typed)
	default:
		return nil
	}
}
