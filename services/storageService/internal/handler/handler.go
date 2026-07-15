package handler

import (
	envelope "Betterfly2/proto/envelope"
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/dispatch"
	"Betterfly2/shared/kafkaconsumer"
	"Betterfly2/shared/logger"
	"Betterfly2/shared/metrics"
	"Betterfly2/shared/mq"
	"context"
	"errors"
	"fmt"
	"storageService/internal/cache"
	"sync"
	"time"

	"google.golang.org/protobuf/proto"
	"gorm.io/gorm"
)

// Pre-defined time formats for efficient parsing (ordered by expected frequency)
var timeFormats = []string{
	time.RFC3339,
	time.RFC3339Nano,
	"2006-01-02T15:04:05Z0700",
	"2006-01-02 15:04:05-07",
	"2006-01-02 15:04:05",
}

type storageRequestContext struct {
	handler   *StorageHandler
	request   *storage.RequestMessage
	database  *gorm.DB
	cacheKeys *[]string
}

type storageRequestModule func(*dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage])

var (
	storageRequestModules    []storageRequestModule
	storageRequestRouter     *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage]
	storageRequestRouterOnce sync.Once
)

func registerStorageRequestModule(register storageRequestModule) {
	storageRequestModules = append(storageRequestModules, register)
}

func getStorageRequestRouter() *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage] {
	storageRequestRouterOnce.Do(func() {
		storageRequestRouter = newStorageRequestRouter()
	})
	return storageRequestRouter
}

func newStorageRequestRouter() *dispatch.OneofRouter[storageRequestContext, *storage.ResponseMessage] {
	router := dispatch.NewOneofRouter[storageRequestContext, *storage.ResponseMessage]()
	for _, register := range storageRequestModules {
		register(router)
	}
	return router
}

// StorageHandler 存储服务处理器
type StorageHandler struct {
	l1Cache  cache.Cache
	l2Cache  cache.Cache // L2 Redis缓存，可能为nil
	database *gorm.DB
}

type fileExistsCacheEntry struct {
	Exists      bool
	FileSize    int64
	StoragePath string
}

// NewStorageHandler 创建新的存储处理器
func NewStorageHandler() *StorageHandler {
	_ = db.DB()

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
		l1Cache:  l1Cache,
		l2Cache:  l2Cache,
		database: db.DB(),
	}
}

func (h *StorageHandler) requestDatabase() *gorm.DB {
	if h.database != nil {
		return h.database
	}
	return db.DB()
}

// HandleMessage 处理Kafka消息
func (h *StorageHandler) HandleMessage(ctx context.Context, message []byte) error {
	sugar := logger.Sugar()
	operationKey, hasOperationKey := kafkaconsumer.OperationKeyFromContext(ctx)
	if !hasOperationKey {
		return errors.New("storage consumer operation key is required")
	}

	// 解析Protobuf请求
	req := &storage.RequestMessage{}
	if err := proto.Unmarshal(message, req); err != nil {
		sugar.Errorf("解析Protobuf请求失败: %v", err)
		return err
	}

	sugar.Debugf("收到存储请求: from_topic=%s, target_user_id=%d",
		req.FromKafkaTopic, req.TargetUserId)

	cacheKeys := make([]string, 0, 1)
	execution, err := db.ExecuteInboxOutbox(ctx, h.requestDatabase(), "storage", operationKey, func(tx *gorm.DB) ([]byte, []db.PendingOutboxEvent, error) {
		resp, dispatchErr := getStorageRequestRouter().Dispatch(storageRequestContext{
			handler: h, request: req, database: tx, cacheKeys: &cacheKeys,
		}, req.Payload)
		if dispatchErr != nil {
			sugar.Errorw("处理存储请求暂时失败", "operation_key", operationKey, "error", dispatchErr)
			return nil, nil, dispatchErr
		}
		if resp == nil {
			return nil, nil, errors.New("storage dispatch returned nil response")
		}
		encoded, marshalErr := proto.Marshal(resp)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		envelopePayload, marshalErr := mq.MarshalEnvelope(envelope.MessageType_STORAGE_RESPONSE, resp)
		if marshalErr != nil {
			return nil, nil, marshalErr
		}
		return encoded, []db.PendingOutboxEvent{{
			EventID: db.StableEventID("storage", operationKey, "response"),
			Topic:   req.GetFromKafkaTopic(), Payload: envelopePayload,
		}}, nil
	})
	if err == nil {
		if execution.Replayed && len(cacheKeys) == 0 {
			cacheKeys = mutationCacheKeys(req)
		}
		h.clearCacheKeys(cacheKeys)
	}
	return err
}

