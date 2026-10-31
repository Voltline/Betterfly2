package platform

import (
	"bytes"
	"context"
	"crypto/sha512"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	friend "Betterfly2/proto/friend"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"chatbotService/internal/config"

	"google.golang.org/protobuf/proto"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/logger"
)

func chatbotPostgres(t *testing.T) *gorm.DB {
	t.Helper()
	dsn := os.Getenv("BETTERFLY_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("requires isolated BETTERFLY_TEST_POSTGRES_DSN")
	}
	base, err := gorm.Open(postgres.Open(dsn), &gorm.Config{Logger: logger.Default.LogMode(logger.Silent)})
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("chatbot_%d", time.Now().UnixNano())
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

type published struct {
	topic, key string
	envelope   *envelope.Envelope
}

// fakeKafka 记录发布的消息；发往storage的入库请求直接写库，模拟StorageService。
type fakeKafka struct {
	t        *testing.T
	database *gorm.DB
	mu       sync.Mutex
	messages []published
	failNext bool
}

func (k *fakeKafka) publish(_ context.Context, topic, key string, payload []byte) error {
	var message envelope.Envelope
	if err := proto.Unmarshal(payload, &message); err != nil {
		k.t.Fatal(err)
	}
	k.mu.Lock()
	k.messages = append(k.messages, published{topic: topic, key: key, envelope: &message})
	k.mu.Unlock()
	if topic != "storage-service" {
		return nil
	}
	var request storage.RequestMessage
	if err := proto.Unmarshal(message.GetPayload(), &request); err != nil {
		k.t.Fatal(err)
	}
	if request.GetFromKafkaTopic() != "df-shared" {
		k.t.Errorf("storage reply topic = %q", request.GetFromKafkaTopic())
	}
	store := request.GetStoreNewMessage()
	_, _, err := db.StoreNewMessageWithDB(k.database, store.GetFromUserId(), store.GetToUserId(), store.GetContent(), store.GetMessageType(),
		store.GetRealFileName(), store.GetIsGroup(), store.GetClientMessageId(), store.GetCaption(), store.GetReplyToMessageId())
	return err
}

func (k *fakeKafka) take(topic string) []published {
	k.mu.Lock()
	defer k.mu.Unlock()
	var matched, rest []published
	for _, message := range k.messages {
		if message.topic == topic {
			matched = append(matched, message)
		} else {
			rest = append(rest, message)
		}
	}
	k.messages = rest
	return matched
}

type memoryObjects struct {
	mu   sync.Mutex
	data map[string][]byte
}

func (o *memoryObjects) Open(_ context.Context, key string) (io.ReadCloser, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	data, ok := o.data[key]
	if !ok {
		return nil, fmt.Errorf("no object %s", key)
	}
	return io.NopCloser(bytes.NewReader(data)), nil
}

func (o *memoryObjects) Put(_ context.Context, key string, body io.ReadSeeker, _ int64) error {
	data, err := io.ReadAll(body)
	o.mu.Lock()
	o.data[key] = data
	o.mu.Unlock()
	return err
}

type harness struct {
	t        *testing.T
	database *gorm.DB
	platform *Platform
	kafka    *fakeKafka
	server   *httptest.Server
}

const (
	alice      int64 = 1001
	bob        int64 = 1002
	groupID    int64 = 77
	adminToken       = "admin-token-0123456789"
)

func newHarness(t *testing.T) *harness {
	database := chatbotPostgres(t)
	kafka := &fakeKafka{t: t, database: database}
	cfg := config.Config{
		StorageTopic: "storage-service", FriendTopic: "friend-service", ReplyTopic: "df-shared", AdminToken: adminToken,
		UpdateRetention: time.Hour, MaxPollTimeout: 5 * time.Second, SendWait: 2 * time.Second,
		WebhookTimeout: 2 * time.Second, WebhookAllowPrivate: true, MaxUploadBytes: 1 << 20,
	}
	platform := New(cfg, NewStore(database), kafka.publish, &memoryObjects{data: map[string][]byte{}})
	mux := http.NewServeMux()
	platform.Routes(mux)
	platform.AdminRoutes(mux)
	server := httptest.NewServer(mux)
	t.Cleanup(server.Close)
	h := &harness{t: t, database: database, platform: platform, kafka: kafka, server: server}
	h.exec("INSERT INTO users (id, account, name, password_hash) VALUES (?, 'alice', 'Alice', 'x'), (?, 'bob', 'Bob', 'x')", alice, bob)
	// 内置AI助手：Kafka投递
	h.exec("INSERT INTO users (id, account, name, password_hash) VALUES (?, 'betterfly_assistant', 'Betterfly 助手', '')", db.BotUserIDMin)
	h.exec("INSERT INTO bots (user_id, delivery, kafka_topic, allow_groups, created_at, updated_at) VALUES (?, 'kafka', 'aichat-service', TRUE, now(), now())", db.BotUserIDMin)
	h.exec("INSERT INTO groups (group_id, name, owner_user_id) VALUES (?, '测试群', ?)", groupID, alice)
	return h
}

func (h *harness) exec(sql string, args ...any) {
	h.t.Helper()
	if err := h.database.Exec(sql, args...).Error; err != nil {
		h.t.Fatal(err)
	}
}

type response struct {
	status int
	header http.Header
	body   []byte
}

func (r response) result(t *testing.T, target any) {
	t.Helper()
	var envelope struct {
		OK     bool            `json:"ok"`
		Result json.RawMessage `json:"result"`
	}
	if err := json.Unmarshal(r.body, &envelope); err != nil || !envelope.OK {
		t.Fatalf("status %d body %s", r.status, r.body)
	}
	if target != nil {
		if err := json.Unmarshal(envelope.Result, target); err != nil {
			t.Fatal(err)
		}
	}
}

func (r response) errorCode(t *testing.T) string {
	t.Helper()
	var body struct {
		OK    bool `json:"ok"`
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	if err := json.Unmarshal(r.body, &body); err != nil || body.OK {
		t.Fatalf("expected error, status %d body %s", r.status, r.body)
	}
	return body.Error.Code
}

func (h *harness) do(method, path string, headers map[string]string, contentType string, body io.Reader) response {
	h.t.Helper()
	request, err := http.NewRequest(method, h.server.URL+path, body)
	if err != nil {
		h.t.Fatal(err)
	}
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	for key, value := range headers {
		request.Header.Set(key, value)
	}
	resp, err := h.server.Client().Do(request)
	if err != nil {
		h.t.Fatal(err)
	}
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	return response{status: resp.StatusCode, header: resp.Header, body: data}
}

func (h *harness) bot(token, method, path string, body any) response {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	return h.do(method, path, map[string]string{"Authorization": "Bearer " + token}, "application/json", reader)
}

func (h *harness) admin(method, path string, body any) response {
	var reader io.Reader
	if body != nil {
		data, _ := json.Marshal(body)
		reader = bytes.NewReader(data)
	}
	return h.do(method, path, map[string]string{"X-Admin-Token": adminToken}, "application/json", reader)
}

func (h *harness) createBot(account, name string) (int64, string) {
	h.t.Helper()
	resp := h.admin(http.MethodPost, "/admin/api/bots", map[string]any{"account": account, "name": name})
	if resp.status != http.StatusCreated {
		h.t.Fatalf("create bot: %d %s", resp.status, resp.body)
	}
	var created struct {
		Bot   adminBot `json:"bot"`
		Token string   `json:"token"`
	}
	resp.result(h.t, &created)
	return created.Bot.ID, created.Token
}

func (h *harness) message(from, to int64, isGroup bool, content string, replyTo int64) *db.Message {
	h.t.Helper()
	message, _, err := db.StoreNewMessageWithDB(h.database, from, to, content, "text", "", isGroup, "", "", replyTo)
	if err != nil {
		h.t.Fatal(err)
	}
	return message
}

func (h *harness) trigger(message *db.Message, bots ...int64) {
	h.t.Helper()
	err := h.platform.HandleTrigger(context.Background(), &chatbotpb.ChatbotTrigger{
		MessageId: message.MessageID, FromUserId: message.FromUserID, ToId: message.ToUserID, IsGroup: message.IsGroup, BotUserIds: bots,
	})
	if err != nil {
		h.t.Fatal(err)
	}
}

func (h *harness) updates(token string, offset int64) []Update {
	h.t.Helper()
	resp := h.bot(token, http.MethodGet, fmt.Sprintf("/bot/v1/updates?offset=%d", offset), nil)
	var updates []Update
	resp.result(h.t, &updates)
	return updates
}

func TestAdminAndAuthPostgres(t *testing.T) {
	h := newHarness(t)
	botID, token := h.createBot("weather_bot", "天气 小助手")
	if botID != db.BotUserIDMin+1 {
		t.Fatalf("bot id = %d", botID)
	}
	if second, _ := h.createBot("news_bot", "新闻"); second != botID+1 {
		t.Fatalf("second bot id = %d", second)
	}
	// 显式ID插入不推进普通用户的序列。
	var next int64
	h.database.Raw("SELECT nextval('users_id_seq')").Scan(&next)
	if db.IsBotUserID(next) {
		t.Fatalf("users_id_seq advanced to %d", next)
	}
	for _, request := range []map[string]any{
		{"account": "weather_bot", "name": "重复"},
		{"account": "alice", "name": "抢普通用户账号"},
	} {
		if resp := h.admin(http.MethodPost, "/admin/api/bots", request); resp.status != http.StatusConflict || resp.errorCode(t) != "account_taken" {
			t.Fatalf("duplicate account: %d %s", resp.status, resp.body)
		}
	}
	if resp := h.admin(http.MethodPost, "/admin/api/bots", map[string]any{"account": "Bad-Name", "name": "x"}); resp.status != http.StatusBadRequest {
		t.Fatalf("invalid account: %d", resp.status)
	}
	if resp := h.do(http.MethodGet, "/admin/api/bots", map[string]string{"X-Admin-Token": "wrong-token-0123456789"}, "", nil); resp.status != http.StatusUnauthorized {
		t.Fatalf("wrong admin token: %d", resp.status)
	}
	var bots []adminBot
	h.admin(http.MethodGet, "/admin/api/bots", nil).result(t, &bots)
	if len(bots) != 3 || bots[0].Delivery != db.BotDeliveryKafka || bots[1].Account != "weather_bot" || !bots[1].HasToken {
		t.Fatalf("list = %+v", bots)
	}
	if page := h.do(http.MethodGet, "/admin/", nil, "", nil); page.status != http.StatusOK || !strings.Contains(page.header.Get("Content-Security-Policy"), "default-src 'none'") {
		t.Fatalf("admin page: %d %v", page.status, page.header)
	}

	var me botProfile
	h.bot(token, http.MethodGet, "/bot/v1/me", nil).result(t, &me)
	if me.ID != botID || me.Account != "weather_bot" || !me.AllowGroups {
		t.Fatalf("me = %+v", me)
	}
	for _, bad := range []string{"", "bfbot_wrong", strings.TrimPrefix(token, tokenPrefix)} {
		if resp := h.bot(bad, http.MethodGet, "/bot/v1/me", nil); resp.status != http.StatusUnauthorized {
			t.Fatalf("token %q: %d", bad, resp.status)
		}
	}
	h.bot(token, http.MethodPatch, "/bot/v1/me", map[string]any{"name": " 天气 ", "description": "查天气"}).result(t, &me)
	var user db.User
	h.database.Where("id = ?", botID).Take(&user)
	if me.Name != "天气" || user.Name != "天气" || user.UpdateTime == "" {
		t.Fatalf("rename: %+v %+v", me, user)
	}
	if resp := h.bot(token, http.MethodPatch, "/bot/v1/me", map[string]any{"avatar": strings.Repeat("b", 128)}); resp.errorCode(t) != "invalid_avatar" {
		t.Fatal("unowned avatar accepted")
	}

	var view adminBot
	h.admin(http.MethodPatch, fmt.Sprintf("/admin/api/bots/%d", botID), map[string]any{"disabled": true}).result(t, &view)
	if !view.Disabled {
		t.Fatal("not disabled")
	}
	if resp := h.bot(token, http.MethodGet, "/bot/v1/me", nil); resp.status != http.StatusForbidden || resp.errorCode(t) != "bot_disabled" {
		t.Fatalf("disabled bot: %d", resp.status)
	}
	h.admin(http.MethodPatch, fmt.Sprintf("/admin/api/bots/%d", botID), map[string]any{"disabled": false}).result(t, nil)
	var reset struct {
		Token string `json:"token"`
	}
	h.admin(http.MethodPost, fmt.Sprintf("/admin/api/bots/%d/token", botID), nil).result(t, &reset)
	if resp := h.bot(token, http.MethodGet, "/bot/v1/me", nil); resp.status != http.StatusUnauthorized {
		t.Fatal("old token still works")
	}
	h.bot(reset.Token, http.MethodGet, "/bot/v1/me", nil).result(t, nil)
	if resp := h.admin(http.MethodPost, fmt.Sprintf("/admin/api/bots/%d/token", db.BotUserIDMin), nil); resp.status != http.StatusNotFound {
		t.Fatalf("builtin bot token reset: %d", resp.status)
	}
	if resp := h.admin(http.MethodPatch, "/admin/api/bots/42", map[string]any{"disabled": true}); resp.status != http.StatusNotFound {
		t.Fatalf("non-bot patch: %d", resp.status)
	}

	// 未配置管理令牌时管理接口不存在。
	h.platform.config.AdminToken = ""
	if resp := h.admin(http.MethodGet, "/admin/api/bots", nil); resp.status != http.StatusNotFound {
		t.Fatalf("admin without token: %d", resp.status)
	}
}

func TestRoutingPostgres(t *testing.T) {
	h := newHarness(t)
	botID, token := h.createBot("weather_bot", "天气 小助手")
	otherID, otherToken := h.createBot("news_bot", "新闻")
	h.exec("INSERT INTO group_members (group_id, user_id, role, joined_at) VALUES (?, ?, 'owner', '2000-01-01 00:00:00'), (?, ?, 'member', '2000-01-01 00:00:00'), (?, ?, 'member', '2000-01-01 00:00:00'), (?, ?, 'member', '2000-01-01 00:00:00'), (?, ?, 'member', '2000-01-01 00:00:00')",
		groupID, alice, groupID, bob, groupID, botID, groupID, otherID, groupID, db.BotUserIDMin)
	allBots := []int64{db.BotUserIDMin, botID, otherID}

	// 单聊：命令解析，Kafka重投不重复。
	direct := h.message(alice, botID, false, "/start 北京", 0)
	h.trigger(direct)
	h.trigger(direct)
	updates := h.updates(token, 0)
	if len(updates) != 1 || updates[0].Message.Command != "/start" || updates[0].Message.Chat != (Chat{ID: alice, Type: ChatPrivate}) ||
		updates[0].Message.From.Account != "alice" || updates[0].Message.Text != "/start 北京" {
		t.Fatalf("direct updates = %+v", updates)
	}
	lastID := updates[0].UpdateID

	// 群里：@昵称（忽略空白）、带@账号的命令、不带@的命令、引用机器人消息、机器人发的消息。
	h.trigger(h.message(bob, groupID, true, "@天气小助手 明天下雨吗", 0), allBots...)
	h.trigger(h.message(bob, groupID, true, "/help@news_bot", 0), allBots...)
	h.trigger(h.message(bob, groupID, true, "/help", 0), allBots...)
	botMessage := h.message(botID, groupID, true, "我是天气", 0)
	h.trigger(botMessage, allBots...)
	h.trigger(h.message(alice, groupID, true, "谢谢", botMessage.MessageID), allBots...)
	h.trigger(h.message(alice, groupID, true, "大家好", 0), allBots...)

	updates = h.updates(token, lastID+1)
	var texts []string
	for _, update := range updates {
		if update.Message.Chat.Type != ChatGroup || update.Message.Chat.Title != "测试群" {
			t.Fatalf("group chat = %+v", update.Message.Chat)
		}
		texts = append(texts, update.Message.Text+"|"+update.Message.Command)
	}
	if strings.Join(texts, ",") != "@天气小助手 明天下雨吗|,/help|/help,谢谢|" {
		t.Fatalf("weather bot got %v", texts)
	}
	var otherTexts []string
	var otherLast int64
	for _, update := range h.updates(otherToken, 0) {
		otherTexts = append(otherTexts, update.Message.Text+"|"+update.Message.Command)
		otherLast = update.UpdateID
	}
	if strings.Join(otherTexts, ",") != "/help@news_bot|/help,/help|/help" {
		t.Fatalf("news bot got %v", otherTexts)
	}
	// offset确认之前的更新。
	if again := h.updates(token, updates[len(updates)-1].UpdateID+1); len(again) != 0 {
		t.Fatalf("acked updates returned: %+v", again)
	}
	if forwarded := h.kafka.take("aichat-service"); len(forwarded) != 0 {
		t.Fatalf("AI bot got untargeted messages: %d", len(forwarded))
	}

	// 内置机器人：只在被@时转发，带上自己的ID。
	mention := h.message(bob, groupID, true, "@Betterfly助手 你好", 0)
	h.trigger(mention, allBots...)
	forwarded := h.kafka.take("aichat-service")
	if len(forwarded) != 1 || forwarded[0].key != fmt.Sprintf("g:%d", groupID) || forwarded[0].envelope.GetType() != envelope.MessageType_CHATBOT_REQUEST {
		t.Fatalf("forwarded = %+v", forwarded)
	}
	var request chatbotpb.RequestMessage
	if err := proto.Unmarshal(forwarded[0].envelope.GetPayload(), &request); err != nil {
		t.Fatal(err)
	}
	if trigger := request.GetTrigger(); trigger.GetMessageId() != mention.MessageID || len(trigger.GetBotUserIds()) != 1 || trigger.GetBotUserIds()[0] != db.BotUserIDMin {
		t.Fatalf("forwarded trigger = %+v", trigger)
	}

	// 关闭群聊或停用后不再收到群消息；旧版触发不带bot_user_ids时按群成员查。
	h.admin(http.MethodPatch, fmt.Sprintf("/admin/api/bots/%d", botID), map[string]any{"allow_groups": false}).result(t, nil)
	h.trigger(h.message(bob, groupID, true, "@天气小助手 还在吗", 0))
	if got := h.updates(token, 0); len(got) != 0 {
		t.Fatalf("group disabled bot got %+v", got)
	}
	if got := h.updates(otherToken, otherLast+1); len(got) != 0 {
		t.Fatalf("news bot got unmentioned message %+v", got)
	}
	h.trigger(h.message(bob, groupID, true, "/news", 0))
	if got := h.updates(otherToken, otherLast+1); len(got) != 1 || got[0].Message.Command != "/news" {
		t.Fatalf("legacy trigger: %+v", got)
	}

	// 触发与库里的消息不符时跳过。
	err := h.platform.HandleTrigger(context.Background(), &chatbotpb.ChatbotTrigger{MessageId: direct.MessageID, FromUserId: bob, ToId: botID})
	if err != nil || len(h.updates(token, 0)) != 0 {
		t.Fatalf("mismatched trigger: %v", err)
	}
	if err := h.platform.HandleTrigger(context.Background(), &chatbotpb.ChatbotTrigger{MessageId: 1, FromUserId: alice, ToId: bob}); err != ErrInvalidTrigger {
		t.Fatalf("private trigger to non-bot: %v", err)
	}
}

func TestLongPollWakesOnNewUpdatePostgres(t *testing.T) {
	h := newHarness(t)
	botID, token := h.createBot("weather_bot", "天气")
	done := make(chan []Update, 1)
	start := time.Now()
	go func() {
		resp := h.bot(token, http.MethodGet, "/bot/v1/updates?timeout=5", nil)
		var updates []Update
		resp.result(t, &updates)
		done <- updates
	}()
	time.Sleep(200 * time.Millisecond)
	h.trigger(h.message(alice, botID, false, "在吗", 0))
	select {
	case updates := <-done:
		if len(updates) != 1 || time.Since(start) > 2*time.Second {
			t.Fatalf("updates=%+v after %s", updates, time.Since(start))
		}
	case <-time.After(4 * time.Second):
		t.Fatal("long poll not woken")
	}
	if resp := h.bot(token, http.MethodGet, "/bot/v1/updates?timeout=60", nil); resp.status != http.StatusBadRequest {
		t.Fatalf("timeout over limit: %d", resp.status)
	}
}

func TestEventsPostgres(t *testing.T) {
	h := newHarness(t)
	botID, token := h.createBot("weather_bot", "天气")
	ctx := context.Background()
	decision := func() (*friend.RequestMessage, string) {
		t.Helper()
		messages := h.kafka.take("friend-service")
		if len(messages) != 1 {
			t.Fatalf("friend requests = %d", len(messages))
		}
		var request friend.RequestMessage
		if err := proto.Unmarshal(messages[0].envelope.GetPayload(), &request); err != nil {
			t.Fatal(err)
		}
		if request.GetFromKafkaTopic() != "df-shared" || request.GetTargetUserId() != botID || messages[0].key != fmt.Sprintf("b:%d", botID) {
			t.Fatalf("request routing = %+v key %s", &request, messages[0].key)
		}
		if resolve := request.GetResolveFriendRequest(); resolve != nil {
			return &request, resolve.GetDecision().String()
		}
		return &request, request.GetResolveGroupInvitation().GetDecision().String()
	}
	event := func(kind, requestType string, requestID int64) *chatbotpb.BotEvent {
		return &chatbotpb.BotEvent{Type: kind, BotUserId: botID, UserId: alice, GroupId: groupID, RequestId: requestID, RequestType: requestType, OccurredAt: "2026-10-31 10:00:00"}
	}

	if err := h.platform.HandleEvent(ctx, event("relationship_request", db.RequestTypeFriend, 5)); err != nil {
		t.Fatal(err)
	}
	if request, result := decision(); result != "REQUEST_ACCEPT" || request.GetResolveFriendRequest().GetRequestId() != 5 {
		t.Fatalf("friend request: %s", result)
	}
	if err := h.platform.HandleEvent(ctx, event("relationship_request", db.RequestTypeGroupInvite, 6)); err != nil {
		t.Fatal(err)
	}
	if request, result := decision(); result != "REQUEST_ACCEPT" || request.GetResolveGroupInvitation().GetInvitationId() != 6 {
		t.Fatalf("group invite: %s", result)
	}
	h.admin(http.MethodPatch, fmt.Sprintf("/admin/api/bots/%d", botID), map[string]any{"allow_groups": false}).result(t, nil)
	h.platform.HandleEvent(ctx, event("relationship_request", db.RequestTypeGroupInvite, 7))
	if _, result := decision(); result != "REQUEST_REJECT" {
		t.Fatalf("invite with groups off: %s", result)
	}
	h.admin(http.MethodPatch, fmt.Sprintf("/admin/api/bots/%d", botID), map[string]any{"disabled": true}).result(t, nil)
	h.platform.HandleEvent(ctx, event("relationship_request", db.RequestTypeFriend, 8))
	if _, result := decision(); result != "REQUEST_REJECT" {
		t.Fatalf("disabled bot friend request: %s", result)
	}
	h.admin(http.MethodPatch, fmt.Sprintf("/admin/api/bots/%d", botID), map[string]any{"disabled": false}).result(t, nil)

	for _, kind := range []string{"friend_added", "group_joined", "group_joined", "nonsense"} {
		if err := h.platform.HandleEvent(ctx, event(kind, "", 0)); err != nil {
			t.Fatal(err)
		}
	}
	updates := h.updates(token, 0)
	if len(updates) != 2 || updates[0].Event.Type != "friend_added" || updates[0].Event.Chat != nil || updates[0].Event.User.Account != "alice" ||
		updates[1].Event.Chat == nil || updates[1].Event.Chat.Title != "测试群" {
		t.Fatalf("event updates = %+v", updates)
	}
	// 内置机器人与未登记的机器人不收事件更新。
	for _, id := range []int64{db.BotUserIDMin, db.BotUserIDMax} {
		if err := h.platform.HandleEvent(ctx, &chatbotpb.BotEvent{Type: "friend_added", BotUserId: id, UserId: alice}); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.platform.HandleEvent(ctx, &chatbotpb.BotEvent{Type: "relationship_request", BotUserId: botID, UserId: alice}); err != ErrInvalidTrigger {
		t.Fatalf("missing request id: %v", err)
	}
}

func uploadFile(h *harness, token, name string, content []byte) response {
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, _ := writer.CreateFormFile("file", name)
	part.Write(content)
	writer.Close()
	return h.do(http.MethodPost, "/bot/v1/files", map[string]string{"Authorization": "Bearer " + token}, writer.FormDataContentType(), &body)
}

func TestSendAndFilesPostgres(t *testing.T) {
	h := newHarness(t)
	botID, token := h.createBot("weather_bot", "天气")
	send := func(request map[string]any) response {
		return h.bot(token, http.MethodPost, "/bot/v1/messages", request)
	}

	if resp := send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "你好"}); resp.status != http.StatusForbidden || resp.errorCode(t) != "not_friend" {
		t.Fatalf("send to stranger: %d %s", resp.status, resp.body)
	}
	h.exec("INSERT INTO friends (user_id, friend_id, is_delete) VALUES (?, ?, FALSE), (?, ?, FALSE)", botID, alice, alice, botID)
	var sent sentMessage
	send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "你好", "client_message_id": "weather-1"}).result(t, &sent)
	if sent.MessageID <= 0 || sent.Pending || sent.ClientMessageID != "weather-1" {
		t.Fatalf("sent = %+v", sent)
	}
	var again sentMessage
	send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "你好", "client_message_id": "weather-1"}).result(t, &again)
	if again.MessageID != sent.MessageID || len(h.kafka.take("storage-service")) != 1 {
		t.Fatalf("retry sent twice: %+v", again)
	}
	stored, _ := NewStore(h.database).Message(context.Background(), sent.MessageID)
	if stored.FromUserID != botID || stored.ToUserID != alice || stored.Content != "你好" {
		t.Fatalf("stored = %+v", stored)
	}

	// 引用：本会话的消息可以，别的会话不行。
	incoming := h.message(alice, botID, false, "问题", 0)
	send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "回答", "reply_to_message_id": incoming.MessageID}).result(t, nil)
	elsewhere := h.message(bob, alice, false, "私事", 0)
	if resp := send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "偷看", "reply_to_message_id": elsewhere.MessageID}); resp.errorCode(t) != "invalid_reply" {
		t.Fatal("reply to other conversation accepted")
	}

	// 群：必须是成员。
	if resp := send(map[string]any{"chat_id": groupID, "chat_type": "group", "text": "大家好"}); resp.errorCode(t) != "not_member" {
		t.Fatal("non-member group send accepted")
	}
	h.exec("INSERT INTO group_members (group_id, user_id, role, joined_at) VALUES (?, ?, 'member', '2000-01-01 00:00:00')", groupID, botID)
	send(map[string]any{"chat_id": groupID, "chat_type": "group", "text": "大家好"}).result(t, &sent)
	if sent.Chat.Type != ChatGroup || sent.MessageID <= 0 {
		t.Fatalf("group sent = %+v", sent)
	}

	// 发送超时只返回pending。
	h.platform.config.SendWait = 0
	h.platform.publish = func(context.Context, string, string, []byte) error { return nil }
	resp := send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "慢"})
	if resp.status != http.StatusAccepted {
		t.Fatalf("pending send: %d %s", resp.status, resp.body)
	}
	h.platform.publish = func(context.Context, string, string, []byte) error { return fmt.Errorf("kafka down") }
	if resp := send(map[string]any{"chat_id": alice, "chat_type": "private", "text": "挂了"}); resp.status != http.StatusServiceUnavailable || resp.header.Get("Retry-After") == "" {
		t.Fatalf("kafka down: %d", resp.status)
	}
	h.platform.config.SendWait, h.platform.publish = 2*time.Second, h.kafka.publish

	// 上传、发送、下载文件。
	content := []byte("weather report")
	sum := sha512.Sum512(content)
	hash := hex.EncodeToString(sum[:])
	var uploaded uploadedFile
	uploadFile(h, token, "report.TXT", content).result(t, &uploaded)
	if uploaded.FileID != hash+".txt" || uploaded.FileName != "report.TXT" || uploaded.Size != int64(len(content)) {
		t.Fatalf("uploaded = %+v", uploaded)
	}
	send(map[string]any{"chat_id": alice, "chat_type": "private", "file_id": uploaded.FileID, "file_name": "report.txt"}).result(t, &sent)
	stored, _ = NewStore(h.database).Message(context.Background(), sent.MessageID)
	if stored.MessageType != "file" || stored.Content != uploaded.FileID || stored.RealFileName != "report.txt" {
		t.Fatalf("file message = %+v", stored)
	}
	download := h.do(http.MethodGet, "/bot/v1/files/"+uploaded.FileID, map[string]string{"Authorization": "Bearer " + token}, "", nil)
	if download.status != http.StatusOK || !bytes.Equal(download.body, content) {
		t.Fatalf("download: %d %q", download.status, download.body)
	}
	// 别的机器人看不到这个文件，也不能拿它发消息；上传同样内容后获得使用权。
	_, otherToken := h.createBot("news_bot", "新闻")
	if resp := h.do(http.MethodGet, "/bot/v1/files/"+hash, map[string]string{"Authorization": "Bearer " + otherToken}, "", nil); resp.status != http.StatusNotFound {
		t.Fatalf("other bot download: %d", resp.status)
	}
	uploadFile(h, otherToken, "copy.txt", content).result(t, nil)
	if resp := h.do(http.MethodGet, "/bot/v1/files/"+hash, map[string]string{"Authorization": "Bearer " + otherToken}, "", nil); resp.status != http.StatusOK {
		t.Fatalf("other bot after upload: %d", resp.status)
	}
	strangerHash := strings.Repeat("c", 128)
	if resp := send(map[string]any{"chat_id": alice, "chat_type": "private", "type": "image", "file_id": strangerHash + ".png"}); resp.errorCode(t) != "file_not_owned" {
		t.Fatal("unowned file accepted")
	}
	if resp := uploadFile(h, token, "big.bin", bytes.Repeat([]byte("x"), 1<<20+1)); resp.status != http.StatusRequestEntityTooLarge {
		t.Fatalf("oversized upload: %d", resp.status)
	}
	if resp := uploadFile(h, token, "empty.txt", nil); resp.errorCode(t) != "empty_file" {
		t.Fatal("empty upload accepted")
	}

	// 收到的文件消息里的文件可以下载。
	incomingHash := strings.Repeat("d", 128)
	if err := db.StoreVerifiedFileWithDB(h.database, incomingHash, 3, StoragePath(incomingHash), alice); err != nil {
		t.Fatal(err)
	}
	h.platform.objects.Put(context.Background(), StoragePath(incomingHash), bytes.NewReader([]byte("img")), 3)
	if _, _, err := db.StoreNewMessageWithDB(h.database, alice, botID, incomingHash+".png", "image", "", false, "", "看图", 0); err != nil {
		t.Fatal(err)
	}
	if resp := h.do(http.MethodGet, "/bot/v1/files/"+incomingHash+".png", map[string]string{"Authorization": "Bearer " + token}, "", nil); resp.status != http.StatusOK || string(resp.body) != "img" {
		t.Fatalf("received file download: %d", resp.status)
	}

	h.platform.objects = nil
	if resp := uploadFile(h, token, "a.txt", content); resp.status != http.StatusServiceUnavailable || resp.errorCode(t) != "files_disabled" {
		t.Fatalf("files disabled: %d", resp.status)
	}
}

