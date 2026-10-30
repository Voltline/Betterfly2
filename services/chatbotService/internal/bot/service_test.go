package bot

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	chatbotpb "Betterfly2/proto/chatbot"
	envelope "Betterfly2/proto/envelope"
	storage "Betterfly2/proto/storage"
	"Betterfly2/shared/db"
	"chatbotService/internal/config"
	"chatbotService/internal/llm"

	"google.golang.org/protobuf/proto"
)

const (
	testBot   = int64(900000000000000002)
	testAlice = int64(10)
	testBob   = int64(11)
	testGroup = int64(500)
)

var testNow = time.Date(2026, 10, 30, 12, 0, 0, 0, time.UTC)

type fakeStore struct {
	messages  map[int64]*db.Message
	history   []db.Message
	canPost   bool
	replies   map[string]bool
	names     map[int64]string
	groupName string
	err       error
}

func (s *fakeStore) Message(_ context.Context, id int64) (*db.Message, error) {
	return s.messages[id], s.err
}
func (s *fakeStore) CanPublishGroup(context.Context, int64, int64) (bool, error) {
	return s.canPost, s.err
}
func (s *fakeStore) ReplyExists(_ context.Context, _ int64, id string) (bool, error) {
	return s.replies[id], s.err
}
func (s *fakeStore) DirectHistory(context.Context, int64, int64, int64, int) ([]db.Message, error) {
	return s.history, s.err
}
func (s *fakeStore) GroupHistory(context.Context, int64, int64, int) ([]db.Message, error) {
	return s.history, s.err
}
func (s *fakeStore) UserNames(context.Context, []int64) (map[int64]string, error) {
	return s.names, s.err
}
func (s *fakeStore) GroupName(context.Context, int64) (string, error) { return s.groupName, s.err }

type fakeCompleter struct {
	reply  string
	err    error
	prompt []llm.Message
}

func (c *fakeCompleter) Complete(_ context.Context, messages []llm.Message) (string, error) {
	c.prompt = messages
	return c.reply, c.err
}

type published struct {
	topic, key string
	request    *storage.RequestMessage
}

func testService(t *testing.T, store *fakeStore, completer Completer) (*Service, *[]published) {
	t.Helper()
	cfg := config.Config{
		BotUserID: testBot, Name: "Betterfly 助手", StorageTopic: "storage-service", SystemPrompt: "你是助手。",
		Location: time.UTC, ContextMessages: 20, ContextChars: 6000, MaxReplyParts: 4,
		UserRatePerMinute: 6, UserDailyLimit: 200, MaxTriggerAge: 15 * time.Minute,
	}
	var sent []published
	service := NewService(cfg, store, completer, func(_ context.Context, topic, key string, payload []byte) error {
		var env envelope.Envelope
		if err := proto.Unmarshal(payload, &env); err != nil || env.GetType() != envelope.MessageType_STORAGE_REQUEST {
			t.Fatalf("bad envelope: %v %v", env.GetType(), err)
		}
		var request storage.RequestMessage
		if err := proto.Unmarshal(env.GetPayload(), &request); err != nil {
			t.Fatal(err)
		}
		sent = append(sent, published{topic: topic, key: key, request: &request})
		return nil
	})
	service.now = func() time.Time { return testNow }
	return service, &sent
}

func directMessage(id int64, content string) *db.Message {
	return &db.Message{MessageID: id, FromUserID: testAlice, ToUserID: testBot, Content: content, MessageType: "text", Timestamp: testNow.Add(-time.Second).Format(time.RFC3339)}
}

func directTrigger(id int64) *chatbotpb.ChatbotTrigger {
	return &chatbotpb.ChatbotTrigger{MessageId: id, FromUserId: testAlice, ToId: testBot, ReplyTopic: "df-1"}
}