func mutationCacheKeys(req *storage.RequestMessage) []string {
	switch payload := req.GetPayload().(type) {
	case *storage.RequestMessage_StoreNewMessage:
		return []string{fmt.Sprintf("user_messages:%d", payload.StoreNewMessage.GetToUserId())}
	case *storage.RequestMessage_UpdateUserName:
		return []string{fmt.Sprintf("user:%d", payload.UpdateUserName.GetUserId())}
	case *storage.RequestMessage_UpdateUserAvatar:
		return []string{fmt.Sprintf("user:%d", payload.UpdateUserAvatar.GetUserId())}
	default:
		return nil
	}
}

// handleStoreNewMessage 处理存储新消息请求
func (h *StorageHandler) handleStoreNewMessageWithDB(database *gorm.DB, req *storage.RequestMessage, msg *storage.StoreNewMessage, cacheKeys *[]string) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 保存到数据库
	start := time.Now()
	storedMessage, created, err := db.StoreNewMessageWithDB(database,
		msg.FromUserId,
		msg.ToUserId,
		msg.Content,
		msg.MessageType,
		msg.GetRealFileName(),
		msg.IsGroup,
		msg.GetClientMessageId(),
	)
	metrics.RecordDatabaseQuery("insert", start)
	if err != nil {
		sugar.Errorf("保存消息到数据库失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	sugar.Debugf("消息保存成功: message_id=%d client_message_id=%s created=%t", storedMessage.MessageID, msg.GetClientMessageId(), created)

	cacheKey := fmt.Sprintf("user_messages:%d", msg.ToUserId)
	if cacheKeys != nil {
		*cacheKeys = append(*cacheKeys, cacheKey)
	} else {
		h.clearCacheKeys([]string{cacheKey})
	}

	// 构建响应
	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_StoreMsgRsp{
			StoreMsgRsp: &storage.StoreMsgRsp{
				MessageId:       storedMessage.MessageID,
				ClientMessageId: msg.GetClientMessageId(),
				Created:         created,
				FromUserId:      msg.GetFromUserId(),
				ToUserId:        msg.GetToUserId(),
				Content:         msg.GetContent(),
				MessageType:     msg.GetMessageType(),
				IsGroup:         msg.GetIsGroup(),
				RealFileName:    msg.GetRealFileName(),
				ClientTimestamp: msg.GetClientTimestamp(),
			},
		},
	}

	return resp, nil
}

// handleQueryMessage 处理查询消息请求
func (h *StorageHandler) handleQueryMessageWithDB(database *gorm.DB, req *storage.RequestMessage, query *storage.QueryMessage) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 先尝试从缓存获取
	cacheKey := fmt.Sprintf("message:%d", query.MessageId)
	if cached, ok := h.getFromCache(cacheKey); ok {
		if msg, ok := cached.(*db.Message); ok {
			sugar.Debugf("从缓存获取消息: message_id=%d", query.MessageId)
			return h.authorizedMessageResponseWithDB(database, req, msg)
		}
	}

	// 从数据库查询
	if database == nil {
		database = h.requestDatabase()
	}
	start := time.Now()
	message, err := db.GetMessageByIDWithDB(database, query.MessageId)
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

	return h.authorizedMessageResponseWithDB(database, req, message)
}

func (h *StorageHandler) authorizedMessageResponseWithDB(database *gorm.DB, req *storage.RequestMessage, message *db.Message) (*storage.ResponseMessage, error) {
	if database == nil && message != nil && message.IsGroup && req.GetTargetUserId() != message.FromUserID {
		database = h.requestDatabase()
	}
	allowed, err := db.CanUserReadMessageWithDB(database, req.GetTargetUserId(), message)
	if err != nil {
		return nil, err
	}
	if !allowed {
		logger.Sugar().Warnf(
			"安全拒绝按ID读取消息: requester_user_id=%d message_id=%d",
			req.GetTargetUserId(),
			message.MessageID,
		)
		return messageNotFoundResponse(req), nil
	}
	return h.buildMessageResponse(req, message), nil
}

