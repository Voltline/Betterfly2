package session

import (
	"data_forwarding_service/internal/connection"
	"sync"
	"testing"
	"time"
)

func TestSessionLifecycle(t *testing.T) {
	manager := NewSessionManager()
	if err := manager.CreateSession("user-1", "conn-1"); err != nil {
		t.Fatal(err)
	}
	if err := manager.CreateSession("user-1", "conn-2"); err == nil {
		t.Fatal("expected duplicate active session to be rejected")
	}

	session, ok := manager.GetSession("user-1")
	if !ok || session.ConnectionID != "conn-1" || !session.IsActive {
		t.Fatalf("unexpected session: %+v exists=%v", session, ok)
	}
	previousActivity := session.LastActive
	time.Sleep(time.Millisecond)
	if err := manager.UpdateSessionActivity("user-1"); err != nil {
		t.Fatal(err)
	}
	if !session.LastActive.After(previousActivity) {
		t.Fatal("expected activity timestamp to advance")
	}
	if manager.GetSessionCount() != 1 || len(manager.GetActiveSessions()) != 1 {
		t.Fatalf("unexpected active session counts: total=%d active=%d", manager.GetSessionCount(), len(manager.GetActiveSessions()))
	}
	if err := manager.RemoveSession("user-1"); err != nil {
		t.Fatal(err)
	}
	if session.IsActive || manager.GetSessionCount() != 0 {
		t.Fatalf("session was not removed: %+v", session)
	}
	if err := manager.RemoveSession("user-1"); err == nil {
		t.Fatal("expected removing a missing session to fail")
	}
	if err := manager.UpdateSessionActivity("missing"); err == nil {
		t.Fatal("expected updating a missing session to fail")
	}
}

func TestCleanupInactiveSessionsOnlyRemovesExpiredActiveSessions(t *testing.T) {
	manager := NewSessionManager()
	_ = manager.CreateSession("expired", "conn-expired")
	_ = manager.CreateSession("fresh", "conn-fresh")
	expired, _ := manager.GetSession("expired")
	expired.LastActive = time.Now().Add(-time.Hour)

	cleaned := manager.CleanupInactiveSessions(time.Minute, connection.NewConnectionManager())
	if cleaned != 1 {
		t.Fatalf("expected one cleanup, got %d", cleaned)
	}
	if _, exists := manager.GetSession("expired"); exists {
		t.Fatal("expired session was not removed")
	}
	if _, exists := manager.GetSession("fresh"); !exists {
		t.Fatal("fresh session was removed")
	}
}

func TestConcurrentSessionCreationAllowsSingleWinner(t *testing.T) {
	manager := NewSessionManager()
	const attempts = 20
	var wg sync.WaitGroup
	results := make(chan error, attempts)
	for i := 0; i < attempts; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			results <- manager.CreateSession("same-user", "connection")
		}()
	}
	wg.Wait()
	close(results)

	successes := 0
	for err := range results {
		if err == nil {
			successes++
		}
	}
	if successes != 1 || manager.GetSessionCount() != 1 {
		t.Fatalf("expected one winning session, successes=%d count=%d", successes, manager.GetSessionCount())
	}
}
