package handlers

import (
	pushpb "Betterfly2/proto/push"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"os"
	"strconv"
	"strings"
	"time"
)

// pushTokenRevocationEnv 打开后，改密码或撤销全部会话成功时让push删除该用户此前注册的推送token。
// 默认关闭：须等客户端在每次认证成功后都重新注册token的版本铺开，再部署支持该请求的push，最后打开。
const pushTokenRevocationEnv = "PUSH_REVOKE_TOKENS_ON_SESSION_REVOKE"

var publishPushTokenRevocation = publishPushRequest

func pushTokenRevocationEnabled() bool {
	enabled, _ := strconv.ParseBool(strings.TrimSpace(os.Getenv(pushTokenRevocationEnv)))
	return enabled
}

// revokePushTokens 在凭据已轮换后调用；截止时间取在给客户端回包之前，当前设备随后的重新注册不会被删。
// 发布失败只记日志，不影响已经成功的改密码或撤销会话。
func revokePushTokens(userID int64, reason string, revokedAt time.Time) {
	if userID <= 0 || !pushTokenRevocationEnabled() {
		return
	}
	request := &pushpb.RequestMessage{Payload: &pushpb.RequestMessage_RevokeUserTokens{RevokeUserTokens: &pushpb.RevokeUserTokens{
		UserId: userID, RevokedBefore: sharedDB.FormatReliabilityTime(revokedAt), Reason: reason,
	}}}
	if err := publishPushTokenRevocation(request); err != nil {
		logger.Sugar().Warnw("发布推送token撤销请求失败", "user_id", userID, "reason", reason, "error", err)
	}
}
