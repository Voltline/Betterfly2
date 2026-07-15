package abtest

import (
	"context"
	"fmt"
	"strings"

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
	pubsub := b.client.Subscribe(ctx, abTestInvalidationChannel)
	defer pubsub.Close()
	if _, err := pubsub.Receive(ctx); err != nil {
		return err
	}
	channel := pubsub.Channel()
	for {
		select {
		case <-ctx.Done():
			return nil
		case _, ok := <-channel:
			if !ok {
				return nil
			}
			invalidate()
		}
	}
}

func (b *RedisInvalidationBus) Close() error {
	return b.client.Close()
}
