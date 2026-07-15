package abtest

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

const abTestInvalidationChannel = "abtest:snapshot:invalidate"

type InvalidationBus interface {
	Publish(context.Context) error
	Subscribe(context.Context, func()) error
}

type RedisInvalidationBus struct {
	client *redis.Client
}

var publishInvalidationScript = redis.NewScript(`
local version = redis.call('INCR', KEYS[1])
redis.call('PUBLISH', ARGV[1], tostring(version))
return version
`)

func NewRedisInvalidationBus(ctx context.Context, address string) (*RedisInvalidationBus, error) {
	address = strings.TrimSpace(address)
	if address == "" {
		address = "localhost:6379"
	}
	client := redis.NewClient(&redis.Options{Addr: address})
	if err := client.Ping(ctx).Err(); err != nil {
		_ = client.Close()
		return nil, fmt.Errorf("connect AB Test invalidation Redis: %w", err)
	}
	return &RedisInvalidationBus{client: client}, nil
}

func (b *RedisInvalidationBus) Publish(ctx context.Context) error {
	return publishInvalidationScript.Run(ctx, b.client, []string{"abtest:snapshot:version"}, abTestInvalidationChannel).Err()
}

func (b *RedisInvalidationBus) Subscribe(ctx context.Context, invalidate func()) error {
	backoff := 100 * time.Millisecond
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		pubsub := b.client.Subscribe(ctx, abTestInvalidationChannel)
		if _, err := pubsub.Receive(ctx); err != nil {
			_ = pubsub.Close()
			if !waitInvalidationReconnect(ctx, backoff) {
				return nil
			}
			backoff = nextInvalidationBackoff(backoff)
			continue
		}
		backoff = 100 * time.Millisecond
		channel := pubsub.Channel()
		connected := true
		for connected {
			select {
			case <-ctx.Done():
				_ = pubsub.Close()
				return nil
			case _, ok := <-channel:
				if !ok {
					connected = false
					continue
				}
				invalidate()
			}
		}
		_ = pubsub.Close()
		if !waitInvalidationReconnect(ctx, backoff) {
			return nil
		}
		backoff = nextInvalidationBackoff(backoff)
	}
}

func waitInvalidationReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextInvalidationBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > 5*time.Second {
		return 5 * time.Second
	}
	return current
}

func (b *RedisInvalidationBus) Close() error {
	return b.client.Close()
}
