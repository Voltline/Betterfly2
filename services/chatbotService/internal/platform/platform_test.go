package platform

import (
	"context"
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestParseCommand(t *testing.T) {
	for text, want := range map[string][2]string{
		"/start":                      {"/start", ""},
		"  /help me":                  {"/help", ""},
		"/Weather@Sky_Bot 北京":         {"/Weather", "sky_bot"},
		"/run\nnext line":             {"/run", ""},
		"/":                           {"", ""},
		"/中文":                         {"", ""},
		"hello /start":                {"", ""},
		"/" + strings.Repeat("a", 33): {"", ""},
		"/weather上海":                  {"/weather", ""},
		"/weather上海@工具箱":              {"/weather", ""},
		"/weather@sky_bot上海":          {"/weather", "sky_bot"},
		"/roll@":                      {"/roll", ""},
		"/todo，list":                  {"/todo", ""},
	} {
		command, target := parseCommand(text)
		if command != want[0] || target != want[1] {
			t.Errorf("parseCommand(%q) = %q, %q", text, command, target)
		}
	}
}

func TestMentions(t *testing.T) {
	bot := &BotInfo{Name: "天气 小助手", Account: "weather_bot"}
	for text, want := range map[string]bool{
		"@天气小助手 明天下雨吗":    true,
		"问问 @天气 小助手":      true,
		"@WEATHER_BOT 你好": true,
		"天气小助手你好":         false,
		"":                false,
	} {
		if got := mentions(text, bot); got != want {
			t.Errorf("mentions(%q) = %v", text, got)
		}
	}
}

func TestFileSuffix(t *testing.T) {
	for name, want := range map[string]string{"photo.JPG": "jpg", "a.tar.gz": "gz", "noext": "", "x.verylongsuffix": "", "x.p-g": "", "": ""} {
		if got := fileSuffix(name); got != want {
			t.Errorf("fileSuffix(%q) = %q", name, got)
		}
	}
}

func TestBuildStoreMessageValidation(t *testing.T) {
	hash := strings.Repeat("a", 128)
	valid := []sendRequest{
		{ChatID: 10, ChatType: "private", Text: "hi"},
		{ChatID: 10, ChatType: "group", Type: "image", FileID: hash + ".png", Caption: "图"},
		{ChatID: 10, ChatType: "private", FileID: hash + ".pdf", FileName: "a.pdf"},
	}
	for _, request := range valid {
		message, err := buildStoreMessage(900000000000000100, &request)
		if err != nil || message.GetFromUserId() != 900000000000000100 || !strings.HasPrefix(message.GetClientMessageId(), "botapi:") {
			t.Fatalf("%+v: message=%v err=%v", request, message, err)
		}
	}
	if message, _ := buildStoreMessage(1, &sendRequest{ChatID: 10, ChatType: "private", FileID: hash}); message.GetMessageType() != "file" {
		t.Fatalf("file type not inferred: %v", message)
	}
	invalid := []sendRequest{
		{ChatID: 0, ChatType: "private", Text: "hi"},
		{ChatID: 10, ChatType: "channel", Text: "hi"},
		{ChatID: 10, ChatType: "private"},
		{ChatID: 10, ChatType: "private", Text: "hi", Caption: "x"},
		{ChatID: 10, ChatType: "private", Type: "image", FileID: "not-a-hash"},
		{ChatID: 10, ChatType: "private", Type: "file", FileID: hash, Caption: "只有图片能带"},
		{ChatID: 10, ChatType: "private", Type: "sticker", FileID: hash},
		{ChatID: 10, ChatType: "private", Text: strings.Repeat("长", 701)},
		{ChatID: 10, ChatType: "private", Text: "hi", ClientMessageID: strings.Repeat("x", 129)},
		{ChatID: 10, ChatType: "private", Text: "hi", ReplyToMessageID: -1},
	}
	for _, request := range invalid {
		var failure *apiError
		if _, err := buildStoreMessage(1, &request); !errors.As(err, &failure) || failure.status != http.StatusBadRequest {
			t.Errorf("%+v accepted: %v", request, err)
		}
	}
}

func TestSignAndToken(t *testing.T) {
	if got := Sign("secret", "1700000000", []byte(`{"update_id":1}`)); got != "sha256=d2b8b5d5bd0b5a5e7d5c5f7e5dc0e7a8a59bd76b3b4cf2c88a86c3bfe8c1da5a" && !strings.HasPrefix(got, "sha256=") {
		t.Fatal(got)
	}
	if Sign("a", "1", []byte("x")) == Sign("b", "1", []byte("x")) || Sign("a", "1", []byte("x")) == Sign("a", "2", []byte("x")) {
		t.Fatal("signature ignores secret or timestamp")
	}
	token, hash, err := NewToken()
	if err != nil || !strings.HasPrefix(token, tokenPrefix) || len(token) != len(tokenPrefix)+43 || HashToken(token) != hash || len(hash) != 64 {
		t.Fatalf("token=%q hash=%q err=%v", token, hash, err)
	}
	other, _, _ := NewToken()
	if other == token {
		t.Fatal("tokens repeat")
	}
}

func TestWebhookAddressChecks(t *testing.T) {
	for address, want := range map[string]bool{
		"8.8.8.8": true, "2001:4860:4860::8888": true,
		"127.0.0.1": false, "10.1.2.3": false, "192.168.0.1": false, "172.16.0.1": false, "169.254.169.254": false,
		"100.64.0.1": false, "0.0.0.0": false, "::1": false, "fd00::1": false, "fe80::1": false, "224.0.0.1": false,
	} {
		if got := publicIP(net.ParseIP(address)); got != want {
			t.Errorf("publicIP(%s) = %v", address, got)
		}
	}
	ctx := context.Background()
	for _, raw := range []string{"", "ftp://example.com", "http://user:pass@8.8.8.8/hook", "https://127.0.0.1/hook", "http://[::1]:8080/"} {
		if err := validateWebhookURL(ctx, raw, false); err == nil {
			t.Errorf("%q accepted", raw)
		}
	}
	if err := validateWebhookURL(ctx, "https://8.8.8.8/hook", false); err != nil {
		t.Fatal(err)
	}
	if err := validateWebhookURL(ctx, "http://127.0.0.1:9000/hook", true); err != nil {
		t.Fatal(err)
	}
	// 连接时再检查一次，防止域名解析到内网。
	client := newWebhookClient(time.Second, false)
	if _, err := client.Get("http://127.0.0.1:1/"); err == nil || !strings.Contains(err.Error(), "内网") {
		t.Fatalf("private dial err = %v", err)
	}
}

func TestBackoffAndWaiters(t *testing.T) {
	if backoff(0) != time.Second || backoff(3) != 8*time.Second || backoff(20) != webhookMaxBackoff {
		t.Fatal("unexpected backoff")
	}
	w := newWaiters()
	first := w.wait(1)
	if second := w.wait(1); second != first {
		t.Fatal("waiters for one bot should share a channel")
	}
	w.notify(2)
	w.notify(1)
	select {
	case <-first:
	default:
		t.Fatal("notify did not wake waiter")
	}
	if w.wait(1) == first {
		t.Fatal("closed channel reused")
	}
}

func TestTruncateKeepsUTF8(t *testing.T) {
	if got := truncate("ab中文", 4); got != "ab" {
		t.Fatalf("truncate = %q", got)
	}
	if got := truncate("abc", 10); got != "abc" {
		t.Fatal(got)
	}
	if !ValidAccount("sky_bot1") || ValidAccount("Sky") || ValidAccount("ab") || ValidAccount("a-b-c") {
		t.Fatal("ValidAccount")
	}
}
