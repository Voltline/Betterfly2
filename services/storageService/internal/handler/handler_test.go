package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"Betterfly2/shared/kafkaconsumer"
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestHandleMessageDatabaseFailureIsRetriedWithoutCompletedInbox(t *testing.T) {
	database, mock := setupMockDB(t)
	handler := &StorageHandler{l1Cache: newMockCache(), database: database}
	request := &storage.RequestMessage{
		FromKafkaTopic: "df-storage-test", TargetUserId: 1001,
		Payload: &storage.RequestMessage_QueryMessage{QueryMessage: &storage.QueryMessage{MessageId: 77}},
	}
	payload, err := proto.Marshal(request)
	if err != nil {
		t.Fatal(err)
	}
	ctx := kafkaconsumer.WithOperationKey(context.Background(), "storage-service/0/77")
	injected := errors.New("database temporarily unavailable")

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnError(injected)
	mock.ExpectRollback()
	if err := handler.HandleMessage(ctx, payload); !errors.Is(err, injected) {
		t.Fatalf("expected transient database error, got %v", err)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectQuery(`SELECT \* FROM "messages"`).WillReturnRows(sqlmock.NewRows([]string{
		"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "is_group",
	}))
	mock.ExpectExec(`INSERT INTO "outbox_events"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`UPDATE "consumer_inboxes"`).WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()
	if err := handler.HandleMessage(ctx, payload); err != nil {
		t.Fatalf("retry after transient failure failed: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatal(err)
	}
}

// mockCache 模拟缓存实现
type mockCache struct {
	storage map[string]interface{}
}

func newMockCache() *mockCache {
	return &mockCache{
		storage: make(map[string]interface{}),
	}
}

func (m *mockCache) Set(key string, value interface{}, ttl time.Duration) bool {
	m.storage[key] = value
	return true
}

func (m *mockCache) Get(key string) (interface{}, bool) {
	val, ok := m.storage[key]
	return val, ok
}

func (m *mockCache) Del(key string) {
	delete(m.storage, key)
}

func (m *mockCache) Close() {
	// 什么都不做
}

// setupMockDB 创建模拟数据库连接
func setupMockDB(t *testing.T) (*gorm.DB, sqlmock.Sqlmock) {
	dbMock, mock, err := sqlmock.New()
	if err != nil {
		t.Fatalf("创建模拟数据库失败: %v", err)
	}

	dialector := postgres.New(postgres.Config{
		Conn:       dbMock,
		DriverName: "postgres",
	})

	gormDB, err := gorm.Open(dialector, &gorm.Config{})
	if err != nil {
		t.Fatalf("创建GORM连接失败: %v", err)
	}

	return gormDB, mock
}

func useMockDB(t *testing.T) sqlmock.Sqlmock {
	t.Helper()

	gormDB, mock := setupMockDB(t)
	originalDBFunc := db.DB
	db.DB = func(dst ...interface{}) *gorm.DB {
		return gormDB
	}

	t.Cleanup(func() {
		db.DB = originalDBFunc
		sqlDB, err := gormDB.DB()
		if err == nil {
			_ = sqlDB.Close()
		}
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	})

	return mock
}

func TestNewStorageHandler(t *testing.T) {
	_, _ = setupMockDB(t)
	// 注意：我们不检查模拟数据库的期望，因为这个测试只验证handler创建

	// 创建handler实例
	handler := &StorageHandler{
		l1Cache: newMockCache(), // 使用模拟缓存
		l2Cache: nil,
	}

	assert.NotNil(t, handler)
	// db字段已移除，不再检查
}

func TestHandleStoreNewMessage(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	// 创建测试请求
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_StoreNewMessage{
			StoreNewMessage: &storage.StoreNewMessage{
				FromUserId:      1000,
				ToUserId:        1001,
				Content:         "Hello, World!",
				MessageType:     "text",
				IsGroup:         false,
				RealFileName:    "",
				ClientMessageId: "client-message-1",
				ClientTimestamp: "2026-07-12T09:00:00Z",
			},
		},
	}

	// 设置数据库期望
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO \"messages\"").
		WithArgs("client-message-1", int64(1000), int64(1001), "Hello, World!", sqlmock.AnyArg(), "text", "", false).
		WillReturnRows(sqlmock.NewRows([]string{"message_id"}).AddRow(12345))
	mock.ExpectCommit()

	// 调用处理函数
	resp, err := handler.handleStoreNewMessage(req, req.GetStoreNewMessage())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)

	storeRsp := resp.GetStoreMsgRsp()
	assert.NotNil(t, storeRsp)
	assert.Equal(t, int64(12345), storeRsp.MessageId)
	assert.Equal(t, "client-message-1", storeRsp.ClientMessageId)
	assert.True(t, storeRsp.Created)
	assert.Equal(t, "2026-07-12T09:00:00Z", storeRsp.ClientTimestamp)
}

func TestHandleStoreNewMessageReturnsExistingMessageForDuplicateClientID(t *testing.T) {
	mock := useMockDB(t)
	handler := &StorageHandler{l1Cache: newMockCache()}
	msg := &storage.StoreNewMessage{
		FromUserId:      1000,
		ToUserId:        1001,
		Content:         "Hello, World!",
		MessageType:     "text",
		ClientMessageId: "client-message-1",
		ClientTimestamp: "2026-07-12T09:00:00Z",
	}
	req := &storage.RequestMessage{TargetUserId: 1000, Payload: &storage.RequestMessage_StoreNewMessage{StoreNewMessage: msg}}

	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO \"messages\"").
		WithArgs("client-message-1", int64(1000), int64(1001), "Hello, World!", sqlmock.AnyArg(), "text", "", false).
		WillReturnRows(sqlmock.NewRows([]string{"message_id"}))
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM \"messages\" WHERE from_user_id = \\$1 AND client_message_id = \\$2 ORDER BY \"messages\".\"message_id\" LIMIT \\$3").
		WithArgs(int64(1000), "client-message-1", int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"message_id", "client_message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "real_file_name", "is_group",
		}).AddRow(12345, "client-message-1", 1000, 1001, "Hello, World!", "2026-07-12T09:00:01Z", "text", "", false))

	resp, err := handler.handleStoreNewMessage(req, msg)
	assert.NoError(t, err)
	if assert.NotNil(t, resp) && assert.NotNil(t, resp.GetStoreMsgRsp()) {
		assert.Equal(t, int64(12345), resp.GetStoreMsgRsp().GetMessageId())
		assert.False(t, resp.GetStoreMsgRsp().GetCreated())
	}
}

func TestHandleQueryMessage(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	// 创建测试请求
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_QueryMessage{
			QueryMessage: &storage.QueryMessage{
				MessageId: 12345,
			},
		},
	}

	// 设置数据库期望 - 查询消息
	expectedTime := time.Now().Format("2006-01-02 15:04:05")
	mock.ExpectQuery("SELECT \\* FROM \"messages\" WHERE message_id = \\$1 ORDER BY \"messages\".\"message_id\" LIMIT \\$2").
		WithArgs(int64(12345), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "is_group",
		}).AddRow(12345, 1000, 1001, "Hello, World!", expectedTime, "text", false))

	// 调用处理函数
	resp, err := handler.handleQueryMessage(req, req.GetQueryMessage())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)

	msgRsp := resp.GetMsgRsp()
	assert.NotNil(t, msgRsp)
	assert.Equal(t, int64(12345), msgRsp.MessageId)
	assert.Equal(t, int64(1000), msgRsp.FromUserId)
	assert.Equal(t, int64(1001), msgRsp.ToUserId)
	assert.Equal(t, "Hello, World!", msgRsp.Content)
	assert.Equal(t, "text", msgRsp.MsgType)
	assert.Equal(t, false, msgRsp.IsGroup)
}

func TestHandleQueryMessage_NotFound(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	// 创建测试请求
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_QueryMessage{
			QueryMessage: &storage.QueryMessage{
				MessageId: 99999,
			},
		},
	}

	// 设置数据库期望 - 查询不到消息
	mock.ExpectQuery("SELECT \\* FROM \"messages\" WHERE message_id = \\$1 ORDER BY \"messages\".\"message_id\" LIMIT \\$2").
		WithArgs(int64(99999), int64(1)).
		WillReturnRows(sqlmock.NewRows([]string{
			"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "is_group",
		}))

	// 调用处理函数
	resp, err := handler.handleQueryMessage(req, req.GetQueryMessage())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_RECORD_NOT_EXIST, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)
}

func TestHandleUpdateUserName(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	// 创建测试请求
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_UpdateUserName{
			UpdateUserName: &storage.UpdateUserName{
				UserId:      1000,
				NewUserName: "NewUsername",
			},
		},
	}

	// 设置数据库期望
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE \"users\" SET .* WHERE id = \\$3").
		WithArgs("NewUsername", sqlmock.AnyArg(), int64(1000)).
		WillReturnResult(sqlmock.NewResult(0, 1)) // 影响1行
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM \"users\" WHERE \"users\".\"id\" = \\$1 ORDER BY \"users\".\"id\" LIMIT \\$2").
		WithArgs(int64(1000), 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account", "name", "update_time", "avatar", "password_hash", "jwt_key",
		}).AddRow(1000, "test-account", "NewUsername", time.Now().Format("2006-01-02 15:04:05"), "", "", []byte("key")))

	// 调用处理函数
	resp, err := handler.handleUpdateUserName(req, req.GetUpdateUserName())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)
}

func TestHandleUpdateUserName_NotFound(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	// 创建测试请求
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_UpdateUserName{
			UpdateUserName: &storage.UpdateUserName{
				UserId:      99999,
				NewUserName: "NewUsername",
			},
		},
	}

	// 设置数据库期望 - 更新0行
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE \"users\" SET .* WHERE id = \\$3").
		WithArgs("NewUsername", sqlmock.AnyArg(), int64(99999)).
		WillReturnResult(sqlmock.NewResult(0, 0)) // 影响0行
	mock.ExpectCommit()
	mock.ExpectQuery("SELECT \\* FROM \"users\" WHERE \"users\".\"id\" = \\$1 ORDER BY \"users\".\"id\" LIMIT \\$2").
		WithArgs(int64(99999), 1).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "account", "name", "update_time", "avatar", "password_hash", "jwt_key",
		}))

	// 调用处理函数
	resp, err := handler.handleUpdateUserName(req, req.GetUpdateUserName())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_RECORD_NOT_EXIST, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)
}

func TestHandleQuerySyncMessages_IncludesDirectAndGroupMessages(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_QuerySyncMessages{
			QuerySyncMessages: &storage.QuerySyncMessages{
				ToUserId:  1001,
				Timestamp: "2026-04-17T10:00:00Z",
			},
		},
	}

	mock.ExpectQuery(`(?s)SELECT \*.*m\.to_user_id = \$1.*m\.timestamp > \$2.*m\.timestamp = \$3.*m\.message_id > \$4.*m\.timestamp > \$5.*m\.timestamp = \$6.*m\.message_id > \$7.*gm\.user_id = \$8.*ORDER BY timestamp ASC, message_id ASC\s+LIMIT \$9`).
		WithArgs(int64(1001), "2026-04-17T10:00:00Z", "2026-04-17T10:00:00Z", int64(0), "2026-04-17T10:00:00Z", "2026-04-17T10:00:00Z", int64(0), int64(1001), 101).
		WillReturnRows(sqlmock.NewRows([]string{
			"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "real_file_name", "is_group",
		}).
			AddRow(20001, 2002, 1001, "direct-msg", "2026-04-17T10:05:00Z", "text", "", false).
			AddRow(20002, 1001, 9001, "own-group-msg", "2026-04-17T10:06:00Z", "text", "", true).
			AddRow(20003, 3003, 9001, "other-group-msg", "2026-04-17T10:07:00Z", "text", "", true))

	resp, err := handler.handleQuerySyncMessages(req, req.GetQuerySyncMessages())

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)

	syncRsp := resp.GetSyncMsgsRsp()
	assert.NotNil(t, syncRsp)
	if syncRsp != nil && assert.Len(t, syncRsp.GetMsgs(), 3) {
		assert.Equal(t, int64(20001), syncRsp.GetMsgs()[0].GetMessageId())
		assert.Equal(t, int64(1001), syncRsp.GetMsgs()[0].GetToUserId())
		assert.False(t, syncRsp.GetMsgs()[0].GetIsGroup())
		assert.Equal(t, int64(20002), syncRsp.GetMsgs()[1].GetMessageId())
		assert.Equal(t, int64(1001), syncRsp.GetMsgs()[1].GetFromUserId())
		assert.Equal(t, int64(9001), syncRsp.GetMsgs()[1].GetToUserId())
		assert.True(t, syncRsp.GetMsgs()[1].GetIsGroup())
		assert.Equal(t, int64(20003), syncRsp.GetMsgs()[2].GetMessageId())
		assert.Equal(t, int64(3003), syncRsp.GetMsgs()[2].GetFromUserId())
		assert.Equal(t, int64(9001), syncRsp.GetMsgs()[2].GetToUserId())
		assert.True(t, syncRsp.GetMsgs()[2].GetIsGroup())
		assert.False(t, syncRsp.GetHasMore())
		assert.Equal(t, "2026-04-17T10:07:00Z", syncRsp.GetNextCursorTimestamp())
		assert.Equal(t, int64(20003), syncRsp.GetNextCursorMessageId())
	}
}

func TestHandleQuerySyncMessages_DefaultPageIsBounded(t *testing.T) {
	mock := useMockDB(t)

	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
		Payload: &storage.RequestMessage_QuerySyncMessages{
			QuerySyncMessages: &storage.QuerySyncMessages{
				ToUserId:  1001,
				Timestamp: "2026-04-17T10:00:00Z",
			},
		},
	}

	rows := sqlmock.NewRows([]string{
		"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "real_file_name", "is_group",
	})
	for i := 0; i < 101; i++ {
		rows.AddRow(
			int64(30000+i),
			int64(2000+i),
			int64(1001),
			fmt.Sprintf("msg-%d", i),
			fmt.Sprintf("2026-04-17T10:%02d:00Z", i%60),
			"text",
			"",
			false,
		)
	}

	mock.ExpectQuery(`(?s)SELECT \*.*ORDER BY timestamp ASC, message_id ASC\s+LIMIT \$9`).
		WithArgs(int64(1001), "2026-04-17T10:00:00Z", "2026-04-17T10:00:00Z", int64(0), "2026-04-17T10:00:00Z", "2026-04-17T10:00:00Z", int64(0), int64(1001), 101).
		WillReturnRows(rows)

	resp, err := handler.handleQuerySyncMessages(req, req.GetQuerySyncMessages())

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	syncRsp := resp.GetSyncMsgsRsp()
	assert.NotNil(t, syncRsp)
	if syncRsp != nil {
		assert.Len(t, syncRsp.GetMsgs(), 100)
		assert.True(t, syncRsp.GetHasMore())
		assert.Equal(t, int64(30099), syncRsp.GetNextCursorMessageId())
	}
}

func TestNormalizeSyncPageSizeDefaultsAndCaps(t *testing.T) {
	for _, test := range []struct {
		requested int32
		want      int
	}{{0, 100}, {-1, 100}, {1, 1}, {500, 500}, {501, 500}} {
		if got := normalizeSyncPageSize(test.requested); got != test.want {
			t.Fatalf("page_size %d normalized to %d, want %d", test.requested, got, test.want)
		}
	}
}

func TestSyncCompositeCursorDoesNotRepeatEqualTimestamps(t *testing.T) {
	mock := useMockDB(t)
	handler := &StorageHandler{l1Cache: newMockCache()}
	queryPattern := `(?s)SELECT \*.*m\.timestamp = \$3 AND m\.message_id > \$4.*m\.timestamp = \$6 AND m\.message_id > \$7.*ORDER BY timestamp ASC, message_id ASC\s+LIMIT \$9`
	timestamp := "2026-04-17T10:00:00Z"
	columns := []string{"message_id", "from_user_id", "to_user_id", "content", "timestamp", "message_type", "real_file_name", "is_group"}
	mock.ExpectQuery(queryPattern).
		WithArgs(int64(1001), timestamp, timestamp, int64(0), timestamp, timestamp, int64(0), int64(1001), 3).
		WillReturnRows(sqlmock.NewRows(columns).
			AddRow(1, 2, 1001, "direct", timestamp, "text", "", false).
			AddRow(2, 3, 9001, "group", timestamp, "text", "", true).
			AddRow(3, 4, 1001, "next", timestamp, "text", "", false))
	request := &storage.RequestMessage{TargetUserId: 1001, Payload: &storage.RequestMessage_QuerySyncMessages{QuerySyncMessages: &storage.QuerySyncMessages{ToUserId: 1001, CursorTimestamp: timestamp, PageSize: 2}}}
	first, err := handler.handleQuerySyncMessages(request, request.GetQuerySyncMessages())
	if err != nil {
		t.Fatal(err)
	}
	if len(first.GetSyncMsgsRsp().GetMsgs()) != 2 || !first.GetSyncMsgsRsp().GetHasMore() || first.GetSyncMsgsRsp().GetNextCursorMessageId() != 2 {
		t.Fatalf("unexpected first page: %+v", first.GetSyncMsgsRsp())
	}

	mock.ExpectQuery(queryPattern).
		WithArgs(int64(1001), timestamp, timestamp, int64(2), timestamp, timestamp, int64(2), int64(1001), 3).
		WillReturnRows(sqlmock.NewRows(columns).AddRow(3, 4, 1001, "next", timestamp, "text", "", false))
	request.GetQuerySyncMessages().CursorMessageId = 2
	second, err := handler.handleQuerySyncMessages(request, request.GetQuerySyncMessages())
	if err != nil {
		t.Fatal(err)
	}
	if len(second.GetSyncMsgsRsp().GetMsgs()) != 1 || second.GetSyncMsgsRsp().GetMsgs()[0].GetMessageId() != 3 || second.GetSyncMsgsRsp().GetHasMore() {
		t.Fatalf("equal timestamp cursor repeated or omitted messages: %+v", second.GetSyncMsgsRsp())
	}
}

func TestBuildMessageResponse(t *testing.T) {
	handler := &StorageHandler{}

	// 创建测试请求
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
	}

	// 创建测试消息
	message := &db.Message{
		MessageID:    12345,
		FromUserID:   1000,
		ToUserID:     1001,
		Content:      "Test message",
		Timestamp:    "2024-01-01 12:00:00",
		MessageType:  "text",
		RealFileName: "",
		IsGroup:      false,
	}

	// 构建响应
	resp := handler.buildMessageResponse(req, message)

	// 验证结果
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)

	msgRsp := resp.GetMsgRsp()
	assert.NotNil(t, msgRsp)
	assert.Equal(t, int64(12345), msgRsp.MessageId)
	assert.Equal(t, int64(1000), msgRsp.FromUserId)
	assert.Equal(t, int64(1001), msgRsp.ToUserId)
	assert.Equal(t, "Test message", msgRsp.Content)
	assert.Equal(t, "2024-01-01 12:00:00", msgRsp.Timestamp)
	assert.Equal(t, "text", msgRsp.MsgType)
	assert.Equal(t, false, msgRsp.IsGroup)
}

func TestBuildMessageResponse_PreservesRealFileName(t *testing.T) {
	handler := &StorageHandler{}
	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
	}
	message := &db.Message{
		MessageID:    12346,
		FromUserID:   1000,
		ToUserID:     1001,
		Content:      "sha512-hash",
		Timestamp:    "2024-01-01 12:00:01",
		MessageType:  "file",
		RealFileName: "report.pdf",
		IsGroup:      false,
	}

	resp := handler.buildMessageResponse(req, message)

	msgRsp := resp.GetMsgRsp()
	assert.NotNil(t, msgRsp)
	assert.Equal(t, int64(12346), msgRsp.GetMessageId())
	assert.Equal(t, "file", msgRsp.GetMsgType())
	assert.Equal(t, "report.pdf", msgRsp.GetRealFileName())
}

func TestHandleQueryFileExists_UsesCachedMetadata(t *testing.T) {
	handler := &StorageHandler{
		l1Cache: newMockCache(),
		l2Cache: nil,
	}

	cacheKey := "file_exists:hash123"
	handler.l1Cache.Set(cacheKey, fileExistsCacheEntry{
		Exists:      true,
		FileSize:    4096,
		StoragePath: "ab/hash123",
	}, time.Minute)

	req := &storage.RequestMessage{
		FromKafkaTopic: "test-topic",
		TargetUserId:   1001,
	}
	query := &storage.QueryFileExists{
		FileHash: "hash123",
	}

	resp, err := handler.handleQueryFileExists(req, query)

	assert.NoError(t, err)
	assert.NotNil(t, resp)
	fileRsp := resp.GetFileExistsRsp()
	assert.NotNil(t, fileRsp)
	assert.True(t, fileRsp.GetExists())
	assert.Equal(t, int64(4096), fileRsp.GetFileSize())
	assert.Equal(t, "ab/hash123", fileRsp.GetStoragePath())
}
