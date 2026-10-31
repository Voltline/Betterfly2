package bot

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"Betterfly2/shared/db"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func aichatPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("aichat_%d", time.Now().UnixNano())
	if err := base.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { base.Exec("DROP SCHEMA " + schema + " CASCADE"); sql, _ := base.DB(); sql.Close() })
	database, err := gorm.Open(postgres.Open(dsn+" search_path="+schema), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { sql, _ := database.DB(); sql.Close() })
	if err := db.RunMigrations(database); err != nil {
		t.Fatal(err)
	}
	return database
}

func TestEnsureBotUserPostgres(t *testing.T) {
	database := aichatPostgres(t)
	ctx := context.Background()
	identity := Identity{UserID: 900000000000000002, Account: "betterfly_assistant", Name: "Betterfly 助手"}
	if err := EnsureBotUser(ctx, database, identity); err != nil {
		t.Fatal(err)
	}
	if err := EnsureBotUser(ctx, database, identity); err != nil {
		t.Fatalf("second ensure: %v", err)
	}
	// 显式ID插入不能推进序列，否则后注册的用户ID会跳到机器人ID之后。
	var next int64
	if err := database.Raw("SELECT nextval('users_id_seq')").Scan(&next).Error; err != nil {
		t.Fatal(err)
	}
	if next >= identity.UserID {
		t.Fatalf("users_id_seq advanced to %d", next)
	}

	identity.Name, identity.Avatar = "新名字", "https://example.com/bot.png"
	if err := EnsureBotUser(ctx, database, identity); err != nil {
		t.Fatal(err)
	}
	var user db.User
	if err := database.Where("id = ?", identity.UserID).Take(&user).Error; err != nil {
		t.Fatal(err)
	}
	if user.Name != "新名字" || user.Avatar != identity.Avatar || user.UpdateTime == "" || user.PasswordHash != "" {
		t.Fatalf("bot user not updated: %+v", user)
	}

	if err := database.Exec("INSERT INTO users (id, account, name, password_hash) VALUES (42, 'taken', 'x', 'hash')").Error; err != nil {
		t.Fatal(err)
	}
	err := EnsureBotUser(ctx, database, Identity{UserID: 900000000000000003, Account: "taken", Name: "bot"})
	if err == nil || !strings.Contains(err.Error(), "已被其他用户使用") {
		t.Fatalf("account conflict err = %v", err)
	}
	err = EnsureBotUser(ctx, database, Identity{UserID: 42, Account: "other_bot", Name: "bot"})
	if err == nil || !strings.Contains(err.Error(), "不是机器人账号") {
		t.Fatalf("id conflict err = %v", err)
	}
}

