package apns

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"fmt"
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
		Token: strings.Repeat("ab", 32), Environment: pushpb.PushEnvironment_PRODUCTION,
		CallID: "00112233445566778899aabbccddeeff", CallerUserID: 1, CalleeUserID: 2,
		CallType: "video", ExpiresAt: time.Now().Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if productionHits != 1 || sandboxHits != 0 || result.APNSID != "apns-1" || !strings.HasPrefix(authorization, "bearer ") {
		t.Fatalf("unexpected APNs delivery: production=%d sandbox=%d result=%+v authorization=%q", productionHits, sandboxHits, result, authorization)
	}
	fmt.Println(pushservice.Notification{
		Token: strings.Repeat("ab", 32), Environment: pushpb.PushEnvironment_PRODUCTION,
		CallID: "00112233445566778899aabbccddeeff", CallerUserID: 1, CalleeUserID: 2,
		CallType: "video", ExpiresAt: time.Now().Add(time.Minute),
	})
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
		Token: strings.Repeat("cd", 32), Environment: pushpb.PushEnvironment_SANDBOX,
		CallID: "call", CallerUserID: 1, CalleeUserID: 2, CallType: "audio", ExpiresAt: time.Now().Add(time.Minute),
	})
	apnsErr, ok := err.(*pushservice.APNSError)
	if !ok || !apnsErr.InvalidatesToken() {
		t.Fatalf("expected invalidating APNs error, got %T %v", err, err)
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
			_, sendErr := client.Send(context.Background(), pushservice.Notification{
				Token: strings.Repeat("00", 32), Environment: environment,
				CallID: "00112233445566778899aabbccddeeff", CallerUserID: 1, CalleeUserID: 2,
				CallType: "audio", ExpiresAt: time.Now().Add(time.Minute),
			})
			if sendErr == nil {
				t.Fatal("APNs unexpectedly accepted a synthetic device token")
			}
			apnsErr, ok := sendErr.(*pushservice.APNSError)
			if !ok {
				t.Fatalf("expected APNs protocol response, got %T: %v", sendErr, sendErr)
			}
			if apnsErr.Reason == "InvalidProviderToken" || apnsErr.Reason == "ExpiredProviderToken" || apnsErr.Reason == "TopicDisallowed" {
				t.Fatalf("APNs credential or topic rejected: %s", apnsErr.Reason)
			}
		})
	}
}