func messageNotFoundResponse(req *storage.RequestMessage) *storage.ResponseMessage {
	return &storage.ResponseMessage{
		Result:       storage.StorageResult_RECORD_NOT_EXIST,
		TargetUserId: req.GetTargetUserId(),
	}
}

// handleQuerySyncMessages 处理同步消息请求
func (h *StorageHandler) handleQuerySyncMessagesWithDB(database *gorm.DB, req *storage.RequestMessage, query *storage.QuerySyncMessages) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()
	if req.GetTargetUserId() <= 0 || req.GetTargetUserId() != query.GetToUserId() {
		sugar.Warnf(
			"安全拒绝同步消息身份不一致: requester_user_id=%d query_user_id=%d",
			req.GetTargetUserId(),
			query.GetToUserId(),
		)
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_FORBIDDEN,
			TargetUserId: req.GetTargetUserId(),
		}, nil
	}

	// 新客户端优先使用复合游标；旧客户端继续使用 timestamp 作为初始下界。
	cursorTimestamp := query.GetCursorTimestamp()
	if cursorTimestamp == "" {
		cursorTimestamp = query.GetTimestamp()
	}

	// 解析时间戳，使用预定义的格式列表
	var timestamp time.Time
	var err error
	parsed := false

	for _, format := range timeFormats {
		timestamp, err = time.Parse(format, cursorTimestamp)
		if err == nil {
			parsed = true
			break
		}
	}

	if !parsed {
		sugar.Warnf("解析时间戳失败，使用默认值: 原始时间戳: %s", cursorTimestamp)
		timestamp = time.Now().Add(-24 * time.Hour) // 默认查询最近24小时
	}

	pageSize := normalizeSyncPageSize(query.GetPageSize())
	cursorMessageID := query.GetCursorMessageId()
	if cursorMessageID < 0 {
		cursorMessageID = 0
	}

	// 查询该复合游标之后的消息，数据库读取 limit+1 判断 has_more。
	start := time.Now()
	page, err := db.GetSyncMessagesPageWithDB(database, query.ToUserId, timestamp.UTC().Format(time.RFC3339), cursorMessageID, pageSize)
	metrics.RecordDatabaseQuery("select", start)
	if err != nil {
		sugar.Errorf("查询同步消息失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	sugar.Debugf("查询到 %d 条同步消息: has_more=%t", len(page.Messages), page.HasMore)

	// 转换为Protobuf格式
	var msgResponses []*storage.MessageRsp
	for _, msg := range page.Messages {
		msgResponses = append(msgResponses, &storage.MessageRsp{
			MessageId:    msg.MessageID,
			FromUserId:   msg.FromUserID,
			ToUserId:     msg.ToUserID,
			Content:      msg.Content,
			Timestamp:    msg.Timestamp,
			MsgType:      msg.MessageType,
			IsGroup:      msg.IsGroup,
			RealFileName: msg.RealFileName,
		})
	}

	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
		Payload: &storage.ResponseMessage_SyncMsgsRsp{
			SyncMsgsRsp: &storage.SyncMessagesRsp{
				Msgs:                msgResponses,
				HasMore:             page.HasMore,
				NextCursorTimestamp: page.NextCursorTimestamp,
				NextCursorMessageId: page.NextCursorMessageID,
			},
		},
	}

	return resp, nil
}

func normalizeSyncPageSize(requested int32) int {
	if requested <= 0 {
		return db.DefaultSyncPageSize
	}
	if requested > db.MaxSyncPageSize {
		return db.MaxSyncPageSize
	}
	return int(requested)
}

