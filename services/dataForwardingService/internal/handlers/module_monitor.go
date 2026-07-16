package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	pb "Betterfly2/proto/data_forwarding"
	sharedDB "Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"data_forwarding_service/internal/monitor"
	redisClient "data_forwarding_service/internal/redis"
	"github.com/redis/go-redis/v9"
	"google.golang.org/protobuf/proto"
)

const monitorContactKey = "monitor:contact:1"

var lastMonitorMessageID atomic.Int64

var monitorExecutor = monitor.NewExecutor(monitor.Actions{
	Status:      monitorServiceStatus,
	Connections: monitorConnectionStatus,
	Route:       monitorUserRoute,
	Kick:        monitorKickUser,
	User:        monitorUserSummary,
	Group:       monitorGroupSummary,
	Requests:    monitorRelationshipRequests,
})

func handleMonitorQueryUser(requesterUserID int64) error {
	if requesterUserID != monitor.AdminUserID {
		return sendMonitorWarning(requesterUserID, "目标用户不存在")
	}
	profile := monitor.CurrentProfile()
	return sendMonitorResponse(requesterUserID, &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_UserInfo{UserInfo: &pb.UserInfo{
			SendToUserId:  requesterUserID,
			QueryUserName: profile.Name,
			UserId:        profile.UserID,
			Account:       profile.Account,
			Name:          profile.Name,
			Avatar:        profile.Avatar,
			UpdateTime:    profile.UpdateTime,
		}},
	})
}

func handleMonitorAddContact(requesterUserID int64) error {
	if requesterUserID != monitor.AdminUserID {
		return sendMonitorWarning(requesterUserID, "目标用户不存在")
	}
	if err := setMonitorContact(context.Background(), true); err != nil {
		return sendMonitorWarning(requesterUserID, "Monitor 联系人暂时不可用")
	}
	logger.Sugar().Infof("Monitor审计: actor_user_id=%d action=add_contact", requesterUserID)
	return sendMonitorResponse(requesterUserID, newMonitorFriendAcceptance(requesterUserID, time.Now()))
}

func newMonitorFriendAcceptance(requesterUserID int64, now time.Time) *pb.ResponseMessage {
	profile := monitor.CurrentProfile()
	return &pb.ResponseMessage{Payload: &pb.ResponseMessage_RelationshipOperationRsp{
		RelationshipOperationRsp: &pb.RelationshipOperationRsp{
			Operation: "create_friend_request", Result: "FRIEND_OK",
			Request: &pb.RelationshipRequestInfo{
				RequestType: "friend", RequesterUserId: requesterUserID, TargetUserId: profile.UserID,
				TargetName: profile.Name, TargetAvatar: profile.Avatar, Status: "accepted",
				CreatedAt: now.UTC().Format(time.RFC3339Nano), ResolvedBy: profile.UserID,
			},
		},
	}}
}

func handleMonitorDeleteContact(requesterUserID int64) error {
	if requesterUserID != monitor.AdminUserID {
		return sendMonitorWarning(requesterUserID, "目标用户不存在")
	}
	if err := setMonitorContact(context.Background(), false); err != nil {
		return sendMonitorWarning(requesterUserID, "Monitor 联系人暂时不可用")
	}
	logger.Sugar().Infof("Monitor审计: actor_user_id=%d action=remove_contact", requesterUserID)
	return sendMonitorServerMessage(requesterUserID, "删除好友成功")
}