func TestHandleDirectReplyPublishesStorageRequest(t *testing.T) {
	question := directMessage(5, "今天星期几？")
	store := &fakeStore{
		messages: map[int64]*db.Message{5: question},
		history: []db.Message{*question,
			{MessageID: 3, FromUserID: testBot, ToUserID: testAlice, Content: "你好呀", MessageType: "text"},
			{MessageID: 2, FromUserID: testAlice, ToUserID: testBot, Content: "在吗", MessageType: "text"},
			{MessageID: 1, FromUserID: testAlice, ToUserID: testBot, Content: "你好", MessageType: "text"},
			{MessageID: 0, FromUserID: testBot, ToUserID: testAlice, Content: "最早的机器人消息", MessageType: "text"},
		},
	}
	completer := &fakeCompleter{reply: "<think>算一下</think>今天是星期五。"}
	service, sent := testService(t, store, completer)
	if err := service.Handle(context.Background(), directTrigger(5)); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 1 {
		t.Fatalf("published %d", len(*sent))
	}
	got := (*sent)[0]
	stored := got.request.GetStoreNewMessage()
	if got.topic != "storage-service" || got.key != "u:10" || got.request.GetFromKafkaTopic() != "df-1" || got.request.GetTargetUserId() != testBot {
		t.Fatalf("routing = %+v", got)
	}
	if stored.GetFromUserId() != testBot || stored.GetToUserId() != testAlice || stored.GetIsGroup() || stored.GetMessageType() != "text" ||
		stored.GetContent() != "今天是星期五。" || stored.GetClientMessageId() != "chatbot:5:0" || stored.GetReplyToMessageId() != 0 {
		t.Fatalf("stored = %+v", stored)
	}
	// 开头的机器人消息被丢弃，连续的用户消息合并，时间写进system。
	roles := make([]string, 0, len(completer.prompt))
	for _, message := range completer.prompt {
		roles = append(roles, message.Role)
	}
	if strings.Join(roles, ",") != "system,user,assistant,user" {
		t.Fatalf("roles = %v", roles)
	}
	if completer.prompt[1].Content != "你好\n在吗" || !strings.Contains(completer.prompt[0].Content, "2026-10-30 12:00 Friday") {
		t.Fatalf("prompt = %+v", completer.prompt)
	}
}

func TestHandleGroupReplyQuotesTriggerAndNamesSpeakers(t *testing.T) {
	question := &db.Message{MessageID: 8, FromUserID: testBob, ToUserID: testGroup, IsGroup: true, Content: "@Betterfly 助手 总结一下", MessageType: "text", Timestamp: testNow.Format(time.RFC3339)}
	store := &fakeStore{
		messages: map[int64]*db.Message{8: question}, canPost: true,
		history: []db.Message{*question, {MessageID: 6, FromUserID: testAlice, ToUserID: testGroup, IsGroup: true, Content: "周五聚餐", MessageType: "text"}},
		names:   map[int64]string{testBob: "Bob"}, groupName: "饭搭子",
	}
	completer := &fakeCompleter{reply: "Alice提议周五聚餐。"}
	service, sent := testService(t, store, completer)
	trigger := &chatbotpb.ChatbotTrigger{MessageId: 8, FromUserId: testBob, ToId: testGroup, IsGroup: true, ReplyTopic: "df-2"}
	if err := service.Handle(context.Background(), trigger); err != nil {
		t.Fatal(err)
	}
	stored := (*sent)[0].request.GetStoreNewMessage()
	if (*sent)[0].key != "g:500" || stored.GetToUserId() != testGroup || !stored.GetIsGroup() || stored.GetReplyToMessageId() != 8 {
		t.Fatalf("group reply = %+v key=%s", stored, (*sent)[0].key)
	}
	if len(completer.prompt) != 2 || completer.prompt[1].Content != "用户10：周五聚餐\nBob：@Betterfly 助手 总结一下" ||
		!strings.Contains(completer.prompt[0].Content, "饭搭子") {
		t.Fatalf("prompt = %+v", completer.prompt)
	}
}

func TestHandleLongReplySplitsIntoOrderedParts(t *testing.T) {
	store := &fakeStore{messages: map[int64]*db.Message{5: directMessage(5, "写长一点")}}
	service, sent := testService(t, store, &fakeCompleter{reply: strings.Repeat("长", 1500)})
	if err := service.Handle(context.Background(), directTrigger(5)); err != nil {
		t.Fatal(err)
	}
	if len(*sent) != 3 {
		t.Fatalf("parts = %d", len(*sent))
	}
	for i, item := range *sent {
		if id := item.request.GetStoreNewMessage().GetClientMessageId(); id != clientMessageID(5, i) || item.key != "u:10" {
			t.Fatalf("part %d id=%s key=%s", i, id, item.key)
		}
	}
}

