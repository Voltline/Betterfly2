package llm

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func testClient(url string, retries int) *Client {
	client := New(Config{BaseURL: url, APIKey: "test-key", Model: "test-model", MaxTokens: 64, Timeout: 5 * time.Second, MaxRetries: retries})
	client.backoff = func(int) time.Duration { return time.Millisecond }
	return client
}

func TestCompleteSendsOpenAIRequest(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/chat/completions" || r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("path=%s auth=%s", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body["model"] != "test-model" || body["max_tokens"] != float64(64) || body["stream"] != false || body["temperature"] != nil {
			t.Errorf("body = %v", body)
		}
		messages := body["messages"].([]any)
		if len(messages) != 2 || messages[1].(map[string]any)["content"] != "你好" {
			t.Errorf("messages = %v", messages)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"你好！"},"finish_reason":"stop"}]}`))
	}))
	defer server.Close()
	reply, err := testClient(server.URL+"/v1", 0).Complete(context.Background(), []Message{{Role: "system", Content: "s"}, {Role: "user", Content: "你好"}})
	if err != nil || reply != "你好！" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
}

func TestCompleteRetriesRateLimitAndServerErrors(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch calls.Add(1) {
		case 1:
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusTooManyRequests)
		case 2:
			w.WriteHeader(http.StatusBadGateway)
		default:
			_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"ok"}}]}`))
		}
	}))
	defer server.Close()
	reply, err := testClient(server.URL, 2).Complete(context.Background(), nil)
	if err != nil || reply != "ok" || calls.Load() != 3 {
		t.Fatalf("reply=%q err=%v calls=%d", reply, err, calls.Load())
	}
}

func TestCompleteDoesNotRetryClientErrorsOrInvalidResponses(t *testing.T) {
	for name, handler := range map[string]http.HandlerFunc{
		"401": func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, `{"error":"secret detail"}`, http.StatusUnauthorized)
		},
		"bad json":   func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("not json")) },
		"no choices": func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"choices":[]}`)) },
	} {
		t.Run(name, func(t *testing.T) {
			var calls atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls.Add(1); handler(w, r) }))
			defer server.Close()
			_, err := testClient(server.URL, 3).Complete(context.Background(), nil)
			if err == nil || calls.Load() != 1 || strings.Contains(err.Error(), "secret") {
				t.Fatalf("err=%v calls=%d", err, calls.Load())
			}
		})
	}
}

func TestCompleteGivesUpAfterRetriesAndHonorsCancel(t *testing.T) {
	var calls atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		calls.Add(1)
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	_, err := testClient(server.URL, 2).Complete(context.Background(), nil)
	var statusErr *StatusError
	if !errors.As(err, &statusErr) || statusErr.StatusCode != 503 || calls.Load() != 3 {
		t.Fatalf("err=%v calls=%d", err, calls.Load())
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := testClient(server.URL, 2).Complete(ctx, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled err = %v", err)
	}
}

func TestParseRetryAfter(t *testing.T) {
	if got := parseRetryAfter("3"); got != 3*time.Second {
		t.Fatalf("seconds = %v", got)
	}
	if got := parseRetryAfter("3600"); got != maxRetryAfter {
		t.Fatalf("capped = %v", got)
	}
	if got := parseRetryAfter("nonsense"); got != 0 {
		t.Fatalf("invalid = %v", got)
	}
}

func TestCompleteSendsImagePartsToVisionModel(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model    string `json:"model"`
			Messages []struct {
				Role    string          `json:"role"`
				Content json.RawMessage `json:"content"`
			} `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Error(err)
		}
		if body.Model != "vision-model" || string(body.Messages[0].Content) != `"s"` ||
			string(body.Messages[1].Content) != `[{"type":"text","text":"这是什么"},{"type":"image_url","image_url":{"url":"data:image/jpeg;base64,AA=="}}]` {
			t.Errorf("body = %+v", body)
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"一只猫"}}]}`))
	}))
	defer server.Close()
	client := testClient(server.URL, 0)
	client.config.VisionModel = "vision-model"
	messages := []Message{{Role: "system", Content: "s"}, {Role: "user", Parts: []Part{{Text: "这是什么"}, {ImageURL: "data:image/jpeg;base64,AA=="}}}}
	if reply, err := client.Complete(context.Background(), messages); err != nil || reply != "一只猫" {
		t.Fatalf("reply=%q err=%v", reply, err)
	}
	if !HasImages(messages) {
		t.Fatal("images not detected")
	}
	plain := WithoutImages(messages, "[图片]")
	if HasImages(plain) || plain[1].Content != "这是什么\n[图片]" || plain[0].Content != "s" || messages[1].Parts == nil {
		t.Fatalf("plain = %+v", plain)
	}
}

func TestStatusErrorRejectsInput(t *testing.T) {
	for status, want := range map[int]bool{400: true, 413: true, 415: true, 422: true, 401: false, 429: false, 500: false} {
		if got := (&StatusError{StatusCode: status}).RejectsInput(); got != want {
			t.Fatalf("%d RejectsInput = %v", status, got)
		}
	}
}
