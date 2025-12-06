package handler

import (
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"context"
	"fmt"
	"storageService/internal/cache"
	"time"

	"storageService/internal/publisher"

	"google.golang.org/protobuf/proto"
)

// StorageHandler 存储服务处理器
type StorageHandler struct {
	l1Cache cache.Cache
	l2Cache cache.Cache // L2 Redis缓存，可能为nil
}

// NewStorageHandler 创建新的存储处理器
func NewStorageHandler() *StorageHandler {
	// 初始化数据库连接并自动迁移表
	_ = db.DB(&db.User{}, &db.Friend{}, &db.Message{}, &db.FileMetadata{})

	// 初始化L1缓存
	l1Cache := cache.NewL1Cache()

	// 初始化L2缓存（Redis）
	var l2Cache cache.Cache
	l2CacheInstance, err := cache.NewL2Cache()
	if err != nil {
		logger.Sugar().Warnf("L2 Redis缓存初始化失败，将仅使用L1缓存: %v", err)
		l2Cache = nil
	} else {
		logger.Sugar().Info("L2 Redis缓存初始化成功")
		l2Cache = l2CacheInstance
	}

	return &StorageHandler{
		l1Cache: l1Cache,
		l2Cache: l2Cache,
	}
}

// HandleMessage 处理Kafka消息
func (h *StorageHandler) HandleMessage(ctx context.Context, message []byte) error {
	sugar := logger.Sugar()

	// 解析Protobuf请求
	req := &storage.RequestMessage{}
	if err := proto.Unmarshal(message, req); err != nil {
		sugar.Errorf("解析Protobuf请求失败: %v", err)
		return err
	}

	sugar.Debugf("收到存储请求: from_topic=%s, target_user_id=%d",
		req.FromKafkaTopic, req.TargetUserId)

	// 根据请求类型处理
	var resp *storage.ResponseMessage
	var err error

	switch payload := req.Payload.(type) {
	case *storage.RequestMessage_StoreNewMessage:
		resp, err = h.handleStoreNewMessage(req, payload.StoreNewMessage)
	case *storage.RequestMessage_QueryMessage:
		resp, err = h.handleQueryMessage(req, payload.QueryMessage)
	case *storage.RequestMessage_QuerySyncMessages:
		resp, err = h.handleQuerySyncMessages(req, payload.QuerySyncMessages)
	case *storage.RequestMessage_UpdateUserName:
		resp, err = h.handleUpdateUserName(req, payload.UpdateUserName)
	case *storage.RequestMessage_UpdateUserAvatar:
		resp, err = h.handleUpdateUserAvatar(req, payload.UpdateUserAvatar)
	case *storage.RequestMessage_QueryUser:
		resp, err = h.handleQueryUser(req, payload.QueryUser)
	case *storage.RequestMessage_QueryFileExists:
		resp, err = h.handleQueryFileExists(req, payload.QueryFileExists)
	default:
		err = fmt.Errorf("未知的请求类型")
	}

	if err != nil {
		sugar.Errorf("处理请求失败: %v", err)
		// 返回错误响应
		resp = &storage.ResponseMessage{
			Result:       storage.StorageResult_SERVICE_ERROR,
			TargetUserId: req.TargetUserId,
		}
	}

	// 发送响应
	return h.sendResponse(req.FromKafkaTopic, resp)
}

// handleStoreNewMessage 处理存储新消息请求
func (h *StorageHandler) handleStoreNewMessage(req *storage.RequestMessage, msg *storage.StoreNewMessage) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 保存到数据库
	start := time.Now()
	messageID, err := db.StoreNewMessage(msg.FromUserId, msg.ToUserId, msg.Content, msg.MessageType, msg.IsGroup)
	metrics.RecordDatabaseQuery("insert", start)
	if err != nil {
		sugar.Errorf("保存消息到数据库失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	sugar.Debugf("消息保存成功: message_id=%d", messageID)

	// 更新缓存（先清除相关缓存）
	h.clearMessageCache(msg.ToUserId)

	// 构建响应
	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_StoreMsgRsp{
			StoreMsgRsp: &storage.StoreMsgRsp{
				MessageId: messageID,
			},
		},
	}

	return resp, nil
}

