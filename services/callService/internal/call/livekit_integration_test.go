package call

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"
)

// Opt-in real SFU contract test; no APNs or client media devices involved.
func TestLiveKitRealRoomAndSignedWebhook(t *testing.T) {
	api := os.Getenv("BETTERFLY_LIVEKIT_TEST_URL")
	if api == "" {
		t.Skip("BETTERFLY_LIVEKIT_TEST_URL is not configured")
	}
	media, err := NewLiveKit(api, "ws://127.0.0.1:27880", "test-key", "test-secret-at-least-32-characters-long")
	if err != nil {
		t.Fatal(err)
	}
	f := newGroupFixture(t, 4)
	f.service.groups.media = media
	listener, err := net.Listen("tcp", ":27985")
	if err != nil {
		t.Fatal(err)
	}
	callbackErrors := make(chan error, 10)
	server := &http.Server{ReadHeaderTimeout: time.Second, Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 64*1024))
		if err == nil {
			err = f.service.HandleMediaWebhook(r.Context(), body, r.Header.Get("Authorization"))
		}
		if err != nil {
			select {
			case callbackErrors <- err:
			default:
			}
			w.WriteHeader(503)
			return
		}
		w.WriteHeader(204)
	})}
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() { _ = server.Close() })
	room := f.create(t)
	room = f.join(t, 1, room.ID, "join-real")
	t.Cleanup(func() {
		_ = media.Execute(context.Background(), "DeleteRoom", mediaEvent("cleanup", "DeleteRoom", room.Name(), "").Payload)
	})
	if room.MediaSID == "" {
		t.Fatal("SFU room identity missing")
	}
	if err := media.Execute(context.Background(), "DeleteRoom", mediaEvent("delete", "DeleteRoom", room.Name(), "").Payload); err != nil {
		t.Fatal(err)
	}
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case err := <-callbackErrors:
			t.Fatalf("real SFU callback was rejected: %v", err)
		case <-deadline.C:
			t.Fatal("real room_finished webhook did not end the room")
		case <-ticker.C:
			current, err := f.store.GetRoom(context.Background(), room.ID)
			if err != nil {
				t.Fatal(err)
			}
			if current.State == StateEnded {
				return
			}
		}
	}
}
