package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	pushpb "Betterfly2/proto/push"
	pushservice "pushService/internal/push"
)

func TestClientSendsVoIPHeadersAndPayloadToSelectedEnvironment(t *testing.T) {
	var authorization string
	productionHits := 0
	production := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		productionHits++
		if r.ProtoMajor != 2 {
			t.Errorf("expected HTTP/2, got %s", r.Proto)
		}
		if r.Header.Get("apns-push-type") != "voip" || r.Header.Get("apns-topic") != "com.Voltline.Betterfly2.voip" || r.Header.Get("apns-priority") != "10" {
			t.Errorf("unexpected APNs headers: %+v", r.Header)
		}
		authorization = r.Header.Get("authorization")
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Errorf("decode payload: %v", err)
		}
		if payload["event"] != "incoming_call" || payload["call_uuid"] != "00112233-4455-6677-8899-aabbccddeeff" || payload["has_video"] != true {
			t.Errorf("unexpected payload: %+v", payload)
		}
		w.Header().Set("apns-id", "apns-1")
		w.WriteHeader(http.StatusOK)
	}))
	production.EnableHTTP2 = true
	production.StartTLS()
	defer production.Close()
	sandboxHits := 0
	sandbox := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		sandboxHits++
		w.WriteHeader(http.StatusOK)
	}))
	defer sandbox.Close()

	client := newTestClient(t)
	client.httpClient = production.Client()
	client.productionEndpoint = production.URL
	client.sandboxEndpoint = sandbox.URL
	result, err := client.Send(context.Background(), pushservice.Notification{
		Kind: pushservice.NotificationVoIP, Token: strings.Repeat("ab", 32), Environment: pushpb.PushEnvironment_PRODUCTION,
		CallID: "00112233445566778899aabbccddeeff", CallerUserID: 1, CalleeUserID: 2,
		CallType: "video", ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if productionHits != 1 || sandboxHits != 0 || result.APNSID != "apns-1" || !strings.HasPrefix(authorization, "bearer ") {
		t.Fatalf("unexpected APNs delivery: production=%d sandbox=%d result=%+v authorization=%q", productionHits, sandboxHits, result, authorization)
	}
}

func TestClientSendsAlertHeadersAndMessageMetadata(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apns-push-type") != "alert" || r.Header.Get("apns-topic") != "com.Voltline.Betterfly2" || r.Header.Get("apns-collapse-id") != "message-99" {
			t.Errorf("unexpected message APNs headers: %+v", r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["event"] != "new_message" || payload["conversation_id"] != float64(88) || payload["is_group"] != true || payload["message_type"] != "image" || payload["message_id"] != float64(99) {
			t.Errorf("unexpected message payload: %+v", payload)
		}
		aps := payload["aps"].(map[string]any)
		alert := aps["alert"].(map[string]any)
		if alert["title"] != "调试标题" || alert["body"] != "调试正文" || payload["debug_data"].(map[string]any)["scenario"] != "smoke" {
			t.Errorf("custom message payload missing: %+v", payload)
		}
		if aps["thread-id"] != "group:88" || aps["content-available"] != float64(1) || aps["mutable-content"] != float64(1) || aps["category"] != "MESSAGE" {
			t.Errorf("unexpected aps payload: %+v", aps)
		}
		if payload["sender_name"] != "Alice" || payload["sender_avatar"] != "alice-avatar-hash" || payload["group_name"] != "开发群" || payload["avatar"] != "group-avatar-hash" || payload["avatar_is_group"] != true || payload["conversation_name"] != "开发群" || payload["conversation_avatar"] != "group-avatar-hash" {
			t.Errorf("communication metadata missing: %+v", payload)
		}
		if payload["communication_notification"] != true {
			t.Errorf("group message must remain a communication notification: %+v", payload)
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client := newTestClient(t)
	client.httpClient = server.Client()
	client.sandboxEndpoint = server.URL
	now := time.Date(2026, 7, 11, 3, 0, 0, 0, time.UTC)
	_, err := client.Send(context.Background(), pushservice.Notification{
		Kind: pushservice.NotificationMessage, Token: strings.Repeat("ef", 32), Environment: pushpb.PushEnvironment_SANDBOX,
		SenderUserID: 1, TargetUserID: 2, ConversationID: 88, IsGroup: true, MessageID: 99,
		MessageType: "image", SentAt: now, ExpiresAt: now.Add(24 * time.Hour),
		Title: "调试标题", Body: "调试正文", CustomData: map[string]any{"scenario": "smoke"},
		SenderName: "Alice", SenderAvatar: "alice-avatar-hash", GroupName: "开发群", Avatar: "group-avatar-hash", AvatarIsGroup: true,
		ConversationName: "开发群", ConversationAvatar: "group-avatar-hash",
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestClientMapsUnregisteredResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusGone)
		_, _ = w.Write([]byte(`{"reason":"Unregistered"}`))
	}))
	defer server.Close()
	client := newTestClient(t)
	client.httpClient = server.Client()
	client.sandboxEndpoint = server.URL
	_, err := client.Send(context.Background(), pushservice.Notification{
		Kind: pushservice.NotificationVoIP, Token: strings.Repeat("cd", 32), Environment: pushpb.PushEnvironment_SANDBOX,
		CallID: "call", CallerUserID: 1, CalleeUserID: 2, CallType: "audio", ExpiresAt: time.Now().Add(time.Minute),
	})
	apnsErr, ok := err.(*pushservice.APNSError)
	if !ok || !apnsErr.InvalidatesToken() {
		t.Fatalf("expected invalidating APNs error, got %T %v", err, err)
	}
}

func TestClientSendsBroadcastAsOrdinaryAlertWithoutChatMetadata(t *testing.T) {
	server := httptest.NewUnstartedServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("apns-push-type") != "alert" || !strings.HasPrefix(r.URL.Path, "/3/device/") {
			t.Errorf("broadcast campaign must use ordinary device alert API: path=%s headers=%v", r.URL.Path, r.Header)
		}
		var payload map[string]any
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatal(err)
		}
		if payload["event"] != "broadcast" || payload["campaign_id"] != "summer-2026" || payload["deep_link"] != "betterfly://campaign/summer-2026" {
			t.Errorf("unexpected campaign payload: %+v", payload)
		}
		if _, exists := payload["communication_notification"]; exists {
			t.Errorf("campaign must not be encoded as a communication notification: %+v", payload)
		}
		aps := payload["aps"].(map[string]any)
		if aps["category"] != "BROADCAST" || aps["thread-id"] != "broadcast:summer-2026" {
			t.Errorf("unexpected campaign aps payload: %+v", aps)
		}
		w.WriteHeader(http.StatusOK)
	}))
	server.EnableHTTP2 = true
	server.StartTLS()
	defer server.Close()

	client := newTestClient(t)
	client.httpClient = server.Client()
	client.sandboxEndpoint = server.URL
	now := time.Date(2026, 7, 12, 3, 0, 0, 0, time.UTC)
	_, err := client.Send(context.Background(), pushservice.Notification{
		Kind: pushservice.NotificationBroadcast, Token: strings.Repeat("ab", 32), Environment: pushpb.PushEnvironment_SANDBOX,
		TargetUserID: 2, CampaignID: "summer-2026", Title: "夏日活动", Body: "现在就来看看",
		DeepLink: "betterfly://campaign/summer-2026", SentAt: now, ExpiresAt: now.Add(24 * time.Hour),
	})
	if err != nil {
		t.Fatal(err)
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{
		KeyID: "C6D5695Q4Y", TeamID: "8R5Q4A3RC7", BundleID: "com.Voltline.Betterfly2",
		PrivateKey: pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der}),
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