func handleMonitorPost(senderUserID int64, post *pb.Post) error {
	if senderUserID != monitor.AdminUserID {
		logger.Sugar().Warnf("Monitor越权访问被拒绝: actor_user_id=%d", senderUserID)
		return sendMonitorWarning(senderUserID, "目标用户不存在")
	}
	if post.GetIsGroup() || !monitorContactEnabled(context.Background()) {
		return sendMonitorWarning(senderUserID, "目标用户不存在")
	}
	if strings.ToLower(strings.TrimSpace(post.GetMsgType())) != "text" {
		return sendMonitorWarning(senderUserID, "Monitor 仅接受 text 类型指令")
	}
	clientMessageID := ensurePostClientMessageID(post)
	cached, acquired, err := claimMonitorCommand(context.Background(), senderUserID, clientMessageID)
	if err != nil {
		return err
	}
	if !acquired {
		if cached == nil {
			logger.Sugar().Debugf("Monitor指令正在处理中，忽略重复请求: actor_user_id=%d client_message_id=%s", senderUserID, clientMessageID)
			return nil
		}
		return sendMonitorPostResult(senderUserID, clientMessageID, cached.MessageID, cached.Response)
	}

	commandName, response, err := monitorExecutor.Execute(context.Background(), senderUserID, post.GetMsg())
	if err != nil {
		if errors.Is(err, monitor.ErrForbidden) {
			return sendMonitorWarning(senderUserID, "目标用户不存在")
		}
		logger.Sugar().Errorf("Monitor指令执行失败: actor_user_id=%d command=%s error=%v", senderUserID, commandName, err)
		response = "指令执行失败：" + err.Error()
	}
	logger.Sugar().Infof("Monitor审计: actor_user_id=%d command=%s", senderUserID, commandName)

	messageID := nextMonitorMessageID(time.Now())
	result := &monitorCommandResult{MessageID: messageID, Response: response}
	if err := cacheMonitorCommand(context.Background(), senderUserID, clientMessageID, result); err != nil {
		logger.Sugar().Errorf("缓存Monitor指令结果失败: actor_user_id=%d client_message_id=%s err=%v", senderUserID, clientMessageID, err)
	}
	return sendMonitorPostResult(senderUserID, clientMessageID, messageID, response)
}

type monitorCommandResult struct {
	MessageID int64  `json:"message_id"`
	Response  string `json:"response"`
}

func sendMonitorPostResult(userID int64, clientMessageID string, messageID int64, response string) error {
	if err := sendMonitorResponse(userID, newMonitorPostAck(clientMessageID, messageID)); err != nil {
		return err
	}
	profile := monitor.CurrentProfile()
	return sendMonitorResponse(userID, &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Post{Post: &pb.Post{
			FromId:          profile.UserID,
			ToId:            userID,
			Msg:             response,
			MsgType:         "text",
			Timestamp:       time.Now().UTC().Format(time.RFC3339Nano),
			ClientMessageId: clientMessageID,
		}},
	})
}

func newMonitorPostAck(clientMessageID string, messageID int64) *pb.ResponseMessage {
	return &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_PostAckRsp{
			PostAckRsp: &pb.PostAckRsp{MessageId: messageID, ClientMessageId: clientMessageID},
		},
	}
}

func claimMonitorCommand(ctx context.Context, userID int64, clientMessageID string) (*monitorCommandResult, bool, error) {
	if redisClient.Rdb == nil {
		return nil, true, nil
	}
	key := "monitor:" + postIdempotencyKey(userID, clientMessageID)
	value, err := redisClient.Rdb.Get(ctx, key).Result()
	if err == nil && value != "pending" {
		var cached monitorCommandResult
		if json.Unmarshal([]byte(value), &cached) == nil && cached.MessageID > 0 {
			return &cached, false, nil
		}
	}
	if err != nil && !errors.Is(err, redis.Nil) {
		return nil, false, err
	}
	acquired, err := redisClient.Rdb.SetNX(ctx, key, "pending", postPendingTTL).Result()
	return nil, acquired, err
}

func cacheMonitorCommand(ctx context.Context, userID int64, clientMessageID string, result *monitorCommandResult) error {
	if redisClient.Rdb == nil {
		return nil
	}
	payload, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return redisClient.Rdb.Set(ctx, "monitor:"+postIdempotencyKey(userID, clientMessageID), payload, postCompletedTTL).Err()
}

func nextMonitorMessageID(now time.Time) int64 {
	candidate := now.UnixNano()
	if candidate < 1 {
		candidate = 1
	}
	for {
		previous := lastMonitorMessageID.Load()
		if candidate <= previous {
			candidate = previous + 1
		}
		if lastMonitorMessageID.CompareAndSwap(previous, candidate) {
			return candidate
		}
	}
}