// handleQueryMessage 处理查询消息请求
func (h *StorageHandler) handleQueryMessage(req *storage.RequestMessage, query *storage.QueryMessage) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 先尝试从缓存获取
	cacheKey := fmt.Sprintf("message:%d", query.MessageId)
	if cached, ok := h.getFromCache(cacheKey); ok {
		if msg, ok := cached.(*db.Message); ok {
			sugar.Debugf("从缓存获取消息: message_id=%d", query.MessageId)
			return h.buildMessageResponse(req, msg), nil
		}
	}

	// 从数据库查询
	start := time.Now()
	message, err := db.GetMessageByID(query.MessageId)
	metrics.RecordDatabaseQuery("select", start)
	if err != nil {
		sugar.Errorf("查询消息失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}
	if message == nil {
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	// 存入缓存
	h.setToCache(cacheKey, message, 5*time.Minute)

	return h.buildMessageResponse(req, message), nil
}

// handleQuerySyncMessages 处理同步消息请求
func (h *StorageHandler) handleQuerySyncMessages(req *storage.RequestMessage, query *storage.QuerySyncMessages) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 解析时间戳，支持多种格式
	var timestamp time.Time
	var err error

	// 尝试RFC3339格式（带T和时区，如 "2006-01-02T15:04:05Z07:00"）
	timestamp, err = time.Parse(time.RFC3339, query.Timestamp)
	if err != nil {
		// 尝试RFC3339Nano格式
		timestamp, err = time.Parse(time.RFC3339Nano, query.Timestamp)
		if err != nil {
			// 尝试不带冒号的时区格式（如 "2006-01-02T15:04:05+0800"）
			timestamp, err = time.Parse("2006-01-02T15:04:05Z0700", query.Timestamp)
			if err != nil {
				// 尝试空格分隔的格式（如 "2006-01-02 15:04:05+08"）
				timestamp, err = time.Parse("2006-01-02 15:04:05-07", query.Timestamp)
				if err != nil {
					// 最后尝试简单格式（如 "2006-01-02 15:04:05"）
					timestamp, err = time.Parse("2006-01-02 15:04:05", query.Timestamp)
					if err != nil {
						sugar.Warnf("解析时间戳失败，使用默认值: %v, 原始时间戳: %s", err, query.Timestamp)
						timestamp = time.Now().Add(-24 * time.Hour) // 默认查询最近24小时
					}
				}
			}
		}
	}

	// 查询该时间戳之后的消息，使用UTC时间的RFC3339格式（与数据库存储格式一致）
	start := time.Now()
	messages, err := db.GetSyncMessagesByTimestamp(query.ToUserId, timestamp.UTC().Format(time.RFC3339))
	metrics.RecordDatabaseQuery("select", start)
	if err != nil {
		sugar.Errorf("查询同步消息失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	// 限制每次同步数量
	if len(messages) > 100 {
		messages = messages[:100]
	}

	sugar.Debugf("查询到 %d 条同步消息", len(messages))

	// 转换为Protobuf格式
	var msgResponses []*storage.MessageRsp
	for _, msg := range messages {
		msgResponses = append(msgResponses, &storage.MessageRsp{
			FromUserId: msg.FromUserID,
			ToUserId:   msg.ToUserID,
			Content:    msg.Content,
			Timestamp:  msg.Timestamp,
			MsgType:    msg.MessageType,
			IsGroup:    msg.IsGroup,
		})
	}

	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_SyncMsgsRsp{
			SyncMsgsRsp: &storage.SyncMessagesRsp{
				Msgs: msgResponses,
			},
		},
	}

	return resp, nil
}

// handleUpdateUserName 处理更新用户名请求
func (h *StorageHandler) handleUpdateUserName(req *storage.RequestMessage, update *storage.UpdateUserName) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 更新数据库
	start := time.Now()
	err := db.UpdateUserNameByID(update.UserId, update.NewUserName)
	metrics.RecordDatabaseQuery("update", start)
	if err != nil {
		sugar.Errorf("更新用户名失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	// 注意：UpdateUserNameByID 不返回受影响行数
	// 需要检查用户是否存在
	userStart := time.Now()
	user, err := db.GetUserById(update.UserId)
	metrics.RecordDatabaseQuery("select", userStart)
	if err != nil {
		sugar.Errorf("检查用户是否存在失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}
	if user == nil {
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	sugar.Debugf("用户名更新成功: user_id=%d, new_name=%s",
		update.UserId, update.NewUserName)

	// 清除用户信息缓存
	h.clearUserCache(update.UserId)

	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
	}

	return resp, nil
}

// handleUpdateUserAvatar 处理更新用户头像请求
func (h *StorageHandler) handleUpdateUserAvatar(req *storage.RequestMessage, update *storage.UpdateUserAvatar) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 更新数据库
	start := time.Now()
	err := db.UpdateUserAvatarByID(update.UserId, update.NewAvatarUrl)
	metrics.RecordDatabaseQuery("update", start)
	if err != nil {
		sugar.Errorf("更新用户头像失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	// 检查用户是否存在
	userStart := time.Now()
	user, err := db.GetUserById(update.UserId)
	metrics.RecordDatabaseQuery("select", userStart)
	if err != nil {
		sugar.Errorf("检查用户是否存在失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}
	if user == nil {
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	sugar.Debugf("用户头像更新成功: user_id=%d, new_avatar=%s",
		update.UserId, update.NewAvatarUrl)

	// 清除用户信息缓存
	h.clearUserCache(update.UserId)

	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
	}

	return resp, nil
}

// buildMessageResponse 构建消息查询响应
func (h *StorageHandler) buildMessageResponse(req *storage.RequestMessage, msg *db.Message) *storage.ResponseMessage {
	return &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_MsgRsp{
			MsgRsp: &storage.MessageRsp{
				FromUserId: msg.FromUserID,
				ToUserId:   msg.ToUserID,
				Content:    msg.Content,
				Timestamp:  msg.Timestamp,
				MsgType:    msg.MessageType,
				IsGroup:    msg.IsGroup,
			},
		},
	}
}

// getFromCache 从缓存获取数据（先L1后L2）
func (h *StorageHandler) getFromCache(key string) (interface{}, bool) {
	start := time.Now()

	// 先查L1缓存
	if val, ok := h.l1Cache.Get(key); ok {
		metrics.RecordCacheOperation("get", "l1", start)
		metrics.RecordCacheHit("l1")
		return val, true
	}
	metrics.RecordCacheOperation("get", "l1", start)
	metrics.RecordCacheMiss("l1")

	// 再查L2缓存（如果已初始化）
	if h.l2Cache != nil {
		l2Start := time.Now()
		if val, ok := h.l2Cache.Get(key); ok {
			metrics.RecordCacheOperation("get", "l2", l2Start)
			metrics.RecordCacheHit("l2")
			// 回填到L1缓存
			h.l1Cache.Set(key, val, 5*time.Minute)
			return val, true
		}
		metrics.RecordCacheOperation("get", "l2", l2Start)
		metrics.RecordCacheMiss("l2")
	}

	return nil, false
}

// setToCache 设置缓存（同时设置L1和L2）
func (h *StorageHandler) setToCache(key string, value interface{}, ttl time.Duration) {
	// 设置L1缓存
	l1Start := time.Now()
	h.l1Cache.Set(key, value, ttl)
	metrics.RecordCacheOperation("set", "l1", l1Start)

	// 设置L2缓存（如果已初始化）
	if h.l2Cache != nil {
		l2Start := time.Now()
		h.l2Cache.Set(key, value, ttl)
		metrics.RecordCacheOperation("set", "l2", l2Start)
	}
}

// clearMessageCache 清除消息相关缓存
func (h *StorageHandler) clearMessageCache(userID int64) {
	// 清除用户消息列表缓存
	cacheKey := fmt.Sprintf("user_messages:%d", userID)
	h.l1Cache.Del(cacheKey)
	if h.l2Cache != nil {
		h.l2Cache.Del(cacheKey)
	}
}

// clearUserCache 清除用户信息缓存
func (h *StorageHandler) clearUserCache(userID int64) {
	cacheKey := fmt.Sprintf("user:%d", userID)
	h.l1Cache.Del(cacheKey)
	if h.l2Cache != nil {
		h.l2Cache.Del(cacheKey)
	}
}

// handleQueryUser 处理查询用户信息请求
func (h *StorageHandler) handleQueryUser(req *storage.RequestMessage, query *storage.QueryUser) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 先尝试从缓存获取
	cacheKey := fmt.Sprintf("user:%d", query.UserId)
	if cached, ok := h.getFromCache(cacheKey); ok {
		if user, ok := cached.(*db.User); ok {
			sugar.Debugf("从缓存获取用户信息: user_id=%d", query.UserId)
			return h.buildUserInfoResponse(req, user), nil
		}
	}

	// 从数据库查询
	start := time.Now()
	user, err := db.GetUserById(query.UserId)
	metrics.RecordDatabaseQuery("select", start)
	if err != nil {
		sugar.Errorf("查询用户失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}
	if user == nil {
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_RECORD_NOT_EXIST,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	// 存入缓存
	h.setToCache(cacheKey, user, 10*time.Minute) // 用户信息缓存10分钟

	return h.buildUserInfoResponse(req, user), nil
}

// buildUserInfoResponse 构建用户信息查询响应
func (h *StorageHandler) buildUserInfoResponse(req *storage.RequestMessage, user *db.User) *storage.ResponseMessage {
	return &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_UserInfoRsp{
			UserInfoRsp: &storage.UserInfoRsp{
				UserId:     user.ID,
				Account:    user.Account,
				Name:       user.Name,
				Avatar:     user.Avatar,
				UpdateTime: user.UpdateTime,
			},
		},
	}
}

// handleQueryFileExists 处理查询文件是否存在请求
func (h *StorageHandler) handleQueryFileExists(req *storage.RequestMessage, query *storage.QueryFileExists) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	fileHash := query.FileHash
	if fileHash == "" {
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_SERVICE_ERROR,
			TargetUserId: req.TargetUserId,
		}, fmt.Errorf("file_hash is required")
	}

	// 先尝试从缓存获取
	cacheKey := fmt.Sprintf("file_exists:%s", fileHash)
	if cached, ok := h.getFromCache(cacheKey); ok {
		if exists, ok := cached.(bool); ok {
			sugar.Debugf("从缓存获取文件存在性: file_hash=%s, exists=%v", fileHash, exists)
			return h.buildFileExistsResponse(req, exists, 0, ""), nil
		}
	}

	// 从数据库查询
	start := time.Now()
	fileMetadata, err := db.GetFileMetadata(fileHash)
	metrics.RecordDatabaseQuery("select", start)
	if err != nil {
		sugar.Errorf("查询文件元数据失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	exists := fileMetadata != nil
	var fileSize int64
	var storagePath string
	if exists {
		fileSize = fileMetadata.FileSize
		storagePath = fileMetadata.StoragePath
	}

	// 存入缓存
	h.setToCache(cacheKey, exists, 5*time.Minute)

	return h.buildFileExistsResponse(req, exists, fileSize, storagePath), nil
}

// buildFileExistsResponse 构建文件存在性查询响应
func (h *StorageHandler) buildFileExistsResponse(req *storage.RequestMessage, exists bool, fileSize int64, storagePath string) *storage.ResponseMessage {
	return &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_FileExistsRsp{
			FileExistsRsp: &storage.FileExistsRsp{
				Exists:      exists,
				FileSize:    fileSize,
				StoragePath: storagePath,
			},
		},
	}
}

// sendResponse 发送响应到Kafka
func (h *StorageHandler) sendResponse(topic string, resp *storage.ResponseMessage) error {
	sugar := logger.Sugar()

	// 序列化响应
	respData, err := proto.Marshal(resp)
	if err != nil {
		sugar.Errorf("序列化响应失败: %v", err)
		return err
	}

	// 创建Envelope封装
	env := &envelope.Envelope{
		Type:    envelope.MessageType_STORAGE_RESPONSE,
		Payload: respData,
	}
	envData, err := proto.Marshal(env)
	if err != nil {
		sugar.Errorf("序列化Envelope失败: %v", err)
		return err
	}

	// 发送响应到Kafka
	err = publisher.PublishMessage(string(envData), topic)
	if err != nil {
		sugar.Errorf("发送响应到Kafka失败: %v", err)
		metrics.RecordKafkaProcessingError()
		return err
	}
	metrics.RecordKafkaMessageProduced(topic)

	sugar.Debugf("响应发送成功到topic: %s, 数据长度: %d, Envelope类型: %v", topic, len(envData), envelope.MessageType_STORAGE_RESPONSE)
	return nil
}