func TestAPNSCredentialIntegration(t *testing.T) {
	path := os.Getenv("APNS_INTEGRATION_KEY_PATH")
	if path == "" {
		t.Skip("APNS_INTEGRATION_KEY_PATH is not set")
	}
	key, err := LoadPrivateKey(path, "")
	if err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(Config{
		KeyID: "C6D5695Q4Y", TeamID: "8R5Q4A3RC7", BundleID: "com.Voltline.Betterfly2", PrivateKey: key,
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, environment := range []pushpb.PushEnvironment{pushpb.PushEnvironment_SANDBOX, pushpb.PushEnvironment_PRODUCTION} {
		t.Run(environment.String(), func(t *testing.T) {
			notifications := []pushservice.Notification{{
				Kind: pushservice.NotificationVoIP, Token: strings.Repeat("00", 32), Environment: environment,
				CallID: "00112233445566778899aabbccddeeff", CallerUserID: 1, CalleeUserID: 2,
				CallType: "audio", ExpiresAt: time.Now().Add(time.Minute),
			}, {
				Kind: pushservice.NotificationMessage, Token: strings.Repeat("00", 32), Environment: environment,
				SenderUserID: 1, TargetUserID: 2, ConversationID: 1, MessageType: "text",
				SentAt: time.Now(), ExpiresAt: time.Now().Add(24 * time.Hour),
			}}
			for _, notification := range notifications {
				_, sendErr := client.Send(context.Background(), notification)
				if sendErr == nil {
					t.Fatal("APNs unexpectedly accepted a synthetic device token")
				}
				apnsErr, ok := sendErr.(*pushservice.APNSError)
				if !ok {
					t.Fatalf("expected APNs protocol response, got %T: %v", sendErr, sendErr)
				}
				if apnsErr.Reason == "InvalidProviderToken" || apnsErr.Reason == "ExpiredProviderToken" || apnsErr.Reason == "TopicDisallowed" {
					t.Fatalf("APNs credential or topic rejected for %s: %s", notification.Kind, apnsErr.Reason)
				}
			}
		})
	}
}