func DecorateMonitorContacts(targetUserID int64, contacts []*pb.ContactInfo) []*pb.ContactInfo {
	return decorateMonitorContacts(targetUserID, monitorContactEnabled(context.Background()), contacts)
}

func decorateMonitorContacts(targetUserID int64, enabled bool, contacts []*pb.ContactInfo) []*pb.ContactInfo {
	profile := monitor.CurrentProfile()
	filtered := make([]*pb.ContactInfo, 0, len(contacts)+1)
	for _, contact := range contacts {
		if contact.GetUserId() != profile.UserID {
			filtered = append(filtered, contact)
		}
	}
	if targetUserID != monitor.AdminUserID || !enabled {
		return filtered
	}
	return append(filtered, &pb.ContactInfo{
		UserId:     profile.UserID,
		Account:    profile.Account,
		Name:       profile.Name,
		Avatar:     profile.Avatar,
		Alias:      profile.Name,
		IsNotify:   false,
		UpdateTime: profile.UpdateTime,
	})
}

func setMonitorContact(ctx context.Context, enabled bool) error {
	if redisClient.Rdb == nil {
		return errors.New("redis not initialized")
	}
	if !enabled {
		return redisClient.Rdb.Del(ctx, monitorContactKey).Err()
	}
	return redisClient.Rdb.Set(ctx, monitorContactKey, "1", 0).Err()
}

func monitorContactEnabled(ctx context.Context) bool {
	if redisClient.Rdb == nil {
		return false
	}
	exists, err := redisClient.Rdb.Exists(ctx, monitorContactKey).Result()
	return err == nil && exists == 1
}

func sendMonitorWarning(userID int64, message string) error {
	return sendMonitorResponse(userID, &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Warn{Warn: &pb.Warn{WarningMessage: message}},
	})
}

func sendMonitorServerMessage(userID int64, message string) error {
	return sendMonitorResponse(userID, &pb.ResponseMessage{
		Payload: &pb.ResponseMessage_Server{Server: &pb.Server{ServerMsg: message}},
	})
}

func sendMonitorResponse(userID int64, response *pb.ResponseMessage) error {
	handler := GetWebSocketHandler()
	if handler == nil {
		return errors.New("WebSocket处理器未初始化")
	}
	payload, err := proto.Marshal(response)
	if err != nil {
		return fmt.Errorf("序列化Monitor响应失败: %w", err)
	}
	if err := handler.SendMessage(strconv.FormatInt(userID, 10), payload); err != nil {
		return fmt.Errorf("发送Monitor响应失败: %w", err)
	}
	return nil
}

type monitorProbe struct {
	name string
	kind string
	addr string
}

func monitorServiceStatus(ctx context.Context) (string, error) {
	timeoutCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()

	probes := []monitorProbe{
		{name: "Auth", kind: "tcp", addr: monitorEnv("MONITOR_AUTH_ADDR", "auth_service:50051")},
		{name: "Friend", kind: "http", addr: monitorEnv("MONITOR_FRIEND_READY_URL", "http://friend_service:54401/ready")},
		{name: "Storage", kind: "http", addr: monitorEnv("MONITOR_STORAGE_READY_URL", "http://storage_service:8081/ready")},
		{name: "ABTest", kind: "http", addr: monitorEnv("MONITOR_ABTEST_READY_URL", "http://abtest_service:8082/ready")},
		{name: "Call", kind: "http", addr: monitorEnv("MONITOR_CALL_READY_URL", "http://call_service:8085/ready")},
		{name: "Push", kind: "http", addr: monitorEnv("MONITOR_PUSH_READY_URL", "http://push_service:8086/ready")},
	}
	results := make([]string, len(probes))
	var wg sync.WaitGroup
	for i, probe := range probes {
		wg.Add(1)
		go func(index int, target monitorProbe) {
			defer wg.Done()
			started := time.Now()
			err := runMonitorProbe(timeoutCtx, target)
			results[index] = monitorStatusLine(target.name, time.Since(started), err)
		}(i, probe)
	}
	wg.Wait()

	connections, users := 0, 0
	if handler := GetWebSocketHandler(); handler != nil {
		connections, users = handler.GetConnectionStats()
	}
	lines := []string{fmt.Sprintf("Betterfly 服务状态（DF Pod: %s）", currentContainerTopic())}
	lines = append(lines, fmt.Sprintf("✓ DataForwarding - local connections=%d users=%d", connections, users))
	if redisClient.Rdb == nil {
		lines = append(lines, "✗ Redis - client not initialized")
	} else {
		started := time.Now()
		err := redisClient.Rdb.Ping(timeoutCtx).Err()
		lines = append(lines, monitorStatusLine("Redis", time.Since(started), err))
	}
	for _, broker := range strings.Split(monitorEnv("KAFKA_BROKER", "localhost:9092"), ",") {
		broker = strings.TrimSpace(broker)
		if broker == "" {
			continue
		}
		started := time.Now()
		err := probeTCP(timeoutCtx, broker)
		lines = append(lines, monitorStatusLine("Kafka "+broker, time.Since(started), err))
	}
	lines = append(lines, results...)
	return strings.Join(lines, "\n"), nil
}

