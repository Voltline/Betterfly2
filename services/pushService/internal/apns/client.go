package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	pushpb "Betterfly2/proto/push"
	pushservice "pushService/internal/push"
)

const (
	sandboxEndpoint    = "https://api.sandbox.push.apple.com"
	productionEndpoint = "https://api.push.apple.com"
	maxVoIPPayloadSize = 5120
	maxAPNsPayloadSize = 4096
)

type Config struct {
	KeyID      string
	TeamID     string
	BundleID   string
	PrivateKey []byte
}

type Client struct {
	keyID              string
	teamID             string
	bundleID           string
	privateKey         *ecdsa.PrivateKey
	httpClient         *http.Client
	sandboxEndpoint    string
	productionEndpoint string

	mu          sync.Mutex
	providerJWT string
	jwtIssuedAt time.Time
	now         func() time.Time
}

func NewClient(config Config) (*Client, error) {
	if strings.TrimSpace(config.KeyID) == "" || strings.TrimSpace(config.TeamID) == "" || strings.TrimSpace(config.BundleID) == "" || len(config.PrivateKey) == 0 {
		return nil, errors.New("incomplete APNs configuration")
	}
	privateKey, err := parsePrivateKey(config.PrivateKey)
	if err != nil {
		return nil, err
	}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.ForceAttemptHTTP2 = true
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 20
	transport.IdleConnTimeout = 90 * time.Second
	transport.TLSClientConfig = &tls.Config{MinVersion: tls.VersionTLS12}
	return &Client{
		keyID: strings.TrimSpace(config.KeyID), teamID: strings.TrimSpace(config.TeamID), bundleID: strings.TrimSpace(config.BundleID),
		privateKey: privateKey, httpClient: &http.Client{Transport: transport, Timeout: 15 * time.Second},
		sandboxEndpoint: sandboxEndpoint, productionEndpoint: productionEndpoint, now: time.Now,
	}, nil
}

func LoadPrivateKey(path, encoded string) ([]byte, error) {
	if value := strings.TrimSpace(encoded); value != "" {
		decoded, err := base64.StdEncoding.DecodeString(value)
		if err != nil {
			return nil, fmt.Errorf("decode APNS_PRIVATE_KEY_BASE64: %w", err)
		}
		return decoded, nil
	}
	if strings.TrimSpace(path) == "" {
		return nil, errors.New("APNS_PRIVATE_KEY_PATH or APNS_PRIVATE_KEY_BASE64 is required")
	}
	key, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read APNs private key: %w", err)
	}
	return key, nil
}

func (c *Client) Ready() error { return nil }

func (c *Client) Send(ctx context.Context, notification pushservice.Notification) (pushservice.SendResult, error) {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		result, err := c.sendOnce(ctx, notification)
		if err == nil {
			return result, nil
		}
		lastErr = err
		if errors.Is(err, pushservice.ErrInvalidRequest) {
			return pushservice.SendResult{}, err
		}
		var apnsErr *pushservice.APNSError
		if errors.As(err, &apnsErr) && !apnsErr.Retryable() {
			return pushservice.SendResult{}, err
		}
		if attempt == 2 {
			break
		}
		timer := time.NewTimer(time.Duration(attempt+1) * 150 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return pushservice.SendResult{}, ctx.Err()
		case <-timer.C:
		}
	}
	return pushservice.SendResult{}, lastErr
}

func (c *Client) sendOnce(ctx context.Context, notification pushservice.Notification) (pushservice.SendResult, error) {
	if strings.TrimSpace(notification.Token) == "" || !validNotification(notification) {
		return pushservice.SendResult{}, pushservice.ErrInvalidRequest
	}
	endpoint, err := c.endpoint(notification.Environment)
	if err != nil {
		return pushservice.SendResult{}, err
	}
	payload, err := marshalPayload(notification)
	if err != nil {
		return pushservice.SendResult{}, err
	}
	providerToken, err := c.token()
	if err != nil {
		return pushservice.SendResult{}, err
	}

	requestURL := endpoint + "/3/device/" + url.PathEscape(strings.TrimSpace(notification.Token))
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, requestURL, strings.NewReader(string(payload)))
	if err != nil {
		return pushservice.SendResult{}, err
	}
	request.Header.Set("authorization", "bearer "+providerToken)
	pushType, topic, collapseID := c.requestMetadata(notification)
	request.Header.Set("apns-push-type", pushType)
	request.Header.Set("apns-topic", topic)
	request.Header.Set("apns-priority", "10")
	request.Header.Set("apns-expiration", strconv.FormatInt(notification.ExpiresAt.Unix(), 10))
	if collapseID != "" {
		request.Header.Set("apns-collapse-id", collapseID)
	}
	request.Header.Set("content-type", "application/json")

	response, err := c.httpClient.Do(request)
	if err != nil {
		return pushservice.SendResult{}, err
	}
	defer response.Body.Close()
	apnsID := response.Header.Get("apns-id")
	if response.StatusCode == http.StatusOK {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
		return pushservice.SendResult{APNSID: apnsID}, nil
	}
	var responseBody struct {
		Reason string `json:"reason"`
	}
	_ = json.NewDecoder(io.LimitReader(response.Body, 4096)).Decode(&responseBody)
	return pushservice.SendResult{}, &pushservice.APNSError{StatusCode: response.StatusCode, Reason: responseBody.Reason, APNSID: apnsID}
}

