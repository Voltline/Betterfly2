package connection

import "testing"

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
