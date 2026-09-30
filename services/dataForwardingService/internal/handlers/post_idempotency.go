package handlers

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	pb "Betterfly2/proto/data_forwarding"
	redisClient "data_forwarding_service/internal/redis"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

const (
	postPendingTTL   = 30 * time.Second
	postCompletedTTL = 7 * 24 * time.Hour
	postEffectsTTL   = 30 * 24 * time.Hour
)

type postClaim struct {
	acquired  bool
	messageID int64
}

func ensurePostClientMessageID(post *pb.Post) string {
	if id := strings.TrimSpace(post.GetClientMessageId()); id != "" {
		post.ClientMessageId = id
		return id
	}

	clone := proto.Clone(post).(*pb.Post)
	clone.ClientMessageId = ""
	payload, _ := proto.MarshalOptions{Deterministic: true}.Marshal(clone)
	digest := sha256.Sum256(payload)
	id := "legacy:" + hex.EncodeToString(digest[:])
	post.ClientMessageId = id
	return id
}

func claimPost(ctx context.Context, senderUserID int64, clientMessageID string) (postClaim, error) {
	if redisClient.Rdb == nil {
		return postClaim{acquired: true}, nil
	}

	key := postIdempotencyKey(senderUserID, clientMessageID)
	acquired, err := redisClient.Rdb.SetNX(ctx, key, "pending", postPendingTTL).Result()
	if err != nil {
		return postClaim{}, fmt.Errorf("申请消息幂等键失败: %w", err)
	}
	if acquired {
		return postClaim{acquired: true}, nil
	}

	value, err := redisClient.Rdb.Get(ctx, key).Result()
	if errors.Is(err, redis.Nil) {
		return claimPost(ctx, senderUserID, clientMessageID)
	}
	if err != nil {
		return postClaim{}, fmt.Errorf("读取消息幂等状态失败: %w", err)
	}
	messageID, _ := strconv.ParseInt(strings.TrimPrefix(value, "ack:"), 10, 64)
	return postClaim{messageID: messageID}, nil
}

func releasePostClaim(ctx context.Context, senderUserID int64, clientMessageID string) {
	if redisClient.Rdb == nil {
		return
	}
	key := postIdempotencyKey(senderUserID, clientMessageID)
	if value, err := redisClient.Rdb.Get(ctx, key).Result(); err == nil && value == "pending" {
		_ = redisClient.Rdb.Del(ctx, key).Err()
	}
}

func CompletePostIdempotency(ctx context.Context, senderUserID int64, clientMessageID string, messageID int64) error {
	if redisClient.Rdb == nil || clientMessageID == "" || messageID <= 0 {
		return nil
	}
	return redisClient.Rdb.Set(ctx, postIdempotencyKey(senderUserID, clientMessageID), "ack:"+strconv.FormatInt(messageID, 10), postCompletedTTL).Err()
}

func claimPostEffects(ctx context.Context, messageID int64) (string, error) {
	if redisClient.Rdb == nil || messageID <= 0 {
		return "local", nil
	}
	var token [16]byte
	if _, err := rand.Read(token[:]); err != nil {
		return "", err
	}
	owner := fmt.Sprintf("pending:%x", token)
	result, err := redisClient.Rdb.Eval(ctx, `
local value = redis.call('GET', KEYS[1])
if value == '1' then return 0 end
if value then return -1 end
redis.call('SET', KEYS[1], ARGV[1], 'PX', ARGV[2])
return 1`, []string{fmt.Sprintf("post:effects:%d", messageID)}, owner, postPendingTTL.Milliseconds()).Int()
	if err != nil {
		return "", err
	}
	if result == -1 {
		return "", errors.New("消息副作用正在执行，等待重试")
	}
	if result == 0 {
		return "", nil
	}
	return owner, nil
}

// Only the claim owner may finish or release work; expired claims are retryable.
func finishPostEffects(ctx context.Context, messageID int64, owner string, success bool) error {
	if redisClient.Rdb != nil && messageID > 0 {
		flag := "0"
		if success {
			flag = "1"
		}
		result, err := redisClient.Rdb.Eval(ctx, `
if redis.call('GET', KEYS[1]) ~= ARGV[1] then return 0 end
if ARGV[2] == '1' then
  redis.call('SET', KEYS[1], '1', 'PX', ARGV[3])
else
  redis.call('DEL', KEYS[1])
end
return 1`, []string{fmt.Sprintf("post:effects:%d", messageID)}, owner, flag, postEffectsTTL.Milliseconds()).Int()
		if err != nil {
			return err
		}
		if result == 0 {
			return errors.New("消息副作用归属已过期，等待重试")
		}
	}
	return nil
}

func postIdempotencyKey(senderUserID int64, clientMessageID string) string {
	digest := sha256.Sum256([]byte(clientMessageID))
	return fmt.Sprintf("post:idempotency:%d:%x", senderUserID, digest[:])
}
