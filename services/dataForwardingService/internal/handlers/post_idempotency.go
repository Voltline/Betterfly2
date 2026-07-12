package handlers

import (
	"context"
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

func claimPostEffects(ctx context.Context, messageID int64) (bool, error) {
	if redisClient.Rdb == nil || messageID <= 0 {
		return true, nil
	}
	return redisClient.Rdb.SetNX(ctx, fmt.Sprintf("post:effects:%d", messageID), "1", postEffectsTTL).Result()
}

func releasePostEffects(ctx context.Context, messageID int64) {
	if redisClient.Rdb != nil && messageID > 0 {
		_ = redisClient.Rdb.Del(ctx, fmt.Sprintf("post:effects:%d", messageID)).Err()
	}
}

func postIdempotencyKey(senderUserID int64, clientMessageID string) string {
	digest := sha256.Sum256([]byte(clientMessageID))
	return fmt.Sprintf("post:idempotency:%d:%x", senderUserID, digest[:])
}

func sendPostAck(userID, messageID int64, clientMessageID string) error {
	return sendMonitorResponse(userID, &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_PostAckRsp{PostAckRsp: &pb.PostAckRsp{
			MessageId:       messageID,
			ClientMessageId: clientMessageID,
		}},
	})
}
