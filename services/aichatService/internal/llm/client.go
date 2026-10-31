package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const (
	maxResponseBytes = 1 << 20
	maxRetryAfter    = 10 * time.Second
)

// Message 是OpenAI Chat Completions格式的一条消息，role为system、user或assistant。
// Parts非空时按多模态格式发送，文字与图片按顺序排列，Content被忽略。
type Message struct {
	Role    string
	Content string
	Parts   []Part
}

// Part 是多模态消息的一段：Text与ImageURL二选一，ImageURL通常是data:image/jpeg;base64,...。
type Part struct {
	Text     string
	ImageURL string
}

type wirePart struct {
	Type     string   `json:"type"`
	Text     string   `json:"text,omitempty"`
	ImageURL *wireURL `json:"image_url,omitempty"`
}

type wireURL struct {
	URL string `json:"url"`
}

func (m Message) MarshalJSON() ([]byte, error) {
	if len(m.Parts) == 0 {
		return json.Marshal(struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		}{m.Role, m.Content})
	}
	parts := make([]wirePart, 0, len(m.Parts))
	for _, part := range m.Parts {
		if part.ImageURL != "" {
			parts = append(parts, wirePart{Type: "image_url", ImageURL: &wireURL{URL: part.ImageURL}})
		} else {
			parts = append(parts, wirePart{Type: "text", Text: part.Text})
		}
	}
	return json.Marshal(struct {
		Role    string     `json:"role"`
		Content []wirePart `json:"content"`
	}{m.Role, parts})
}

// HasImages 判断请求里是否带图片。
func HasImages(messages []Message) bool {
	for _, message := range messages {
		for _, part := range message.Parts {
			if part.ImageURL != "" {
				return true
			}
		}
	}
	return false
}

// WithoutImages 把图片换成placeholder文字，供模型不接受图片时退回纯文字重试。
func WithoutImages(messages []Message, placeholder string) []Message {
	result := make([]Message, len(messages))
	for i, message := range messages {
		result[i] = Message{Role: message.Role, Content: message.Content}
		if len(message.Parts) == 0 {
			continue
		}
		texts := make([]string, 0, len(message.Parts))
		for _, part := range message.Parts {
			if part.ImageURL != "" {
				texts = append(texts, placeholder)
			} else {
				texts = append(texts, part.Text)
			}
		}
		result[i].Content = strings.Join(texts, "\n")
	}
	return result
}

type Config struct {
	BaseURL     string
	APIKey      string
	Model       string
	VisionModel string // 请求带图片时使用；为空时沿用Model
	MaxTokens   int
	Temperature *float64
	Timeout     time.Duration // 单次请求超时
	MaxRetries  int
}

// Client 调用OpenAI兼容的/chat/completions接口，OpenAI、DeepSeek、通义千问、Ollama、vLLM等都可接入。
type Client struct {
	config  Config
	http    *http.Client
	backoff func(attempt int) time.Duration
}

func New(config Config) *Client {
	return &Client{
		config: config,
		http:   &http.Client{},
		backoff: func(attempt int) time.Duration {
			return time.Duration(1<<attempt) * time.Second
		},
	}
}

type chatRequest struct {
	Model       string    `json:"model"`
	Messages    []Message `json:"messages"`
	MaxTokens   int       `json:"max_tokens,omitempty"`
	Temperature *float64  `json:"temperature,omitempty"`
	Stream      bool      `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
}

// StatusError 是模型接口返回的非2xx状态；不包含响应正文，避免把上游错误详情写进日志或回复。
type StatusError struct {
	StatusCode int
	retryAfter time.Duration
}

func (e *StatusError) Error() string { return fmt.Sprintf("模型接口返回HTTP %d", e.StatusCode) }

// RejectsInput 表示模型接口拒绝了请求内容本身，例如模型不支持图片或图片过大。
func (e *StatusError) RejectsInput() bool {
	switch e.StatusCode {
	case http.StatusBadRequest, http.StatusRequestEntityTooLarge, http.StatusUnsupportedMediaType, http.StatusUnprocessableEntity:
		return true
	}
	return false
}

func (e *StatusError) retryable() bool {
	return e.StatusCode == http.StatusTooManyRequests || e.StatusCode >= 500
}

// Complete 返回模型回复正文。429、5xx与网络错误按退避重试，尊重Retry-After；ctx取消时立即返回。
func (c *Client) Complete(ctx context.Context, messages []Message) (string, error) {
	model := c.config.Model
	if c.config.VisionModel != "" && HasImages(messages) {
		model = c.config.VisionModel
	}
	body, err := json.Marshal(chatRequest{
		Model: model, Messages: messages, MaxTokens: c.config.MaxTokens, Temperature: c.config.Temperature,
	})
	if err != nil {
		return "", err
	}
	var lastErr error
	for attempt := 0; ; attempt++ {
		reply, err := c.completeOnce(ctx, body)
		if err == nil {
			return reply, nil
		}
		lastErr = err
		if ctx.Err() != nil {
			return "", ctx.Err()
		}
		delay, retry := c.retryDelay(err, attempt)
		if !retry || attempt >= c.config.MaxRetries {
			return "", lastErr
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return "", ctx.Err()
		case <-timer.C:
		}
	}
}

func (c *Client) retryDelay(err error, attempt int) (time.Duration, bool) {
	delay := c.backoff(attempt)
	var statusErr *StatusError
	if errors.As(err, &statusErr) {
		if !statusErr.retryable() {
			return 0, false
		}
		if statusErr.retryAfter > delay {
			delay = statusErr.retryAfter
		}
		return delay, true
	}
	var invalid *invalidResponseError
	if errors.As(err, &invalid) {
		return 0, false
	}
	return delay, true
}

type invalidResponseError struct{ reason string }

func (e *invalidResponseError) Error() string { return "模型接口响应无效: " + e.reason }

func (c *Client) completeOnce(ctx context.Context, body []byte) (string, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.config.Timeout)
	defer cancel()
	request, err := http.NewRequestWithContext(requestCtx, http.MethodPost, c.config.BaseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return "", &invalidResponseError{reason: err.Error()}
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Accept", "application/json")
	if c.config.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.config.APIKey)
	}
	response, err := c.http.Do(request)
	if err != nil {
		return "", err
	}
	defer response.Body.Close()
	if response.StatusCode < 200 || response.StatusCode > 299 {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, maxResponseBytes))
		return "", &StatusError{StatusCode: response.StatusCode, retryAfter: parseRetryAfter(response.Header.Get("Retry-After"))}
	}
	data, err := io.ReadAll(io.LimitReader(response.Body, maxResponseBytes+1))
	if err != nil {
		return "", err
	}
	if len(data) > maxResponseBytes {
		return "", &invalidResponseError{reason: "响应超过1MB"}
	}
	var decoded chatResponse
	if err := json.Unmarshal(data, &decoded); err != nil {
		return "", &invalidResponseError{reason: "JSON解析失败"}
	}
	if len(decoded.Choices) == 0 {
		return "", &invalidResponseError{reason: "没有choices"}
	}
	return decoded.Choices[0].Message.Content, nil
}

func parseRetryAfter(value string) time.Duration {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0
	}
	var delay time.Duration
	if seconds, err := strconv.Atoi(value); err == nil && seconds > 0 {
		delay = time.Duration(seconds) * time.Second
	} else if at, err := http.ParseTime(value); err == nil {
		delay = time.Until(at)
	}
	if delay < 0 {
		return 0
	}
	if delay > maxRetryAfter {
		return maxRetryAfter
	}
	return delay
}
