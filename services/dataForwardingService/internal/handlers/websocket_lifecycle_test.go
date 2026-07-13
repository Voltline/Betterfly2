package handlers

import (
	"data_forwarding_service/internal/connection"
	"data_forwarding_service/internal/router"
	"data_forwarding_service/internal/session"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func testWebSocketHandler(config websocketConfig) *WebSocketHandler {
	manager := connection.NewConnectionManager()
	handler := &WebSocketHandler{
		connManager:    manager,
		sessionManager: session.NewSessionManager(),
		router:         router.NewRouter(manager),
		config:         config,
	}
	handler.upgrader.CheckOrigin = config.checkOrigin
	return handler
}

func testWebSocketConfig() websocketConfig {
	return websocketConfig{
		allowedOrigins:     map[string]struct{}{"https://client.example.com:443": {}},
		allowMissingOrigin: true,
		maxMessageBytes:    1024,
		authTimeout:        200 * time.Millisecond,
		pongWait:           100 * time.Millisecond,
		pingInterval:       20 * time.Millisecond,
		writeTimeout:       100 * time.Millisecond,
		readHeaderTimeout:  time.Second,
		idleTimeout:        time.Second,
		maxHeaderBytes:     4096,
	}
}

func startWebSocketTestServer(t *testing.T, handler *WebSocketHandler) (string, func()) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(handler.handleConnection))
	url := "ws" + strings.TrimPrefix(server.URL, "http")
	return url, server.Close
}

func waitForConnection(t *testing.T, handler *WebSocketHandler, client *websocket.Conn) *connection.Connection {
	t.Helper()
	id := client.UnderlyingConn().LocalAddr().String()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if conn, ok := handler.connManager.GetConnectionByID(id); ok {
			return conn
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("server connection %s was not registered", id)
	return nil
}

func TestWebSocketOriginPolicy(t *testing.T) {
	config := testWebSocketConfig()
	request := httptest.NewRequest(http.MethodGet, "/ws", nil)
	request.Header.Set("Origin", "https://evil.example.com:443")
	if config.checkOrigin(request) {
		t.Fatal("non-whitelisted Origin was accepted")
	}
	request.Header.Set("Origin", "https://client.example.com:443")
	if !config.checkOrigin(request) {
		t.Fatal("whitelisted Origin was rejected")
	}
	request.Header.Del("Origin")
	if !config.checkOrigin(request) {
		t.Fatal("configured native client without Origin was rejected")
	}
	config.allowMissingOrigin = false
	if config.checkOrigin(request) {
		t.Fatal("missing Origin was accepted when disabled")
	}
}

func TestWebSocketOversizedMessageClosesAndCleansConnection(t *testing.T) {
	config := testWebSocketConfig()
	config.maxMessageBytes = 16
	handler := testWebSocketHandler(config)
	url, closeServer := startWebSocketTestServer(t, handler)
	defer closeServer()
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	if err := client.WriteMessage(websocket.BinaryMessage, make([]byte, 64)); err != nil {
		t.Fatal(err)
	}
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("oversized message did not close the connection")
	}
	if closeErr, ok := err.(*websocket.CloseError); !ok || closeErr.Code != websocket.CloseMessageTooBig {
		t.Fatalf("oversized message used unexpected close error: %v", err)
	}
	waitForConnectionCount(t, handler, 0)
}

func TestWebSocketUnauthenticatedConnectionTimesOut(t *testing.T) {
	config := testWebSocketConfig()
	config.authTimeout = 40 * time.Millisecond
	handler := testWebSocketHandler(config)
	url, closeServer := startWebSocketTestServer(t, handler)
	defer closeServer()
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	_, _, err = client.ReadMessage()
	if err == nil {
		t.Fatal("unauthenticated connection remained open")
	}
	if closeErr, ok := err.(*websocket.CloseError); !ok || closeErr.Code != websocket.ClosePolicyViolation {
		t.Fatalf("unexpected authentication timeout close error: %v", err)
	}
	waitForConnectionCount(t, handler, 0)
}

func TestWebSocketPongExtendsAuthenticatedConnection(t *testing.T) {
	config := testWebSocketConfig()
	config.authTimeout = 80 * time.Millisecond
	config.pongWait = 45 * time.Millisecond
	config.pingInterval = 10 * time.Millisecond
	handler := testWebSocketHandler(config)
	url, closeServer := startWebSocketTestServer(t, handler)
	defer closeServer()
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	serverConn := waitForConnection(t, handler, client)
	serverConn.MarkAuthenticated("")
	readDone := make(chan error, 1)
	go func() {
		for {
			if _, _, err := client.ReadMessage(); err != nil {
				readDone <- err
				return
			}
		}
	}()

	time.Sleep(180 * time.Millisecond)
	if serverConn.IsClosed() || handler.connManager.GetConnectionCount() != 1 {
		t.Fatal("pong responses did not extend connection lifetime")
	}
	_ = client.Close()
	select {
	case <-serverConn.Done():
	case <-time.After(time.Second):
		t.Fatal("normal close did not stop heartbeat lifecycle")
	}
	select {
	case <-readDone:
	case <-time.After(time.Second):
		t.Fatal("client read goroutine did not exit")
	}
}

func TestWebSocketMissingPongCleansConnection(t *testing.T) {
	config := testWebSocketConfig()
	config.authTimeout = 60 * time.Millisecond
	config.pingInterval = 10 * time.Millisecond
	handler := testWebSocketHandler(config)
	url, closeServer := startWebSocketTestServer(t, handler)
	defer closeServer()
	client, _, err := websocket.DefaultDialer.Dial(url, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer client.Close()
	serverConn := waitForConnection(t, handler, client)
	serverConn.MarkAuthenticated("")
	// Do not read from the client, so ping control frames are never processed and no pong is sent.
	waitForConnectionCount(t, handler, 0)
	select {
	case <-serverConn.Done():
	case <-time.After(time.Second):
		t.Fatal("heartbeat goroutine did not exit after timeout")
	}
}

func waitForConnectionCount(t *testing.T, handler *WebSocketHandler, want int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if handler.connManager.GetConnectionCount() == want {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("connection count did not become %d; got %d", want, handler.connManager.GetConnectionCount())
}
