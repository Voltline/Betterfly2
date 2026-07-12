package handlers

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	pb "Betterfly2/proto/data_forwarding"
	"data_forwarding_service/internal/monitor"
)

func TestDecorateMonitorContactsRequiresAdminAndAddedState(t *testing.T) {
	original := []*pb.ContactInfo{{UserId: 42, Name: "Alice"}}
	if got := decorateMonitorContacts(2, true, original); len(got) != 1 {
		t.Fatalf("non-admin received monitor contact: %+v", got)
	}
	if got := decorateMonitorContacts(monitor.AdminUserID, false, original); len(got) != 1 {
		t.Fatalf("monitor appeared before it was added: %+v", got)
	}

	got := decorateMonitorContacts(monitor.AdminUserID, true, original)
	if len(got) != 2 || got[1].GetUserId() != monitor.CurrentProfile().UserID || got[1].GetAccount() != "monitor" {
		t.Fatalf("unexpected monitor contact: %+v", got)
	}
}

func TestDecorateMonitorContactsDoesNotDuplicateReservedID(t *testing.T) {
	profile := monitor.CurrentProfile()
	original := []*pb.ContactInfo{{UserId: profile.UserID, Name: "existing"}}
	got := decorateMonitorContacts(monitor.AdminUserID, true, original)
	if len(got) != 1 || got[0].GetName() != profile.Name {
		t.Fatalf("reserved database contact was not replaced by the virtual profile: %+v", got)
	}

	got = decorateMonitorContacts(2, true, original)
	if len(got) != 0 {
		t.Fatalf("reserved database contact leaked to a non-admin: %+v", got)
	}
}

func TestMonitorStatusLineDoesNotExposeUnboundedErrors(t *testing.T) {
	line := monitorStatusLine("Storage", 0, errors.New(strings.Repeat("sensitive", 40)))
	if len(line) > 140 || !strings.Contains(line, "unavailable") {
		t.Fatalf("unexpected status line: %q", line)
	}
}

func TestRunMonitorHTTPProbeUsesReadinessStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()

	err := runMonitorProbe(context.Background(), monitorProbe{name: "test", kind: "http", addr: server.URL})
	if err == nil || !strings.Contains(err.Error(), "503") {
		t.Fatalf("expected HTTP readiness failure, got %v", err)
	}
}
