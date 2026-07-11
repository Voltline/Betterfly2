package push

import (
	"errors"
	"testing"
)

func TestAPNSErrorClassification(t *testing.T) {
	tests := []struct {
		name        string
		err         APNSError
		invalidates bool
		retryable   bool
	}{
		{name: "gone", err: APNSError{StatusCode: 410}, invalidates: true},
		{name: "bad token", err: APNSError{StatusCode: 400, Reason: "BadDeviceToken"}, invalidates: true},
		{name: "wrong topic", err: APNSError{StatusCode: 400, Reason: "DeviceTokenNotForTopic"}, invalidates: true},
		{name: "rate limited", err: APNSError{StatusCode: 429}, retryable: true},
		{name: "server error", err: APNSError{StatusCode: 500}, retryable: true},
		{name: "unavailable", err: APNSError{StatusCode: 503}, retryable: true},
		{name: "bad payload", err: APNSError{StatusCode: 400, Reason: "BadPayload"}},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.err.InvalidatesToken(); got != tt.invalidates {
				t.Fatalf("InvalidatesToken()=%v want %v", got, tt.invalidates)
			}
			if got := tt.err.Retryable(); got != tt.retryable {
				t.Fatalf("Retryable()=%v want %v", got, tt.retryable)
			}
		})
	}
}

func TestAPNSErrorMessageAndUnavailableSender(t *testing.T) {
	if got := (&APNSError{}).Error(); got != "APNs request failed" {
		t.Fatalf("unexpected empty reason message: %q", got)
	}
	if got := (&APNSError{Reason: "BadPayload"}).Error(); got != "APNs request failed: BadPayload" {
		t.Fatalf("unexpected reason message: %q", got)
	}

	sender := UnavailableSender{}
	if !errors.Is(sender.Ready(), ErrAPNSUnavailable) {
		t.Fatalf("expected default unavailable error, got %v", sender.Ready())
	}
	custom := errors.New("credentials missing")
	sender = UnavailableSender{Err: custom}
	if !errors.Is(sender.Ready(), custom) {
		t.Fatalf("expected custom readiness error, got %v", sender.Ready())
	}
	if _, err := sender.Send(t.Context(), Notification{}); !errors.Is(err, custom) {
		t.Fatalf("expected send to return readiness error, got %v", err)
	}
}
