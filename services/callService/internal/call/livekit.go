package call

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	callpb "Betterfly2/proto/call"
	"github.com/golang-jwt/jwt/v5"
)

// Internal stream destinations, never Kafka topics.
const mediaTopicPrefix = "livekit:"

var ErrInvalidWebhook = errors.New("invalid LiveKit webhook")

type LiveKit struct {
	apiURL, publicURL, key, secret string
	client                         *http.Client
}

func NewLiveKit(apiURL, publicURL, key, secret string) (*LiveKit, error) {
	api, err := url.Parse(apiURL)
	if err != nil || api.Host == "" || api.User != nil || api.RawQuery != "" || api.Fragment != "" || api.Scheme != "http" && api.Scheme != "https" {
		return nil, errors.New("invalid LIVEKIT_API_URL")
	}
	public, err := url.Parse(publicURL)
	if err != nil || public.Host == "" || public.User != nil || public.RawQuery != "" || public.Fragment != "" || public.Scheme != "ws" && public.Scheme != "wss" {
		return nil, errors.New("invalid LIVEKIT_PUBLIC_URL")
	}
	if strings.TrimSpace(key) == "" || len(secret) < 32 {
		return nil, errors.New("LiveKit requires an API key and a secret of at least 32 bytes")
	}
	return &LiveKit{apiURL: strings.TrimRight(apiURL, "/"), publicURL: publicURL, key: key, secret: secret, client: &http.Client{Timeout: 5 * time.Second, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}}, nil
}

func (l *LiveKit) URL() string { return l.publicURL }

func (l *LiveKit) sign(identity string, grants map[string]any, now, expires time.Time) (string, error) {
	return jwt.NewWithClaims(jwt.SigningMethodHS256, jwt.MapClaims{
		"iss": l.key, "sub": identity, "iat": now.Unix(), "nbf": now.Add(-5 * time.Second).Unix(), "exp": expires.Unix(), "video": grants,
	}).SignedString([]byte(l.secret))
}

func (l *LiveKit) Token(room GroupRoom, participant RoomParticipant, now time.Time) (string, time.Time, error) {
	expires := now.Add(time.Minute)
	if room.ExpiresAt.Before(expires) {
		expires = room.ExpiresAt
	}
	if !expires.After(now) {
		return "", time.Time{}, ErrInvalidState
	}
	sources := []string{"microphone"}
	if room.CallType == callpb.CallType_VIDEO {
		sources = append(sources, "camera")
	}
	token, err := l.sign(participant.Identity, map[string]any{
		"roomJoin": true, "room": room.Name(), "canPublish": true, "canSubscribe": true, "canPublishData": false,
		"canUpdateOwnMetadata": false, "canPublishSources": sources,
	}, now, expires)
	return token, expires, err
}

func (l *LiveKit) EnsureRoom(ctx context.Context, room GroupRoom) (string, error) {
	metadata, _ := json.Marshal(map[string]any{"group_id": room.GroupID, "call_id": room.ID, "expires_at": timestamp(room.ExpiresAt)})
	payload, _ := json.Marshal(map[string]any{"name": room.Name(), "empty_timeout": 90, "departure_timeout": 20, "max_participants": room.MaxParticipants, "metadata": string(metadata)})
	body, err := l.rpc(ctx, "CreateRoom", payload, map[string]any{"roomCreate": true})
	if err != nil {
		return "", err
	}
	var result struct {
		SID string `json:"sid"`
	}
	if json.Unmarshal(body, &result) != nil || result.SID == "" {
		return "", errors.New("LiveKit returned invalid room metadata")
	}
	return result.SID, nil
}

func (l *LiveKit) Execute(ctx context.Context, method string, payload []byte) error {
	var command struct {
		Room     string `json:"room"`
		Identity string `json:"identity"`
	}
	if json.Unmarshal(payload, &command) != nil || !strings.HasPrefix(command.Room, "bf2-group-") || !validRoomID(strings.TrimPrefix(command.Room, "bf2-group-")) {
		return ErrInvalidInput
	}
	grant := map[string]any{"roomCreate": true}
	switch method {
	case "DeleteRoom":
	case "RemoveParticipant":
		if command.Identity == "" {
			return ErrInvalidInput
		}
		grant = map[string]any{"roomAdmin": true, "room": command.Room}
	default:
		return ErrInvalidInput
	}
	_, err := l.rpc(ctx, method, payload, grant)
	return err
}

func (l *LiveKit) rpc(ctx context.Context, method string, payload []byte, grant map[string]any) ([]byte, error) {
	now := time.Now().UTC()
	token, err := l.sign("", grant, now, now.Add(time.Minute))
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.apiURL+"/twirp/livekit.RoomService/"+method, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	response, err := l.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("LiveKit %s request failed: %w", method, err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(io.LimitReader(response.Body, 64*1024))
	if err != nil {
		return nil, err
	}
	if response.StatusCode >= 200 && response.StatusCode < 300 {
		return body, nil
	}
	var failure struct {
		Code string `json:"code"`
	}
	_ = json.Unmarshal(body, &failure)
	if method != "CreateRoom" && failure.Code == "not_found" {
		return nil, nil
	}
	// Never include SFU responses, credentials or participant metadata in logs.
	return nil, fmt.Errorf("LiveKit %s returned HTTP %d", method, response.StatusCode)
}

func (l *LiveKit) VerifyWebhook(body []byte, authorization string) (MediaWebhook, error) {
	var event MediaWebhook
	if len(body) > 64*1024 {
		return event, ErrInvalidWebhook
	}
	authorization = strings.TrimPrefix(authorization, "Bearer ")
	parsed, err := jwt.Parse(authorization, func(*jwt.Token) (any, error) { return []byte(l.secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(l.key), jwt.WithExpirationRequired())
	if err != nil || !parsed.Valid {
		return event, ErrInvalidWebhook
	}
	claims, ok := parsed.Claims.(jwt.MapClaims)
	if !ok {
		return event, ErrInvalidWebhook
	}
	hash, ok := claims["sha256"].(string)
	digest := sha256.Sum256(body)
	if !ok || subtle.ConstantTimeCompare([]byte(hash), []byte(base64.StdEncoding.EncodeToString(digest[:]))) != 1 {
		return event, ErrInvalidWebhook
	}
	if json.Unmarshal(body, &event) != nil || event.ID == "" || len(event.ID) > 128 || event.Event == "" {
		return event, ErrInvalidWebhook
	}
	return event, nil
}
