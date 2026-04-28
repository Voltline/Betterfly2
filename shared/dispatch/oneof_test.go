package dispatch

import (
	"errors"
	"testing"
)

type testPayload struct {
	Value string
}

type unknownPayload struct{}

func TestOneofRouterDispatchesRegisteredPayload(t *testing.T) {
	router := NewOneofRouter[string, string]()
	Register(router, func(prefix string, payload *testPayload) (string, error) {
		return prefix + payload.Value, nil
	})

	got, err := router.Dispatch("hello ", &testPayload{Value: "betterfly"})
	if err != nil {
		t.Fatalf("Dispatch returned error: %v", err)
	}
	if got != "hello betterfly" {
		t.Fatalf("unexpected dispatch result: %q", got)
	}
}

func TestOneofRouterRejectsUnregisteredPayload(t *testing.T) {
	router := NewOneofRouter[string, string]()
	Register(router, func(prefix string, payload *testPayload) (string, error) {
		return prefix + payload.Value, nil
	})

	if _, err := router.Dispatch("hello ", &unknownPayload{}); !errors.Is(err, ErrUnregisteredPayload) {
		t.Fatal("expected error for unregistered payload")
	}
}

func TestOneofRouterRejectsDuplicateRegistration(t *testing.T) {
	router := NewOneofRouter[string, string]()
	Register(router, func(prefix string, payload *testPayload) (string, error) {
		return prefix + payload.Value, nil
	})

	defer func() {
		if recover() == nil {
			t.Fatal("expected panic for duplicate registration")
		}
	}()
	Register(router, func(prefix string, payload *testPayload) (string, error) {
		return prefix + payload.Value, nil
	})
}
