package connection

import (
	"sync"
	"sync/atomic"
	"testing"
)

func TestConnectionClosePreventsFurtherEnqueue(t *testing.T) {
	conn := &Connection{
		ID:       "conn-1",
		SendChan: make(chan []byte, 1),
	}

	if err := conn.EnqueueMessage([]byte("first")); err != nil {
		t.Fatalf("expected first enqueue to succeed, got error: %v", err)
	}

	conn.Close()

	if err := conn.EnqueueMessage([]byte("second")); err == nil {
		t.Fatal("expected enqueue on closed connection to fail")
	}

	msg, ok := <-conn.SendChan
	if !ok {
		t.Fatal("expected buffered message to remain readable after close")
	}
	if string(msg) != "first" {
		t.Fatalf("unexpected buffered message: %q", string(msg))
	}

	if _, ok := <-conn.SendChan; ok {
		t.Fatal("expected send channel to be closed after draining buffered messages")
	}
}

func TestConnectionEnqueueRejectsFullChannelAndCloseIsIdempotent(t *testing.T) {
	conn := &Connection{ID: "conn-full", SendChan: make(chan []byte, 1)}
	if err := conn.EnqueueMessage([]byte("first")); err != nil {
		t.Fatal(err)
	}
	if err := conn.EnqueueMessage([]byte("second")); err == nil {
		t.Fatal("expected a full send channel to reject the message")
	}
	conn.Close()
	conn.Close()
	if !conn.IsClosed() || !conn.ShouldStop {
		t.Fatalf("connection was not marked closed: %+v", conn)
	}
}

func TestConnectionManagerLookupSendAndRemoveUnloggedConnection(t *testing.T) {
	manager := NewConnectionManager()
	conn := &Connection{ID: "conn-1", UserID: "user-1", SendChan: make(chan []byte, 1)}
	manager.connections.Store(conn.ID, conn)
	manager.userConnections.Store(conn.UserID, conn.ID)
	atomic.StoreInt64(&manager.connectionCount, 1)
	atomic.StoreInt64(&manager.loggedInCount, 1)

	if got, ok := manager.GetConnectionByID(conn.ID); !ok || got != conn {
		t.Fatalf("connection lookup failed: got=%p ok=%v", got, ok)
	}
	if got, ok := manager.GetConnectionByUserID(conn.UserID); !ok || got != conn {
		t.Fatalf("user lookup failed: got=%p ok=%v", got, ok)
	}
	if !manager.IsUserLoggedIn(conn.UserID) || manager.GetConnectionCount() != 1 || manager.GetLoggedInUserCount() != 1 {
		t.Fatalf("unexpected manager counts: connections=%d users=%d", manager.GetConnectionCount(), manager.GetLoggedInUserCount())
	}
	if err := manager.SendMessageToUser(conn.UserID, []byte("hello")); err != nil {
		t.Fatal(err)
	}
	if got := <-conn.SendChan; string(got) != "hello" {
		t.Fatalf("unexpected queued message: %q", got)
	}
	if err := manager.SendMessageToUser("missing", []byte("hello")); err == nil {
		t.Fatal("expected missing user send to fail")
	}

	// Remove the artificial login mapping before exercising unauthenticated removal.
	manager.userConnections.Delete(conn.UserID)
	atomic.StoreInt64(&manager.loggedInCount, 0)
	manager.RemoveConnection(conn.ID)
	if manager.GetConnectionCount() != 0 || !conn.IsClosed() {
		t.Fatalf("connection was not removed: count=%d closed=%v", manager.GetConnectionCount(), conn.IsClosed())
	}
}

func TestConnectionConcurrentEnqueueAndCloseDoesNotPanic(t *testing.T) {
	conn := &Connection{ID: "conn-race", SendChan: make(chan []byte, 64)}
	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = conn.EnqueueMessage([]byte("message"))
		}()
	}
	conn.Close()
	wg.Wait()
	if !conn.IsClosed() {
		t.Fatal("connection should be closed")
	}
}