func TestGormStoreHistoryPostgres(t *testing.T) {
	database := aichatPostgres(t)
	ctx := context.Background()
	const bot, alice, bob, group = int64(900000000000000002), int64(10), int64(11), int64(500)
	for _, user := range []db.User{{ID: alice, Account: "alice", Name: "Alice"}, {ID: bob, Account: "bob", Name: "Bob"}} {
		if err := database.Create(&user).Error; err != nil {
			t.Fatal(err)
		}
	}
	if err := database.Create(&db.Group{GroupID: group, Name: "测试群"}).Error; err != nil {
		t.Fatal(err)
	}
	clientID := "chatbot:1:0"
	rows := []db.Message{
		{MessageID: 1, FromUserID: alice, ToUserID: bot, Content: "你好", MessageType: "text"},
		{MessageID: 2, FromUserID: bot, ToUserID: alice, Content: "你好呀", MessageType: "text", ClientMessageID: &clientID},
		{MessageID: 3, FromUserID: bob, ToUserID: bot, Content: "别人的", MessageType: "text"},
		{MessageID: 4, FromUserID: alice, ToUserID: bot, Content: "再问", MessageType: "text"},
		{MessageID: 5, FromUserID: alice, ToUserID: bot, Content: "之后的", MessageType: "text"},
		{MessageID: 6, FromUserID: alice, ToUserID: group, Content: "群消息", MessageType: "text", IsGroup: true},
		{MessageID: 7, FromUserID: bob, ToUserID: group, Content: "讨论", MessageType: "text", IsGroup: true, DiscussionRootMessageID: 6},
		{MessageID: 8, FromUserID: bob, ToUserID: group, Content: "@助手", MessageType: "text", IsGroup: true},
	}
	for _, row := range rows {
		if err := database.Create(&row).Error; err != nil {
			t.Fatal(err)
		}
	}
	store := NewGormStore(database)
	direct, err := store.DirectHistory(ctx, alice, bot, 4, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(direct); ids != "4,2,1" {
		t.Fatalf("direct history = %s", ids)
	}
	groupHistory, err := store.GroupHistory(ctx, group, 8, 10)
	if err != nil {
		t.Fatal(err)
	}
	if ids := messageIDs(groupHistory); ids != "8,6" {
		t.Fatalf("group history = %s", ids)
	}
	if exists, err := store.ReplyExists(ctx, bot, clientID); err != nil || !exists {
		t.Fatalf("reply exists = %v, %v", exists, err)
	}
	if exists, err := store.ReplyExists(ctx, bot, "chatbot:4:0"); err != nil || exists {
		t.Fatalf("missing reply exists = %v, %v", exists, err)
	}
	names, err := store.UserNames(ctx, []int64{alice, bob})
	if err != nil || names[alice] != "Alice" || names[bob] != "Bob" {
		t.Fatalf("names = %v, %v", names, err)
	}
	if name, err := store.GroupName(ctx, group); err != nil || name != "测试群" {
		t.Fatalf("group name = %q, %v", name, err)
	}
	if name, err := store.GroupName(ctx, 999); err != nil || name != "" {
		t.Fatalf("missing group name = %q, %v", name, err)
	}
}

func messageIDs(messages []db.Message) string {
	ids := make([]string, 0, len(messages))
	for _, message := range messages {
		ids = append(ids, fmt.Sprint(message.MessageID))
	}
	return strings.Join(ids, ",")
}

func TestRegisterKafkaBotAndImagePathPostgres(t *testing.T) {
	database := aichatPostgres(t)
	ctx := context.Background()
	const botID = int64(900000000000000002)
	if err := RegisterKafkaBot(ctx, database, botID, "aichat-service", "内置AI助手"); err != nil {
		t.Fatal(err)
	}
	// 管理员停用后重启不能把它重新打开；投递方式被改错时恢复。
	if err := database.Exec("UPDATE bots SET disabled = TRUE, allow_groups = FALSE, delivery = 'api', token_hash = 'x' WHERE user_id = ?", botID).Error; err != nil {
		t.Fatal(err)
	}
	if err := RegisterKafkaBot(ctx, database, botID, "aichat-service", "内置AI助手"); err != nil {
		t.Fatal(err)
	}
	var record db.Bot
	if err := database.Take(&record, "user_id = ?", botID).Error; err != nil {
		t.Fatal(err)
	}
	if record.Delivery != db.BotDeliveryKafka || record.KafkaTopic != "aichat-service" || record.TokenHash != nil || !record.Disabled || record.AllowGroups {
		t.Fatalf("record = %+v", record)
	}

	store := NewGormStore(database)
	verified, pending := strings.Repeat("a", 128), strings.Repeat("b", 128)
	if err := db.StoreVerifiedFileWithDB(database, verified, 10, "aa/"+verified, 10); err != nil {
		t.Fatal(err)
	}
	if err := db.UpsertPendingFileMetadataWithDB(database, pending, 10, "bb/"+pending); err != nil {
		t.Fatal(err)
	}
	for hash, want := range map[string]string{verified: "aa/" + verified, pending: "", strings.Repeat("c", 128): ""} {
		if path, err := store.ImagePath(ctx, hash); err != nil || path != want {
			t.Fatalf("ImagePath(%.4s) = %q, %v", hash, path, err)
		}
	}
}