// handleUpdateUserName 处理更新用户名请求
func (h *StorageHandler) handleUpdateUserNameWithDB(database *gorm.DB, req *storage.RequestMessage, update *storage.UpdateUserName, cacheKeys *[]string) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 更新数据库
	start := time.Now()
	err := db.UpdateUserNameByIDWithDB(database, update.UserId, update.NewUserName)
	metrics.RecordDatabaseQuery("update", start)
	if err != nil {
		sugar.Errorf("更新用户名失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	// 注意：UpdateUserNameByID 不返回受影响行数
	// 需要检查用户是否存在
	userStart := time.Now()
	user, err := db.GetUserByIDWithDB(database, update.UserId)
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

	cacheKey := fmt.Sprintf("user:%d", update.UserId)
	if cacheKeys != nil {
		*cacheKeys = append(*cacheKeys, cacheKey)
	} else {
		h.clearCacheKeys([]string{cacheKey})
	}

	resp := &storage.ResponseMessage{
		Result:       storage.StorageResult_OK,
		TargetUserId: req.TargetUserId,
	}

	return resp, nil
}

// handleUpdateUserAvatar 处理更新用户头像请求
func (h *StorageHandler) handleUpdateUserAvatarWithDB(database *gorm.DB, req *storage.RequestMessage, update *storage.UpdateUserAvatar, cacheKeys *[]string) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	// 更新数据库
	start := time.Now()
	err := db.UpdateUserAvatarByIDWithDB(database, update.UserId, update.NewAvatarUrl)
	metrics.RecordDatabaseQuery("update", start)
	if err != nil {
		sugar.Errorf("更新用户头像失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	// 检查用户是否存在
	userStart := time.Now()
	user, err := db.GetUserByIDWithDB(database, update.UserId)
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

	cacheKey := fmt.Sprintf("user:%d", update.UserId)
	if cacheKeys != nil {
		*cacheKeys = append(*cacheKeys, cacheKey)
	} else {
		h.clearCacheKeys([]string{cacheKey})
	}

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
				MessageId:    msg.MessageID,
				FromUserId:   msg.FromUserID,
				ToUserId:     msg.ToUserID,
				Content:      msg.Content,
				Timestamp:    msg.Timestamp,
				MsgType:      msg.MessageType,
				IsGroup:      msg.IsGroup,
				RealFileName: msg.RealFileName,
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

func (h *StorageHandler) clearCacheKeys(keys []string) {
	for _, key := range keys {
		h.l1Cache.Del(key)
		if h.l2Cache != nil {
			h.l2Cache.Del(key)
		}
	}
}

// handleQueryUser 处理查询用户信息请求
func (h *StorageHandler) handleQueryUserWithDB(database *gorm.DB, req *storage.RequestMessage, query *storage.QueryUser) (*storage.ResponseMessage, error) {
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
	if database == nil {
		database = h.requestDatabase()
	}
	start := time.Now()
	user, err := db.GetUserByIDWithDB(database, query.UserId)
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
func (h *StorageHandler) handleQueryFileExistsWithDB(database *gorm.DB, req *storage.RequestMessage, query *storage.QueryFileExists) (*storage.ResponseMessage, error) {
	sugar := logger.Sugar()

	fileHash := query.FileHash
	if fileHash == "" {
		return &storage.ResponseMessage{
			Result:       storage.StorageResult_SERVICE_ERROR,
			TargetUserId: req.TargetUserId,
		}, nil
	}

	// 先尝试从缓存获取
	cacheKey := fmt.Sprintf("file_exists:%s", fileHash)
	if cached, ok := h.getFromCache(cacheKey); ok {
		if entry, ok := cached.(fileExistsCacheEntry); ok {
			sugar.Debugf("从缓存获取文件存在性: file_hash=%s, exists=%v", fileHash, entry.Exists)
			return h.buildFileExistsResponse(req, entry.Exists, entry.FileSize, entry.StoragePath), nil
		}
		if entry, ok := cached.(*fileExistsCacheEntry); ok {
			sugar.Debugf("从缓存获取文件存在性: file_hash=%s, exists=%v", fileHash, entry.Exists)
			return h.buildFileExistsResponse(req, entry.Exists, entry.FileSize, entry.StoragePath), nil
		}
	}

	// 从数据库查询
	if database == nil {
		database = h.requestDatabase()
	}
	start := time.Now()
	fileMetadata, err := db.GetFileMetadataWithDB(database, fileHash)
	metrics.RecordDatabaseQuery("select", start)
	if err != nil {
		sugar.Errorf("查询文件元数据失败: %v", err)
		metrics.RecordDatabaseError()
		return nil, err
	}

	exists := fileMetadata != nil && fileMetadata.IsVerified
	var fileSize int64
	var storagePath string
	if exists {
		fileSize = fileMetadata.FileSize
		storagePath = fileMetadata.StoragePath
	}

	// 存入缓存，避免缓存命中时丢失文件大小和存储路径。
	h.setToCache(cacheKey, fileExistsCacheEntry{
		Exists:      exists,
		FileSize:    fileSize,
		StoragePath: storagePath,
	}, 5*time.Minute)

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