func monitorConnectionStatus(ctx context.Context) (string, error) {
	localConnections, localUsers := 0, 0
	if handler := GetWebSocketHandler(); handler != nil {
		localConnections, localUsers = handler.GetConnectionStats()
	}
	if redisClient.Rdb == nil {
		return "", errors.New("Redis未初始化")
	}
	globalUsers, err := redisClient.Rdb.HLen(ctx, "ws_connection_mapping").Result()
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("连接统计\n当前 Pod: connections=%d logged_in=%d\n全局路由记录: %d", localConnections, localUsers, globalUsers), nil
}

func monitorUserRoute(ctx context.Context, userID int64) (string, error) {
	if redisClient.Rdb == nil {
		return "", errors.New("Redis未初始化")
	}
	container, err := redisClient.Rdb.HGet(ctx, "ws_connection_mapping", strconv.FormatInt(userID, 10)).Result()
	if errors.Is(err, redis.Nil) {
		return fmt.Sprintf("用户 %d 当前离线。", userID), nil
	}
	if err != nil {
		return "", err
	}
	leaseExists, err := redisClient.Rdb.Exists(ctx, "ws_route_lease:"+strconv.FormatInt(userID, 10)).Result()
	if err != nil {
		return "", err
	}
	if leaseExists == 0 {
		return fmt.Sprintf("用户 %d 存在过期路由记录：%s（租约已失效）", userID, container), nil
	}
	return fmt.Sprintf("用户 %d 当前路由到 DF Pod：%s", userID, container), nil
}

func monitorKickUser(ctx context.Context, userID int64) (string, error) {
	if redisClient.Rdb == nil {
		return "", errors.New("Redis未初始化")
	}
	container, err := redisClient.Rdb.HGet(ctx, "ws_connection_mapping", strconv.FormatInt(userID, 10)).Result()
	if errors.Is(err, redis.Nil) {
		return fmt.Sprintf("用户 %d 当前离线，无需踢出。", userID), nil
	}
	if err != nil {
		return "", err
	}
	leaseExists, err := redisClient.Rdb.Exists(ctx, "ws_route_lease:"+strconv.FormatInt(userID, 10)).Result()
	if err != nil {
		return "", err
	}
	if leaseExists == 0 {
		return fmt.Sprintf("用户 %d 的路由租约已失效，未执行踢出。", userID), nil
	}
	dsm := &redisClient.DistributedSessionManager{}
	if err := dsm.PublishKickNotification(ctx, strconv.FormatInt(userID, 10), container); err != nil {
		return "", err
	}
	return fmt.Sprintf("已向 DF Pod %s 发送用户 %d 的强制断线指令。", container, userID), nil
}

