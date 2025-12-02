package handler

import (
	"Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/stretchr/testify/assert"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

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
	_, mock := setupMockDB(t)
	defer func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	}()

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
				FromUserId:  1000,
				ToUserId:    1001,
				Content:     "Hello, World!",
				MessageType: "text",
				IsGroup:     false,
			},
		},
	}

	// 设置数据库期望
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO \"messages\"").
		WithArgs(int64(1000), int64(1001), "Hello, World!", sqlmock.AnyArg(), "text", false).
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
}

func TestHandleQueryMessage(t *testing.T) {
	_, mock := setupMockDB(t)
	defer func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	}()

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
	assert.Equal(t, int64(1000), msgRsp.FromUserId)
	assert.Equal(t, int64(1001), msgRsp.ToUserId)
	assert.Equal(t, "Hello, World!", msgRsp.Content)
	assert.Equal(t, "text", msgRsp.MsgType)
	assert.Equal(t, false, msgRsp.IsGroup)
}

func TestHandleQueryMessage_NotFound(t *testing.T) {
	_, mock := setupMockDB(t)
	defer func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	}()

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
	_, mock := setupMockDB(t)
	defer func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	}()

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

	// 调用处理函数
	resp, err := handler.handleUpdateUserName(req, req.GetUpdateUserName())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)
}

func TestHandleUpdateUserName_NotFound(t *testing.T) {
	_, mock := setupMockDB(t)
	defer func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Errorf("存在未满足的期望: %s", err)
		}
	}()

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

	// 调用处理函数
	resp, err := handler.handleUpdateUserName(req, req.GetUpdateUserName())

	// 验证结果
	assert.NoError(t, err)
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_RECORD_NOT_EXIST, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)
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
		MessageID:   12345,
		FromUserID:  1000,
		ToUserID:    1001,
		Content:     "Test message",
		Timestamp:   "2024-01-01 12:00:00",
		MessageType: "text",
		IsGroup:     false,
	}

	// 构建响应
	resp := handler.buildMessageResponse(req, message)

	// 验证结果
	assert.NotNil(t, resp)
	assert.Equal(t, storage.StorageResult_OK, resp.Result)
	assert.Equal(t, int64(1001), resp.TargetUserId)

	msgRsp := resp.GetMsgRsp()
	assert.NotNil(t, msgRsp)
	assert.Equal(t, int64(1000), msgRsp.FromUserId)
	assert.Equal(t, int64(1001), msgRsp.ToUserId)
	assert.Equal(t, "Test message", msgRsp.Content)
	assert.Equal(t, "2024-01-01 12:00:00", msgRsp.Timestamp)
	assert.Equal(t, "text", msgRsp.MsgType)
	assert.Equal(t, false, msgRsp.IsGroup)
}