func validNotification(notification pushservice.Notification) bool {
	switch notification.Kind {
	case pushservice.NotificationVoIP:
		return strings.TrimSpace(notification.CallID) != "" && notification.CallerUserID > 0 && notification.CalleeUserID > 0 && notification.ExpiresAt.After(time.Unix(0, 0))
	case pushservice.NotificationMessage:
		return notification.SenderUserID > 0 && notification.TargetUserID > 0 && notification.ConversationID > 0 && strings.TrimSpace(notification.MessageType) != "" && notification.ExpiresAt.After(time.Unix(0, 0))
	default:
		return false
	}
}

func (c *Client) requestMetadata(notification pushservice.Notification) (pushType, topic, collapseID string) {
	if notification.Kind == pushservice.NotificationVoIP {
		return "voip", c.bundleID + ".voip", notification.CallID
	}
	return "alert", c.bundleID, ""
}

func (c *Client) endpoint(environment pushpb.PushEnvironment) (string, error) {
	switch environment {
	case pushpb.PushEnvironment_SANDBOX:
		return c.sandboxEndpoint, nil
	case pushpb.PushEnvironment_PRODUCTION:
		return c.productionEndpoint, nil
	default:
		return "", pushservice.ErrInvalidRequest
	}
}

func (c *Client) token() (string, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := c.now().UTC()
	if c.providerJWT != "" && now.Sub(c.jwtIssuedAt) < 50*time.Minute {
		return c.providerJWT, nil
	}
	header, err := encodeJSON(map[string]any{"alg": "ES256", "kid": c.keyID})
	if err != nil {
		return "", err
	}
	claims, err := encodeJSON(map[string]any{"iss": c.teamID, "iat": now.Unix()})
	if err != nil {
		return "", err
	}
	unsigned := header + "." + claims
	hash := sha256.Sum256([]byte(unsigned))
	r, s, err := ecdsa.Sign(rand.Reader, c.privateKey, hash[:])
	if err != nil {
		return "", err
	}
	signature := make([]byte, 64)
	r.FillBytes(signature[:32])
	s.FillBytes(signature[32:])
	c.providerJWT = unsigned + "." + base64.RawURLEncoding.EncodeToString(signature)
	c.jwtIssuedAt = now
	return c.providerJWT, nil
}

func encodeJSON(value any) (string, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(data), nil
}

func parsePrivateKey(data []byte) (*ecdsa.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return nil, errors.New("invalid APNs .p8 PEM data")
	}
	parsed, err := x509.ParsePKCS8PrivateKey(block.Bytes)
	if err != nil {
		return nil, fmt.Errorf("parse APNs private key: %w", err)
	}
	key, ok := parsed.(*ecdsa.PrivateKey)
	if !ok || key.Curve.Params().Name != "P-256" {
		return nil, errors.New("APNs private key must be an ECDSA P-256 key")
	}
	return key, nil
}

func marshalPayload(notification pushservice.Notification) ([]byte, error) {
	if notification.Kind == pushservice.NotificationMessage {
		return marshalMessagePayload(notification)
	}
	callType := strings.ToLower(strings.TrimSpace(notification.CallType))
	payload := map[string]any{
		"aps":            map[string]any{"content-available": 1},
		"event":          "incoming_call",
		"call_id":        notification.CallID,
		"call_uuid":      callUUID(notification.CallID),
		"caller_user_id": notification.CallerUserID,
		"call_type":      callType,
		"has_video":      callType == "video",
		"expires_at":     notification.ExpiresAt.UTC().Format(time.RFC3339Nano),
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(data) > maxVoIPPayloadSize {
		return nil, fmt.Errorf("VoIP payload exceeds %d bytes", maxVoIPPayloadSize)
	}
	return data, nil
}

func marshalMessagePayload(notification pushservice.Notification) ([]byte, error) {
	title := strings.TrimSpace(notification.Title)
	if title == "" {
		title = "Betterfly"
	}
	body := "你收到一条新消息"
	if notification.IsGroup {
		body = "你收到一条群聊消息"
	}
	if customBody := strings.TrimSpace(notification.Body); customBody != "" {
		body = customBody
	}
	threadID := "user:" + strconv.FormatInt(notification.SenderUserID, 10)
	if notification.IsGroup {
		threadID = "group:" + strconv.FormatInt(notification.ConversationID, 10)
	}
	payload := map[string]any{
		"aps": map[string]any{
			"alert": map[string]string{"title": title, "body": body},
			"sound": "default", "content-available": 1, "mutable-content": 1,
			"thread-id": threadID, "category": "MESSAGE",
		},
		"event": "new_message", "sender_user_id": notification.SenderUserID,
		"conversation_id": notification.ConversationID, "is_group": notification.IsGroup,
		"message_type":               strings.TrimSpace(notification.MessageType),
		"sent_at":                    notification.SentAt.UTC().Format(time.RFC3339Nano),
		"sender_name":                strings.TrimSpace(notification.SenderName),
		"group_name":                 strings.TrimSpace(notification.GroupName),
		"avatar":                     strings.TrimSpace(notification.Avatar),
		"avatar_is_group":            notification.AvatarIsGroup,
		"communication_notification": true,
	}
	if len(notification.CustomData) > 0 {
		payload["debug_data"] = notification.CustomData
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(data) > maxAPNsPayloadSize {
		return nil, fmt.Errorf("APNs payload exceeds %d bytes", maxAPNsPayloadSize)
	}
	return data, nil
}

func callUUID(callID string) string {
	compact := strings.ReplaceAll(strings.TrimSpace(callID), "-", "")
	if len(compact) != 32 {
		return callID
	}
	return compact[0:8] + "-" + compact[8:12] + "-" + compact[12:16] + "-" + compact[16:20] + "-" + compact[20:32]
}