func monitorUserSummary(_ context.Context, userID int64) (string, error) {
	user, err := sharedDB.GetUserById(userID)
	if err != nil {
		return "", err
	}
	if user == nil {
		return fmt.Sprintf("用户 %d 不存在。", userID), nil
	}
	friends, err := sharedDB.GetFriendList(userID)
	if err != nil {
		return "", err
	}
	groups, err := sharedDB.GetJoinedGroups(userID)
	if err != nil {
		return "", err
	}
	lines := []string{
		fmt.Sprintf("用户 %d", user.ID),
		fmt.Sprintf("账号: %s", user.Account),
		fmt.Sprintf("昵称: %s", user.Name),
		fmt.Sprintf("好友: %d", len(friends)),
		fmt.Sprintf("群聊: %d", len(groups)),
	}
	for _, group := range groups {
		role := sharedDB.GroupRoleMember
		if memberRole, _, roleErr := sharedDB.RequireGroupManager(group.GroupID, userID); roleErr == nil && memberRole != "" {
			role = memberRole
		}
		lines = append(lines, fmt.Sprintf("- %d %s (%s)", group.GroupID, group.GroupName, role))
	}
	return strings.Join(lines, "\n"), nil
}

func monitorGroupSummary(_ context.Context, groupID int64) (string, error) {
	group, err := sharedDB.GetGroupByID(groupID)
	if err != nil {
		return "", err
	}
	if group == nil {
		return fmt.Sprintf("群组 %d 不存在。", groupID), nil
	}
	members, err := sharedDB.GetGroupMembers(groupID)
	if err != nil {
		return "", err
	}
	roles := map[string]int{}
	for _, member := range members {
		roles[member.Role]++
	}
	return fmt.Sprintf("群组 %d\n名称: %s\n群主: %d\n成员: %d (owner=%d admin=%d member=%d)",
		group.GroupID, group.Name, group.OwnerUserID, len(members), roles[sharedDB.GroupRoleOwner], roles[sharedDB.GroupRoleAdmin], roles[sharedDB.GroupRoleMember]), nil
}

func monitorRelationshipRequests(_ context.Context, userID int64) (string, error) {
	friendRequests, err := sharedDB.ListFriendRequests(userID, true)
	if err != nil {
		return "", err
	}
	groupRequests, err := sharedDB.ListGroupInvitations(userID, true)
	if err != nil {
		return "", err
	}
	pendingFriends, pendingGroups := 0, 0
	for _, request := range friendRequests {
		if request.Status == sharedDB.RequestStatusPending {
			pendingFriends++
		}
	}
	for _, request := range groupRequests {
		if request.Status == sharedDB.RequestStatusPending {
			pendingGroups++
		}
	}
	return fmt.Sprintf("用户 %d 待处理申请\n好友申请: %d\n群邀请/入群申请: %d", userID, pendingFriends, pendingGroups), nil
}

func runMonitorProbe(ctx context.Context, probe monitorProbe) error {
	switch probe.kind {
	case "tcp":
		return probeTCP(ctx, probe.addr)
	case "http":
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, probe.addr, nil)
		if err != nil {
			return err
		}
		response, err := (&http.Client{Timeout: 2500 * time.Millisecond}).Do(request)
		if err != nil {
			return err
		}
		defer response.Body.Close()
		if response.StatusCode < 200 || response.StatusCode >= 300 {
			return fmt.Errorf("HTTP %d", response.StatusCode)
		}
		return nil
	default:
		return errors.New("unknown probe type")
	}
}

func probeTCP(ctx context.Context, addr string) error {
	connection, err := (&net.Dialer{Timeout: 2500 * time.Millisecond}).DialContext(ctx, "tcp", addr)
	if err != nil {
		return err
	}
	return connection.Close()
}

func monitorStatusLine(name string, duration time.Duration, err error) string {
	if err != nil {
		return fmt.Sprintf("✗ %s - unavailable (%s)", name, compactMonitorError(err))
	}
	return fmt.Sprintf("✓ %s - ready (%dms)", name, duration.Milliseconds())
}

func compactMonitorError(err error) string {
	message := strings.ReplaceAll(err.Error(), "\n", " ")
	if len(message) > 100 {
		return message[:100] + "..."
	}
	return message
}

func monitorEnv(key, fallback string) string {
	if value := strings.TrimSpace(os.Getenv(key)); value != "" {
		return value
	}
	return fallback
}
