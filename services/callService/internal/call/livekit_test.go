package call

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	callpb "Betterfly2/proto/call"
	"github.com/golang-jwt/jwt/v5"
)

func TestLiveKitTokensRestrictRoomIdentityAndSources(t *testing.T) {
	l, err := NewLiveKit("http://media.test", "wss://media.test", "key", strings.Repeat("s", 32))
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []callpb.CallType{callpb.CallType_AUDIO, callpb.CallType_VIDEO} {
		now := time.Now()
		room := GroupRoom{ID: strings.Repeat("a", 32), CallType: kind, ExpiresAt: now.Add(time.Hour)}
		token, expires, err := l.Token(room, RoomParticipant{Identity: "bf2-1-admission"}, now)
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := jwt.Parse(token, func(*jwt.Token) (any, error) { return []byte(l.secret), nil }, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer("key"))
		if err != nil {
			t.Fatal(err)
		}
		claims := parsed.Claims.(jwt.MapClaims)
		grant := claims["video"].(map[string]any)
		if grant["room"] != room.Name() || claims["sub"] != "bf2-1-admission" || grant["roomJoin"] != true || grant["canPublishData"] != false || grant["roomAdmin"] != nil || expires.Sub(now) != time.Minute {
			t.Fatal("overprivileged token")
		}
		sources := grant["canPublishSources"].([]any)
		if kind == callpb.CallType_AUDIO && len(sources) != 1 || kind == callpb.CallType_VIDEO && len(sources) != 2 || sources[0] != "microphone" {
			t.Fatal("source permissions incorrect")
		}
	}
}

func TestLiveKitWebhookSignatureBodyIssuerAndAlgorithm(t *testing.T) {
	l, _ := NewLiveKit("http://media.test", "ws://media.test", "key", strings.Repeat("s", 32))
	body := []byte(`{"id":"event","event":"room_finished","room":{"name":"room","sid":"SID"}}`)
	digest := sha256.Sum256(body)
	claims := jwt.MapClaims{"iss": "key", "exp": time.Now().Add(time.Minute).Unix(), "sha256": base64.StdEncoding.EncodeToString(digest[:])}
	token, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(l.secret))
	if _, err := l.VerifyWebhook(body, token); err != nil {
		t.Fatal(err)
	}
	if _, err := l.VerifyWebhook(append(body, ' '), token); err == nil {
		t.Fatal("tampered body accepted")
	}
	claims["iss"] = "other"
	bad, _ := jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(l.secret))
	if _, err := l.VerifyWebhook(body, bad); err == nil {
		t.Fatal("wrong issuer accepted")
	}
	claims["iss"] = "key"
	bad, _ = jwt.NewWithClaims(jwt.SigningMethodHS384, claims).SignedString([]byte(l.secret))
	if _, err := l.VerifyWebhook(body, bad); err == nil {
		t.Fatal("wrong algorithm accepted")
	}
	claims["exp"] = time.Now().Add(-time.Minute).Unix()
	bad, _ = jwt.NewWithClaims(jwt.SigningMethodHS256, claims).SignedString([]byte(l.secret))
	if _, err := l.VerifyWebhook(body, bad); err == nil {
		t.Fatal("expired callback accepted")
	}
}

func TestLiveKitRoomServiceGrantsAndRetryableFailure(t *testing.T) {
	secret := strings.Repeat("s", 32)
	status := http.StatusOK
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parsed, err := jwt.Parse(strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer "), func(*jwt.Token) (any, error) { return []byte(secret), nil })
		if err != nil {
			t.Error(err)
			w.WriteHeader(401)
			return
		}
		var command map[string]any
		_ = json.NewDecoder(r.Body).Decode(&command)
		grant := parsed.Claims.(jwt.MapClaims)["video"].(map[string]any)
		if command["name"] != nil && grant["roomCreate"] != true {
			t.Error("create grant missing")
		}
		if strings.HasSuffix(r.URL.Path, "RemoveParticipant") && (grant["roomAdmin"] != true || grant["room"] != command["room"]) {
			t.Error("room admin grant incorrect")
		}
		w.WriteHeader(status)
		if status == 200 {
			_, _ = w.Write([]byte(`{"sid":"RM_test"}`))
		} else {
			_, _ = w.Write([]byte(`{"code":"not_found"}`))
		}
	}))
	defer server.Close()
	l, _ := NewLiveKit(server.URL, "ws://media.test", "key", secret)
	room := GroupRoom{ID: strings.Repeat("a", 32), MaxParticipants: 16, ExpiresAt: time.Now().Add(time.Hour)}
	if sid, err := l.EnsureRoom(context.Background(), room); err != nil || sid != "RM_test" {
		t.Fatalf("create: %s %v", sid, err)
	}
	if err := l.Execute(context.Background(), "RemoveParticipant", mediaEvent("op", "RemoveParticipant", room.Name(), "identity").Payload); err != nil {
		t.Fatal(err)
	}
	status = 404
	if err := l.Execute(context.Background(), "DeleteRoom", mediaEvent("op", "DeleteRoom", room.Name(), "").Payload); err != nil {
		t.Fatal("delete not_found must be idempotent")
	}
	if _, err := l.EnsureRoom(context.Background(), room); err == nil {
		t.Fatal("create failed but returned success")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := l.Execute(ctx, "DeleteRoom", mediaEvent("op", "DeleteRoom", room.Name(), "").Payload); err == nil {
		t.Fatal("context cancellation ignored")
	}
}