func TestWebhookDeliveryPostgres(t *testing.T) {
	h := newHarness(t)
	botID, token := h.createBot("weather_bot", "天气")
	var mu sync.Mutex
	var received []Update
	status := http.StatusOK
	hook := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		timestamp := r.Header.Get(timestampHeader)
		if r.Header.Get(signatureHeader) != Sign("hook-secret", timestamp, body) {
			t.Errorf("bad signature for %s", body)
		}
		mu.Lock()
		defer mu.Unlock()
		if status == http.StatusOK {
			var update Update
			json.Unmarshal(body, &update)
			received = append(received, update)
		}
		w.WriteHeader(status)
	}))
	defer hook.Close()

	if resp := h.bot(token, http.MethodPut, "/bot/v1/webhook", map[string]any{"url": "ftp://x", "secret": ""}); resp.errorCode(t) != "invalid_url" {
		t.Fatal("invalid webhook accepted")
	}
	h.bot(token, http.MethodPut, "/bot/v1/webhook", map[string]any{"url": hook.URL + "/hook", "secret": "hook-secret"}).result(t, nil)
	if resp := h.bot(token, http.MethodGet, "/bot/v1/updates", nil); resp.status != http.StatusConflict {
		t.Fatalf("getUpdates with webhook: %d", resp.status)
	}

	dispatcher := NewDispatcher(h.platform)
	ctx := context.Background()
	if worked, err := dispatcher.deliverOne(ctx); worked || err != nil {
		t.Fatalf("nothing to deliver: %v %v", worked, err)
	}
	h.trigger(h.message(alice, botID, false, "一", 0))
	h.trigger(h.message(alice, botID, false, "二", 0))
	select {
	case <-dispatcher.wake:
	default:
		t.Fatal("queued update did not wake dispatcher")
	}
	if worked, err := dispatcher.deliverOne(ctx); !worked || err != nil {
		t.Fatalf("deliver: %v %v", worked, err)
	}
	if len(received) != 2 || received[0].Message.Text != "一" || received[1].Message.Text != "二" || received[0].UpdateID >= received[1].UpdateID {
		t.Fatalf("received = %+v", received)
	}
	if worked, _ := dispatcher.deliverOne(ctx); worked {
		t.Fatal("acked updates delivered again")
	}

	mu.Lock()
	status = http.StatusInternalServerError
	mu.Unlock()
	h.trigger(h.message(alice, botID, false, "三", 0))
	if worked, err := dispatcher.deliverOne(ctx); !worked || err != nil {
		t.Fatalf("failed delivery: %v %v", worked, err)
	}
	var info webhookInfo
	h.bot(token, http.MethodGet, "/bot/v1/webhook", nil).result(t, &info)
	if info.Failures != 1 || !strings.Contains(info.LastError, "500") || info.NextRetry == nil || !info.NextRetry.After(time.Now()) || !info.HasSecret {
		t.Fatalf("webhook info = %+v", info)
	}
	if worked, _ := dispatcher.deliverOne(ctx); worked {
		t.Fatal("retried before backoff")
	}
	// 重新设置webhook立即重试。
	mu.Lock()
	status = http.StatusOK
	mu.Unlock()
	h.bot(token, http.MethodPut, "/bot/v1/webhook", map[string]any{"url": hook.URL + "/hook", "secret": "hook-secret"}).result(t, nil)
	if worked, err := dispatcher.deliverOne(ctx); !worked || err != nil || len(received) != 3 || received[2].Message.Text != "三" {
		t.Fatalf("redelivery: %v %v %+v", worked, err, received)
	}

	// 删除webhook后回到长轮询；过期更新被清理。
	h.bot(token, http.MethodDelete, "/bot/v1/webhook", nil).result(t, nil)
	h.trigger(h.message(alice, botID, false, "四", 0))
	if updates := h.updates(token, 0); len(updates) != 1 {
		t.Fatalf("after delete webhook: %+v", updates)
	}
	h.exec("UPDATE bot_updates SET created_at = now() - interval '2 hours'")
	if deleted, err := NewStore(h.database).DeleteUpdatesBefore(ctx, time.Now().Add(-time.Hour)); err != nil || deleted != 4 {
		t.Fatalf("cleanup deleted %d: %v", deleted, err)
	}
}