func TestHandleSkipsWithoutReplying(t *testing.T) {
	recalled := directMessage(5, "撤回了")
	recalled.IsRecalled = true
	stale := directMessage(5, "很久以前")
	stale.Timestamp = testNow.Add(-time.Hour).Format(time.RFC3339)
	groupMessage := &db.Message{MessageID: 5, FromUserID: testAlice, ToUserID: testGroup, IsGroup: true, MessageType: "text", Timestamp: testNow.Format(time.RFC3339)}
	cases := map[string]struct {
		store   *fakeStore
		trigger *chatbotpb.ChatbotTrigger
	}{
		"missing":   {&fakeStore{}, directTrigger(5)},
		"mismatch":  {&fakeStore{messages: map[int64]*db.Message{5: {MessageID: 5, FromUserID: testBob, ToUserID: testBot}}}, directTrigger(5)},
		"recalled":  {&fakeStore{messages: map[int64]*db.Message{5: recalled}}, directTrigger(5)},
		"stale":     {&fakeStore{messages: map[int64]*db.Message{5: stale}}, directTrigger(5)},
		"duplicate": {&fakeStore{messages: map[int64]*db.Message{5: directMessage(5, "hi")}, replies: map[string]bool{"chatbot:5:0": true}}, directTrigger(5)},
		"from bot":  {&fakeStore{}, &chatbotpb.ChatbotTrigger{MessageId: 5, FromUserId: testBot, ToId: testGroup, IsGroup: true, ReplyTopic: "df"}},
		"not member": {&fakeStore{messages: map[int64]*db.Message{5: groupMessage}},
			&chatbotpb.ChatbotTrigger{MessageId: 5, FromUserId: testAlice, ToId: testGroup, IsGroup: true, ReplyTopic: "df"}},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			completer := &fakeCompleter{reply: "不应调用"}
			service, sent := testService(t, tc.store, completer)
			if err := service.Handle(context.Background(), tc.trigger); err != nil {
				t.Fatal(err)
			}
			if len(*sent) != 0 || completer.prompt != nil {
				t.Fatalf("replied: %d prompt=%v", len(*sent), completer.prompt != nil)
			}
		})
	}
}

func TestHandleRejectsInvalidTrigger(t *testing.T) {
	service, _ := testService(t, &fakeStore{}, nil)
	for _, trigger := range []*chatbotpb.ChatbotTrigger{
		{MessageId: 0, FromUserId: testAlice, ToId: testBot, ReplyTopic: "df"},
		{MessageId: 1, FromUserId: testAlice, ToId: testBot},
		{MessageId: 1, FromUserId: testAlice, ToId: testBob, ReplyTopic: "df"},
	} {
		if err := service.Handle(context.Background(), trigger); !errors.Is(err, ErrInvalidTrigger) {
			t.Fatalf("trigger %+v err = %v", trigger, err)
		}
	}
}

func TestHandleFallbackReplies(t *testing.T) {
	image := directMessage(5, "hash")
	image.MessageType = "image"
	cases := map[string]struct {
		message   *db.Message
		completer Completer
		want      string
	}{
		"unconfigured": {directMessage(5, "hi"), nil, replyNotConfigured},
		"non-text":     {image, &fakeCompleter{reply: "x"}, replyTextOnly},
		"llm error":    {directMessage(5, "hi"), &fakeCompleter{err: errors.New("HTTP 500")}, replyFailed},
		"empty":        {directMessage(5, "hi"), &fakeCompleter{reply: "<think>…</think>"}, replyEmpty},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			service, sent := testService(t, &fakeStore{messages: map[int64]*db.Message{5: tc.message}}, tc.completer)
			if err := service.Handle(context.Background(), directTrigger(5)); err != nil {
				t.Fatal(err)
			}
			if len(*sent) != 1 || (*sent)[0].request.GetStoreNewMessage().GetContent() != tc.want {
				t.Fatalf("sent = %+v", *sent)
			}
		})
	}
}

func TestHandleReturnsErrorWhenCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	store := &fakeStore{messages: map[int64]*db.Message{5: directMessage(5, "hi")}}
	service, sent := testService(t, store, &fakeCompleter{err: context.Canceled})
	if err := service.Handle(ctx, directTrigger(5)); !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v", err)
	}
	if len(*sent) != 0 {
		t.Fatal("cancelled trigger must not reply")
	}
	store.err = errors.New("db down")
	if err := service.Handle(context.Background(), directTrigger(5)); err == nil {
		t.Fatal("store error must be retried")
	}
}

func TestHandleRateLimitNotifiesOnceThenStaysSilent(t *testing.T) {
	store := &fakeStore{messages: map[int64]*db.Message{5: directMessage(5, "hi")}}
	service, sent := testService(t, store, &fakeCompleter{reply: "好"})
	service.limiter = newLimiter(1, 0, time.UTC)
	for i := 0; i < 3; i++ {
		if err := service.Handle(context.Background(), directTrigger(5)); err != nil {
			t.Fatal(err)
		}
	}
	if len(*sent) != 2 || (*sent)[1].request.GetStoreNewMessage().GetContent() != replyMinuteLimit {
		t.Fatalf("sent = %d", len(*sent))
	}
}
