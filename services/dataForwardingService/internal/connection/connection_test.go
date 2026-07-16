package connection

import (
	"context"
	redisClient "data_forwarding_service/internal/redis"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/redis/go-redis/v9"
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

func useLoginTestRedis(t *testing.T) {
	t.Helper()
	server := miniredis.RunT(t)
	previous := redisClient.Rdb
	redisClient.Rdb = redis.NewClient(&redis.Options{Addr: server.Addr()})
	t.Cleanup(func() {
		_ = redisClient.Rdb.Close()
		redisClient.Rdb = previous
	})
}

func addTestConnection(manager *ConnectionManager, id string) *Connection {
	connection := &Connection{ID: id, SendChan: make(chan []byte, 4), done: make(chan struct{})}
	manager.connections.Store(id, connection)
	atomic.AddInt64(&manager.connectionCount, 1)
	return connection
}

func TestLoginExternalWorkDoesNotBlockOtherUsersOrLocalOperations(t *testing.T) {
	useLoginTestRedis(t)
	manager := NewConnectionManager()
	blocked := addTestConnection(manager, "blocked")
	other := addTestConnection(manager, "other")
	local := addTestConnection(manager, "local")
	local.MarkAuthenticated("99", "local-owner")
	manager.userConnections.Store("99", local.ID)
	atomic.AddInt64(&manager.loggedInCount, 1)

	entered := make(chan struct{})
	release := make(chan struct{})
	manager.beforeExternalLogin = func(ctx context.Context, userID string) error {
		if userID != "1" {
			return nil
		}
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	blockedResult := make(chan error, 1)
	go func() { blockedResult <- manager.Login(context.Background(), blocked.ID, "1") }()
	<-entered

	if err := manager.SendMessageToUser("99", []byte("still-responsive")); err != nil {
		t.Fatalf("local send was blocked or failed: %v", err)
	}
	manager.RemoveConnection(local.ID)
	if err := manager.Login(context.Background(), other.ID, "2"); err != nil {
		t.Fatalf("different user login was blocked: %v", err)
	}
	close(release)
	if err := <-blockedResult; err != nil {
		t.Fatal(err)
	}
}

func TestLoginFailureAndCancellationFailClosed(t *testing.T) {
	useLoginTestRedis(t)
	manager := NewConnectionManager()
	connection := addTestConnection(manager, "failed")
	manager.beforeExternalLogin = func(context.Context, string) error { return errors.New("redis unavailable") }
	if err := manager.Login(context.Background(), connection.ID, "10"); err == nil {
		t.Fatal("expected external login failure")
	}
	if connection.IsAuthenticated() || manager.GetLoggedInUserCount() != 0 {
		t.Fatal("failed login left local authenticated state")
	}

	manager.beforeExternalLogin = func(ctx context.Context, _ string) error {
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := manager.Login(ctx, connection.ID, "10"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("login did not honor cancellation: %v", err)
	}
	if connection.IsAuthenticated() {
		t.Fatal("canceled login authenticated connection")
	}
}

func TestConcurrentSameUserHasOneOwnerAndOldCleanupIsFenced(t *testing.T) {
	useLoginTestRedis(t)
	manager := NewConnectionManager()
	first := addTestConnection(manager, "first")
	second := addTestConnection(manager, "second")
	if err := manager.Login(context.Background(), first.ID, "7"); err != nil {
		t.Fatal(err)
	}
	firstOwner := first.OwnerToken
	if err := manager.Login(context.Background(), second.ID, "7"); err != nil {
		t.Fatal(err)
	}
	if !second.IsAuthenticated() || second.OwnerToken == firstOwner || !first.IsClosed() {
		t.Fatalf("ownership did not move cleanly: first=%q second=%q", firstOwner, second.OwnerToken)
	}
	manager.RemoveConnection(first.ID)
	container, err := redisClient.GetContainerByConnection("7")
	if err != nil || container != currentContainerID() {
		t.Fatalf("old cleanup removed new route: container=%q err=%v", container, err)
	}
	session, exists, err := (&redisClient.DistributedSessionManager{}).GetUserSession(context.Background(), "7")
	if err != nil || !exists || session.OwnerToken != second.OwnerToken {
		t.Fatalf("unexpected final owner: %+v exists=%v err=%v", session, exists, err)
	}
	if len(manager.userLocks.entries) != 0 {
		t.Fatalf("keyed lock leaked entries: %d", len(manager.userLocks.entries))
	}
}

func TestConnectionEnqueueRejectsFullChannelAndCloseIsIdempotent(t *testing.T) {
	conn := &Connection{ID: "conn-full", SendChan: make(chan []byte, 1), done: make(chan struct{})}
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
	select {
	case <-conn.Done():
	default:
		t.Fatal("connection close signal was not delivered")
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
	if manager.GetConnectionCount() != 1 || manager.GetLoggedInUserCount() != 1 {
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

func TestConnectionManagerRejectsInvalidUserIDBeforeSessionRegistration(t *testing.T) {
	manager := NewConnectionManager()
	for _, userID := range []string{"", "-1", "0", "not-a-number"} {
		if err := manager.Login(context.Background(), "missing-connection", userID); err == nil {
			t.Fatalf("Login accepted invalid user ID %q", userID)
		}
	}
	if manager.GetLoggedInUserCount() != 0 {
		t.Fatalf("invalid login changed logged-in count: %d", manager.GetLoggedInUserCount())
	}
}
